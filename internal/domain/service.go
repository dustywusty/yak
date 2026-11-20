// Package domain implements Yak's authorization-aware operations. Every
// board mutation runs in one transaction that locks the board control row,
// checks conditions, changes state, appends events at transactional
// positions, and stores its idempotency receipt (design §12.1).
package domain

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dustywusty/yak/internal/authz"
	"github.com/dustywusty/yak/internal/events"
	"github.com/dustywusty/yak/internal/ids"
)

// Error is a protocol error rendered as application/problem+json.
type Error struct {
	Status int
	Code   string
	Detail string
	Extra  map[string]any
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

func E(status int, code, format string, a ...any) *Error {
	return &Error{Status: status, Code: code, Detail: fmt.Sprintf(format, a...)}
}

func Validation(format string, a ...any) *Error { return E(400, "validation_failed", format, a...) }
func Forbidden(format string, a ...any) *Error  { return E(403, "forbidden", format, a...) }
func NotFound(what string) *Error               { return E(404, "not_found", "%s not found", what) }

var errUnauthenticated = E(401, "unauthenticated", "missing, invalid, expired, or revoked credential")

// Config holds advertised limits and retention policies (discovery).
type Config struct {
	Version              string
	IdempotencyRetention time.Duration
	EventRetention       time.Duration
	SnapshotTTL          time.Duration
	SnapshotMaxItems     int
	MaxWait              time.Duration
	MaxTextBytes         int
	MaxTitleBytes        int
	MaxLabelsPerMessage  int
	MaxLabelValueBytes   int
	MaxPageSize          int
	DefaultPageSize      int
	EventScanBatch       int
	CredentialDefaultTTL time.Duration
	CredentialMaxTTL     time.Duration

	// P2 tasks (spec/p2-tasks.md TASK-002).
	ClaimLeaseDefault      time.Duration
	ClaimLeaseMin          time.Duration
	ClaimLeaseMax          time.Duration
	MaxBatchTasks          int
	MaxDependenciesPerTask int
	MaxTaskResultBytes     int
	MaxEvidenceItems       int
	MaxClaimWait           time.Duration
}

func DefaultConfig() Config {
	return Config{
		Version:              "0.1.0-dev",
		IdempotencyRetention: 24 * time.Hour,
		EventRetention:       30 * 24 * time.Hour,
		SnapshotTTL:          10 * time.Minute,
		SnapshotMaxItems:     10000,
		MaxWait:              30 * time.Second,
		MaxTextBytes:         64 * 1024,
		MaxTitleBytes:        300,
		MaxLabelsPerMessage:  64,
		MaxLabelValueBytes:   256,
		MaxPageSize:          200,
		DefaultPageSize:      50,
		EventScanBatch:       500,
		CredentialDefaultTTL: 30 * 24 * time.Hour,
		CredentialMaxTTL:     365 * 24 * time.Hour,

		ClaimLeaseDefault:      120 * time.Second,
		ClaimLeaseMin:          15 * time.Second,
		ClaimLeaseMax:          300 * time.Second,
		MaxBatchTasks:          100,
		MaxDependenciesPerTask: 100,
		MaxTaskResultBytes:     16384,
		MaxEvidenceItems:       50,
		MaxClaimWait:           30 * time.Second,
	}
}

// Service is the domain layer. It is safe for concurrent use.
type Service struct {
	DB  *pgxpool.Pool
	Cfg Config
	Hub *Hub
	Now func() time.Time
	// ClaimKeys encrypts claim-proof replay material (ADR-0004 §3). Nil
	// means claim_proof_replay is false and no ciphertext is stored.
	ClaimKeys *ClaimKeyring
}

func New(db *pgxpool.Pool, cfg Config) *Service {
	return &Service{DB: db, Cfg: cfg, Hub: NewHub(), // Microsecond precision matches Postgres timestamptz, so a
		// timestamp in an event payload equals the same value read back.
		Now: func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }}
}

// Principal is the authenticated caller: the effective actor and the
// credential that bounds its authority.
type Principal struct {
	ActorID      string
	Handle       string
	ServerAdmin  bool // actor attribute AND credential scope
	CredentialID string
	Scopes       []string
	BoardIDs     []string // nil = no board restriction
	ExpiresAt    time.Time
	RunID        string
	// System marks server housekeeping (the claim-expiry sweeper). It holds
	// no grants; its board transactions skip the visibility check.
	System bool
}

func (p *Principal) boardInScope(boardID string) bool {
	if p.BoardIDs == nil {
		return true
	}
	for _, b := range p.BoardIDs {
		if b == boardID {
			return true
		}
	}
	return false
}

// Board is the locked board state inside a mutation.
type Board struct {
	ID             string
	Name           string
	OwnerActorID   string
	Revision       int64
	PolicyRevision int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// PendingEvent is appended inside a board transaction.
type PendingEvent struct {
	Type         string
	ChannelID    string
	ThreadID     string
	ResourceType string
	ResourceID   string
	Revision     int64
	Payload      any
	Facets       []events.Facet
	EffectiveAt  time.Time
}

// btx is one board mutation transaction.
type btx struct {
	pgx.Tx
	ctx    context.Context
	s      *Service
	p      *Principal
	board  Board
	head   int64
	now    time.Time
	policy *authz.Policy
	chans  map[string]channelRow // by path
}

func (b *btx) emit(ev PendingEvent) (int64, error) {
	b.head++
	if ev.EffectiveAt.IsZero() {
		ev.EffectiveAt = b.now
	}
	payload, err := json.Marshal(ev.Payload)
	if err != nil {
		return 0, err
	}
	facets, err := json.Marshal(ev.Facets)
	if err != nil {
		return 0, err
	}
	var chID, thID, runID, credID *string
	if b.p.CredentialID != "" {
		credID = &b.p.CredentialID
	}
	if ev.ChannelID != "" {
		chID = &ev.ChannelID
	}
	if ev.ThreadID != "" {
		thID = &ev.ThreadID
	}
	if b.p.RunID != "" {
		runID = &b.p.RunID
	}
	_, err = b.Exec(b.ctx, `INSERT INTO events(board_id, position, id, type, actor_id, credential_id, run_id,
		channel_id, thread_id, resource_type, resource_id, resource_revision, payload, facets, recorded_at, effective_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		b.board.ID, b.head, ids.New("evt"), ev.Type, b.p.ActorID, credID, runID,
		chID, thID, ev.ResourceType, ev.ResourceID, ev.Revision, payload, facets, b.now, ev.EffectiveAt)
	return b.head, err
}

func (b *btx) audit(action, targetType, targetID, reason string, meta map[string]any) error {
	return writeAudit(b.ctx, b.Tx, b.now, &b.board.ID, b.p, action, targetType, targetID, reason, meta)
}

func writeAudit(ctx context.Context, tx pgx.Tx, now time.Time, boardID *string, p *Principal, action, targetType, targetID, reason string, meta map[string]any) error {
	if meta == nil {
		meta = map[string]any{}
	}
	mb, _ := json.Marshal(meta)
	var r *string
	if reason != "" {
		r = &reason
	}
	_, err := tx.Exec(ctx, `INSERT INTO audit_events(id, board_id, actor_id, credential_id, action, target_type, target_id, reason, metadata, recorded_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, ids.New("aud"), boardID, p.ActorID, p.CredentialID, action, targetType, targetID, r, mb, now)
	return err
}

// Idem describes the idempotency scope of a mutation (ADR-003).
type Idem struct {
	Key       string
	Namespace string // board ID or "server"
	Method    string
	Path      string
	Request   any // normalized request; its canonical JSON is fingerprinted
}

func (i *Idem) fingerprint() []byte {
	b, _ := json.Marshal(i.Request)
	h := sha256.Sum256(append([]byte(i.Method+" "+i.Path+"\n"), b...))
	return h[:]
}

func (i *Idem) lockKey(actorID string) int64 {
	h := fnv.New64a()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s", actorID, i.Namespace, i.Method, i.Path, i.Key)
	return int64(h.Sum64())
}

// Result identifies the committed resource of a mutation. On idempotent
// replay the HTTP layer regenerates the current, authorized projection of
// that resource rather than returning stored bytes, so redaction applies.
type Result struct {
	Status       int
	ResourceType string
	ResourceID   string
	Replayed     bool
	Body         any // set on first execution; nil on replay
}

// checkReceipt takes the idempotency advisory lock for the remainder of tx and
// returns a replay Result if a matching receipt exists.
func (s *Service) checkReceipt(ctx context.Context, tx pgx.Tx, p *Principal, idem *Idem) (*Result, error) {
	if idem == nil {
		return nil, nil
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, idem.lockKey(p.ActorID)); err != nil {
		return nil, err
	}
	var fp []byte
	var r Result
	err := tx.QueryRow(ctx, `SELECT fingerprint, status, resource_type, resource_id FROM idempotency_receipts
		WHERE actor_id=$1 AND namespace=$2 AND method=$3 AND path=$4 AND key=$5 AND expires_at > now()`,
		p.ActorID, idem.Namespace, idem.Method, idem.Path, idem.Key).Scan(&fp, &r.Status, &r.ResourceType, &r.ResourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if string(fp) != string(idem.fingerprint()) {
		return nil, E(409, "idempotency_conflict", "Idempotency-Key was already used with different request content")
	}
	r.Replayed = true
	return &r, nil
}

func (s *Service) storeReceipt(ctx context.Context, tx pgx.Tx, p *Principal, idem *Idem, r *Result, now time.Time) error {
	if idem == nil {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO idempotency_receipts(actor_id, namespace, method, path, key, fingerprint, status, resource_type, resource_id, created_at, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (actor_id, namespace, method, path, key) DO UPDATE SET fingerprint=EXCLUDED.fingerprint,
		  status=EXCLUDED.status, resource_type=EXCLUDED.resource_type, resource_id=EXCLUDED.resource_id,
		  created_at=EXCLUDED.created_at, expires_at=EXCLUDED.expires_at`,
		p.ActorID, idem.Namespace, idem.Method, idem.Path, idem.Key, idem.fingerprint(), r.Status, r.ResourceType, r.ResourceID,
		now, now.Add(s.Cfg.IdempotencyRetention))
	return err
}

// boardMutation runs fn inside a board transaction. Lock order: idempotency
// advisory lock, then the board_heads row, then resource rows.
func (s *Service) boardMutation(ctx context.Context, p *Principal, boardID string, idem *Idem, fn func(b *btx) (*Result, error)) (*Result, error) {
	var out *Result
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if r, err := s.checkReceipt(ctx, tx, p, idem); err != nil || r != nil {
			out = r
			return err
		}
		b := &btx{Tx: tx, ctx: ctx, s: s, p: p}
		var head int64
		err := tx.QueryRow(ctx, `SELECT h.head_position, b.id, b.name, b.owner_actor_id, b.revision, b.policy_revision, b.created_at, b.updated_at
			FROM board_heads h JOIN boards b ON b.id=h.board_id WHERE h.board_id=$1 FOR UPDATE OF h`, boardID).
			Scan(&head, &b.board.ID, &b.board.Name, &b.board.OwnerActorID, &b.board.Revision, &b.board.PolicyRevision, &b.board.CreatedAt, &b.board.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return NotFound("board")
		}
		if err != nil {
			return err
		}
		b.head = head
		// Authoritative time is sampled after acquiring the board lock.
		b.now = s.Now()
		pol, chans, err := s.loadPolicy(ctx, tx, p, &b.board)
		if err != nil {
			return err
		}
		if !p.System && !pol.IsBoardAdmin() && !pol.HasAnyAllow() {
			return NotFound("board")
		}
		b.policy, b.chans = pol, chans
		res, err := fn(b)
		if err != nil {
			return err
		}
		if b.head != head {
			if _, err := tx.Exec(ctx, `UPDATE board_heads SET head_position=$2 WHERE board_id=$1`, boardID, b.head); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `SELECT pg_notify('yak_events', $1)`, boardID); err != nil {
				return err
			}
		}
		if err := s.storeReceipt(ctx, tx, p, idem, res, b.now); err != nil {
			return err
		}
		out = res
		return nil
	})
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			return nil, E(409, "already_exists", "a resource with these unique attributes already exists")
		}
		return nil, err
	}
	return out, nil
}

// serverMutation runs fn in a transaction for server-namespace operations.
func (s *Service) serverMutation(ctx context.Context, p *Principal, idem *Idem, fn func(tx pgx.Tx, now time.Time) (*Result, error)) (*Result, error) {
	var out *Result
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if r, err := s.checkReceipt(ctx, tx, p, idem); err != nil || r != nil {
			out = r
			return err
		}
		now := s.Now()
		res, err := fn(tx, now)
		if err != nil {
			return err
		}
		if err := s.storeReceipt(ctx, tx, p, idem, res, now); err != nil {
			return err
		}
		out = res
		return nil
	})
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			return nil, E(409, "already_exists", "a resource with these unique attributes already exists")
		}
		return nil, err
	}
	return out, nil
}

// querier is satisfied by both the pool and transactions.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type channelRow struct {
	ID         string
	Path       string
	AccessMode string
	CreatedBy  string
	CreatedAt  time.Time
}

// loadPolicy evaluates the caller's policy for a board and returns every
// channel on the board keyed by path.
func (s *Service) loadPolicy(ctx context.Context, q querier, p *Principal, board *Board) (*authz.Policy, map[string]channelRow, error) {
	pol := &authz.Policy{IsOwner: board.OwnerActorID == p.ActorID, Scopes: p.Scopes, BoardInScope: p.boardInScope(board.ID)}
	rows, err := q.Query(ctx, `SELECT id, pattern, capabilities, effect FROM grants
		WHERE board_id=$1 AND revoked_at IS NULL AND (subject_type='any_actor' OR subject_actor_id=$2)`, board.ID, p.ActorID)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var g authz.Grant
		if err := rows.Scan(&g.ID, &g.Pattern, &g.Caps, &g.Effect); err != nil {
			return nil, nil, err
		}
		pol.Grants = append(pol.Grants, g)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	chans, err := loadChannels(ctx, q, board.ID)
	if err != nil {
		return nil, nil, err
	}
	for _, c := range chans {
		if c.AccessMode == "private" {
			pol.PrivatePaths = append(pol.PrivatePaths, c.Path)
		}
	}
	return pol, chans, nil
}

func loadChannels(ctx context.Context, q querier, boardID string) (map[string]channelRow, error) {
	rows, err := q.Query(ctx, `SELECT id, path, access_mode, created_by, created_at FROM channels WHERE board_id=$1`, boardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]channelRow{}
	for rows.Next() {
		var c channelRow
		if err := rows.Scan(&c.ID, &c.Path, &c.AccessMode, &c.CreatedBy, &c.CreatedAt); err != nil {
			return nil, err
		}
		out[c.Path] = c
	}
	return out, rows.Err()
}

// boardView is a read-side context: board + evaluated policy, no lock.
type boardView struct {
	board  Board
	policy *authz.Policy
	chans  map[string]channelRow
	byID   map[string]channelRow
}

func (v *boardView) canRead(channelID string) (channelRow, bool) {
	c, ok := v.byID[channelID]
	if !ok {
		return c, false
	}
	return c, v.policy.Has(c.Path, authz.Read)
}

func (s *Service) viewBoard(ctx context.Context, q querier, p *Principal, boardID string) (*boardView, error) {
	v := &boardView{}
	err := q.QueryRow(ctx, `SELECT id, name, owner_actor_id, revision, policy_revision, created_at, updated_at FROM boards WHERE id=$1`, boardID).
		Scan(&v.board.ID, &v.board.Name, &v.board.OwnerActorID, &v.board.Revision, &v.board.PolicyRevision, &v.board.CreatedAt, &v.board.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, NotFound("board")
	}
	if err != nil {
		return nil, err
	}
	pol, chans, err := s.loadPolicy(ctx, q, p, &v.board)
	if err != nil {
		return nil, err
	}
	if !pol.IsBoardAdmin() && !pol.HasAnyAllow() {
		return nil, NotFound("board")
	}
	v.policy, v.chans = pol, chans
	v.byID = make(map[string]channelRow, len(chans))
	for _, c := range chans {
		v.byID[c.ID] = c
	}
	return v, nil
}

func (s *Service) historyEpoch(ctx context.Context, q querier) (string, error) {
	var e string
	err := q.QueryRow(ctx, `SELECT history_epoch FROM server_state`).Scan(&e)
	return e, err
}

func jsonOf(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
