package domain

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/abby-protocol/abby/internal/authz"
	"github.com/abby-protocol/abby/internal/events"
	"github.com/abby-protocol/abby/internal/ids"
)

type BoardView struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	OwnerActorID   string    `json:"owner_actor_id"`
	Revision       int64     `json:"revision"`
	PolicyRevision int64     `json:"policy_revision"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	CallerIsAdmin  bool      `json:"caller_is_board_admin"`
}

func boardViewOf(b Board, pol *authz.Policy) BoardView {
	return BoardView{ID: b.ID, Name: b.Name, OwnerActorID: b.OwnerActorID, Revision: b.Revision, PolicyRevision: b.PolicyRevision,
		CreatedAt: b.CreatedAt, UpdatedAt: b.UpdatedAt, CallerIsAdmin: pol.IsBoardAdmin()}
}

type CreateBoardRequest struct {
	Name         string `json:"name"`
	OwnerActorID string `json:"owner_actor_id"`
}

func validName(n string, max int) error {
	if strings.TrimSpace(n) == "" || len(n) > max || !utf8.ValidString(n) {
		return Validation("name must be 1..%d bytes of UTF-8", max)
	}
	return nil
}

// CreateBoard requires server administration in the initial protocol
// (ADR-002). The owner holds the board_admin role but no content access until
// granted.
func (s *Service) CreateBoard(ctx context.Context, p *Principal, idem *Idem, req CreateBoardRequest) (*Result, error) {
	if err := requireServerAdmin(p); err != nil {
		return nil, err
	}
	if err := validName(req.Name, 200); err != nil {
		return nil, err
	}
	if req.OwnerActorID == "" {
		req.OwnerActorID = p.ActorID
	}
	idem.Request = req
	return s.serverMutation(ctx, p, idem, func(tx pgx.Tx, now time.Time) (*Result, error) {
		if _, err := getActor(ctx, tx, req.OwnerActorID); err != nil {
			return nil, err
		}
		b := Board{ID: ids.New("brd"), Name: req.Name, OwnerActorID: req.OwnerActorID, Revision: 1, PolicyRevision: 1, CreatedAt: now, UpdatedAt: now}
		if _, err := tx.Exec(ctx, `INSERT INTO boards(id, name, owner_actor_id, created_at, updated_at) VALUES ($1,$2,$3,$4,$4)`, b.ID, b.Name, b.OwnerActorID, now); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO board_heads(board_id) VALUES ($1)`, b.ID); err != nil {
			return nil, err
		}
		if err := writeAudit(ctx, tx, now, &b.ID, p, "board.create", "board", b.ID, "", map[string]any{"owner_actor_id": b.OwnerActorID}); err != nil {
			return nil, err
		}
		pol := &authz.Policy{IsOwner: b.OwnerActorID == p.ActorID, Scopes: p.Scopes, BoardInScope: p.boardInScope(b.ID)}
		return &Result{Status: 201, ResourceType: "board", ResourceID: b.ID, Body: boardViewOf(b, pol)}, nil
	})
}

func (s *Service) GetBoard(ctx context.Context, p *Principal, boardID string) (*BoardView, error) {
	v, err := s.viewBoard(ctx, s.DB, p, boardID)
	if err != nil {
		return nil, err
	}
	bv := boardViewOf(v.board, v.policy)
	return &bv, nil
}

// ListBoards returns boards whose metadata the caller may see.
func (s *Service) ListBoards(ctx context.Context, p *Principal) ([]BoardView, error) {
	rows, err := s.DB.Query(ctx, `SELECT DISTINCT b.id FROM boards b LEFT JOIN grants g ON g.board_id=b.id AND g.revoked_at IS NULL
		WHERE b.owner_actor_id=$1 OR (g.effect='allow' AND (g.subject_type='any_actor' OR g.subject_actor_id=$1)) ORDER BY b.id`, p.ActorID)
	if err != nil {
		return nil, err
	}
	idList, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	out := []BoardView{}
	for _, id := range idList {
		bv, err := s.GetBoard(ctx, p, id)
		var de *Error
		if errors.As(err, &de) && de.Status == 404 {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, *bv)
	}
	slices.SortFunc(out, func(a, b BoardView) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return out, nil
}

type PatchBoardRequest struct {
	Name *string `json:"name"`
}

func (s *Service) PatchBoard(ctx context.Context, p *Principal, boardID string, ifMatch int64, req PatchBoardRequest) (*BoardView, error) {
	if req.Name != nil {
		if err := validName(*req.Name, 200); err != nil {
			return nil, err
		}
	}
	var out BoardView
	_, err := s.boardMutation(ctx, p, boardID, nil, func(b *btx) (*Result, error) {
		if !b.policy.IsBoardAdmin() {
			return nil, Forbidden("board_admin required")
		}
		if b.board.Revision != ifMatch {
			return nil, E(412, "revision_conflict", "board revision is %d", b.board.Revision)
		}
		if req.Name != nil {
			b.board.Name = *req.Name
		}
		b.board.Revision++
		b.board.UpdatedAt = b.now
		if _, err := b.Exec(ctx, `UPDATE boards SET name=$2, revision=$3, updated_at=$4 WHERE id=$1`, boardID, b.board.Name, b.board.Revision, b.now); err != nil {
			return nil, err
		}
		if err := b.audit("board.update", "board", boardID, "", map[string]any{"revision": b.board.Revision}); err != nil {
			return nil, err
		}
		out = boardViewOf(b.board, b.policy)
		return &Result{}, nil
	})
	return &out, err
}

// ---- channels ----

type ChannelView struct {
	ID         string    `json:"id"`
	BoardID    string    `json:"board_id"`
	Path       string    `json:"path"`
	AccessMode string    `json:"access_mode"`
	CreatedBy  string    `json:"created_by_actor_id"`
	CreatedAt  time.Time `json:"created_at"`
	CallerCaps []string  `json:"caller_capabilities"`
}

func channelViewOf(boardID string, c channelRow, pol *authz.Policy) ChannelView {
	return ChannelView{ID: c.ID, BoardID: boardID, Path: c.Path, AccessMode: c.AccessMode, CreatedBy: c.CreatedBy, CreatedAt: c.CreatedAt, CallerCaps: pol.Caps(c.Path)}
}

type CreateChannelRequest struct {
	Path       string `json:"path"`
	AccessMode string `json:"access_mode"`
}

func (s *Service) CreateChannel(ctx context.Context, p *Principal, boardID string, idem *Idem, req CreateChannelRequest) (*Result, error) {
	if err := authz.ValidatePath(req.Path); err != nil {
		return nil, Validation("%s", err)
	}
	if req.AccessMode == "" {
		req.AccessMode = "inherited"
	}
	if req.AccessMode != "inherited" && req.AccessMode != "private" {
		return nil, Validation("access_mode must be inherited or private")
	}
	idem.Request = req
	return s.boardMutation(ctx, p, boardID, idem, func(b *btx) (*Result, error) {
		if !b.policy.Has(req.Path, authz.ChannelManage) {
			return nil, Forbidden("channel_manage required at %q", req.Path)
		}
		if _, exists := b.chans[req.Path]; exists {
			return nil, E(409, "already_exists", "channel %q already exists", req.Path)
		}
		c, err := b.createChannel(req.Path, req.AccessMode)
		if err != nil {
			return nil, err
		}
		return &Result{Status: 201, ResourceType: "channel", ResourceID: c.ID, Body: channelViewOf(boardID, c, b.policy)}, nil
	})
}

func (b *btx) createChannel(path, mode string) (channelRow, error) {
	c := channelRow{ID: ids.New("chn"), Path: path, AccessMode: mode, CreatedBy: b.p.ActorID, CreatedAt: b.now}
	if _, err := b.Exec(b.ctx, `INSERT INTO channels(id, board_id, path, access_mode, created_by, created_at) VALUES ($1,$2,$3,$4,$5,$6)`,
		c.ID, b.board.ID, path, mode, c.CreatedBy, b.now); err != nil {
		return c, err
	}
	b.chans[path] = c
	if mode == "private" {
		// A new private boundary changes effective content access.
		b.policy.PrivatePaths = append(b.policy.PrivatePaths, path)
		if err := b.bumpPolicy(); err != nil {
			return c, err
		}
	}
	if err := b.audit("channel.create", "channel", c.ID, "", map[string]any{"path": path, "access_mode": mode}); err != nil {
		return c, err
	}
	_, err := b.emit(PendingEvent{Type: "channel.created", ChannelID: c.ID, ResourceType: "channel", ResourceID: c.ID, Revision: 1,
		Payload: map[string]any{"channel": ChannelView{ID: c.ID, BoardID: b.board.ID, Path: path, AccessMode: mode, CreatedBy: c.CreatedBy, CreatedAt: b.now, CallerCaps: []string{}}},
		Facets:  []events.Facet{{ChannelPath: path}}})
	return c, err
}

func (b *btx) bumpPolicy() error {
	b.board.PolicyRevision++
	_, err := b.Exec(b.ctx, `UPDATE boards SET policy_revision=$2 WHERE id=$1`, b.board.ID, b.board.PolicyRevision)
	return err
}

func (s *Service) ListChannels(ctx context.Context, p *Principal, boardID string) ([]ChannelView, error) {
	v, err := s.viewBoard(ctx, s.DB, p, boardID)
	if err != nil {
		return nil, err
	}
	admin := v.policy.IsBoardAdmin()
	out := []ChannelView{}
	for _, c := range v.chans {
		if admin || v.policy.Has(c.Path, authz.Read) {
			out = append(out, channelViewOf(boardID, c, v.policy))
		}
	}
	slices.SortFunc(out, func(a, b ChannelView) int { return strings.Compare(a.Path, b.Path) })
	return out, nil
}

func (s *Service) GetChannel(ctx context.Context, p *Principal, boardID, channelID string) (*ChannelView, error) {
	v, err := s.viewBoard(ctx, s.DB, p, boardID)
	if err != nil {
		return nil, err
	}
	c, ok := v.byID[channelID]
	if !ok || !(v.policy.IsBoardAdmin() || v.policy.Has(c.Path, authz.Read)) {
		return nil, NotFound("channel")
	}
	cv := channelViewOf(boardID, c, v.policy)
	return &cv, nil
}

// PermissionsView is effective-permission introspection for the caller.
type PermissionsView struct {
	BoardID         string   `json:"board_id"`
	Channel         string   `json:"channel"`
	Capabilities    []string `json:"capabilities"`
	BoardAdmin      bool     `json:"board_admin"`
	PrivateBoundary *string  `json:"private_boundary"`
	PolicyRevision  int64    `json:"policy_revision"`
}

func (s *Service) Permissions(ctx context.Context, p *Principal, boardID, path string) (*PermissionsView, error) {
	if err := authz.ValidatePath(path); err != nil {
		return nil, Validation("%s", err)
	}
	v, err := s.viewBoard(ctx, s.DB, p, boardID)
	if err != nil {
		return nil, err
	}
	pv := &PermissionsView{BoardID: boardID, Channel: path, Capabilities: v.policy.Caps(path), BoardAdmin: v.policy.IsBoardAdmin(), PolicyRevision: v.board.PolicyRevision}
	// Reveal the governing private boundary only to callers who may see it.
	if bnd := v.policy.Boundary(path); bnd != "" && (pv.BoardAdmin || v.policy.Has(bnd, authz.Read)) {
		pv.PrivateBoundary = &bnd
	}
	return pv, nil
}

// ---- grants ----

type GrantSubject struct {
	Type    string  `json:"type"` // "actor" | "any_actor"
	ActorID *string `json:"actor_id,omitempty"`
}

type GrantView struct {
	ID           string       `json:"id"`
	BoardID      string       `json:"board_id"`
	Subject      GrantSubject `json:"subject"`
	Pattern      string       `json:"pattern"`
	Capabilities []string     `json:"capabilities"`
	Effect       string       `json:"effect"`
	Revision     int64        `json:"revision"`
	CreatedBy    string       `json:"created_by_actor_id"`
	CreatedAt    time.Time    `json:"created_at"`
	RevokedAt    *time.Time   `json:"revoked_at"`
}

type CreateGrantRequest struct {
	Subject      GrantSubject `json:"subject"`
	Pattern      string       `json:"pattern"`
	Capabilities []string     `json:"capabilities"`
	Effect       string       `json:"effect"`
}

func validateGrant(pattern string, caps []string, effect string) error {
	if err := authz.ValidatePattern(pattern); err != nil {
		return Validation("pattern: %s", err)
	}
	if len(caps) == 0 {
		return Validation("capabilities must not be empty")
	}
	for _, c := range caps {
		if !authz.ValidGrantCapability(c) {
			return Validation("unknown capability %q", c)
		}
		if c == authz.BoardAdmin && pattern != "*" {
			return Validation("board_admin may only be granted with pattern \"*\"")
		}
	}
	if effect != "allow" && effect != "deny" {
		return Validation("effect must be allow or deny")
	}
	return nil
}

func (s *Service) CreateGrant(ctx context.Context, p *Principal, boardID string, idem *Idem, req CreateGrantRequest) (*Result, error) {
	if req.Effect == "" {
		req.Effect = "allow"
	}
	req.Capabilities = sortedUnique(req.Capabilities)
	if err := validateGrant(req.Pattern, req.Capabilities, req.Effect); err != nil {
		return nil, err
	}
	switch req.Subject.Type {
	case "actor":
		if req.Subject.ActorID == nil || *req.Subject.ActorID == "" {
			return nil, Validation("subject.actor_id required for subject type actor")
		}
	case "any_actor":
		req.Subject.ActorID = nil
	default:
		return nil, Validation("subject.type must be actor or any_actor")
	}
	idem.Request = req
	return s.boardMutation(ctx, p, boardID, idem, func(b *btx) (*Result, error) {
		if !b.policy.IsBoardAdmin() {
			return nil, Forbidden("board_admin required")
		}
		if req.Subject.ActorID != nil {
			if _, err := getActor(ctx, b, *req.Subject.ActorID); err != nil {
				return nil, Validation("subject actor does not exist")
			}
		}
		g := GrantView{ID: ids.New("grt"), BoardID: boardID, Subject: req.Subject, Pattern: req.Pattern, Capabilities: req.Capabilities,
			Effect: req.Effect, Revision: 1, CreatedBy: p.ActorID, CreatedAt: b.now}
		if _, err := b.Exec(ctx, `INSERT INTO grants(id, board_id, subject_type, subject_actor_id, pattern, capabilities, effect, created_by, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, g.ID, boardID, req.Subject.Type, req.Subject.ActorID, g.Pattern, g.Capabilities, g.Effect, p.ActorID, b.now); err != nil {
			return nil, err
		}
		if err := b.bumpPolicy(); err != nil {
			return nil, err
		}
		if err := b.audit("grant.create", "grant", g.ID, "", map[string]any{"grant": g}); err != nil {
			return nil, err
		}
		return &Result{Status: 201, ResourceType: "grant", ResourceID: g.ID, Body: g}, nil
	})
}

const grantCols = `id, board_id, subject_type, subject_actor_id, pattern, capabilities, effect, revision, created_by, created_at, revoked_at`

func scanGrant(r pgx.Row) (GrantView, error) {
	var g GrantView
	err := r.Scan(&g.ID, &g.BoardID, &g.Subject.Type, &g.Subject.ActorID, &g.Pattern, &g.Capabilities, &g.Effect, &g.Revision, &g.CreatedBy, &g.CreatedAt, &g.RevokedAt)
	return g, err
}

// ListGrants returns one page of the board's grants in (created_at, id)
// order and the token for the next page (nil on the last page).
func (s *Service) ListGrants(ctx context.Context, p *Principal, boardID string, includeRevoked bool, pg PageRequest) ([]GrantView, *string, error) {
	has, t, id, err := pg.pageAfter()
	if err != nil {
		return nil, nil, err
	}
	v, err := s.viewBoard(ctx, s.DB, p, boardID)
	if err != nil {
		return nil, nil, err
	}
	if !v.policy.IsBoardAdmin() {
		return nil, nil, Forbidden("board_admin required")
	}
	rows, err := s.DB.Query(ctx, `SELECT `+grantCols+` FROM grants WHERE board_id=$1 AND ($2 OR revoked_at IS NULL)
		AND (NOT $3 OR (created_at, id) > ($4, $5)) ORDER BY created_at, id LIMIT $6`,
		boardID, includeRevoked, has, t, id, pg.Limit+1)
	if err != nil {
		return nil, nil, err
	}
	l, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (GrantView, error) { return scanGrant(r) })
	if err != nil {
		return nil, nil, err
	}
	l, next := trimPage(l, pg.Limit, func(g GrantView) (time.Time, string) { return g.CreatedAt, g.ID })
	return l, next, nil
}

func (s *Service) GetGrant(ctx context.Context, p *Principal, boardID, grantID string) (*GrantView, error) {
	v, err := s.viewBoard(ctx, s.DB, p, boardID)
	if err != nil {
		return nil, err
	}
	if !v.policy.IsBoardAdmin() {
		return nil, Forbidden("board_admin required")
	}
	g, err := scanGrant(s.DB.QueryRow(ctx, `SELECT `+grantCols+` FROM grants WHERE board_id=$1 AND id=$2`, boardID, grantID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, NotFound("grant")
	}
	return &g, err
}

type PatchGrantRequest struct {
	Pattern      *string  `json:"pattern"`
	Capabilities []string `json:"capabilities"`
	Effect       *string  `json:"effect"`
}

func (s *Service) PatchGrant(ctx context.Context, p *Principal, boardID, grantID string, ifMatch int64, req PatchGrantRequest) (*GrantView, error) {
	var out GrantView
	_, err := s.boardMutation(ctx, p, boardID, nil, func(b *btx) (*Result, error) {
		if !b.policy.IsBoardAdmin() {
			return nil, Forbidden("board_admin required")
		}
		g, err := scanGrant(b.QueryRow(ctx, `SELECT `+grantCols+` FROM grants WHERE board_id=$1 AND id=$2 FOR UPDATE`, boardID, grantID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, NotFound("grant")
		}
		if err != nil {
			return nil, err
		}
		if g.RevokedAt != nil {
			return nil, E(409, "invalid_transition", "grant is revoked")
		}
		if g.Revision != ifMatch {
			return nil, E(412, "revision_conflict", "grant revision is %d", g.Revision)
		}
		before := g
		if req.Pattern != nil {
			g.Pattern = *req.Pattern
		}
		if req.Capabilities != nil {
			g.Capabilities = sortedUnique(req.Capabilities)
		}
		if req.Effect != nil {
			g.Effect = *req.Effect
		}
		if err := validateGrant(g.Pattern, g.Capabilities, g.Effect); err != nil {
			return nil, err
		}
		g.Revision++
		if _, err := b.Exec(ctx, `UPDATE grants SET pattern=$2, capabilities=$3, effect=$4, revision=$5 WHERE id=$1`, g.ID, g.Pattern, g.Capabilities, g.Effect, g.Revision); err != nil {
			return nil, err
		}
		if err := b.bumpPolicy(); err != nil {
			return nil, err
		}
		if err := b.audit("grant.update", "grant", g.ID, "", map[string]any{"before": before, "after": g}); err != nil {
			return nil, err
		}
		out = g
		return &Result{}, nil
	})
	return &out, err
}

func (s *Service) RevokeGrant(ctx context.Context, p *Principal, boardID, grantID string) (*GrantView, error) {
	var out GrantView
	_, err := s.boardMutation(ctx, p, boardID, nil, func(b *btx) (*Result, error) {
		if !b.policy.IsBoardAdmin() {
			return nil, Forbidden("board_admin required")
		}
		g, err := scanGrant(b.QueryRow(ctx, `SELECT `+grantCols+` FROM grants WHERE board_id=$1 AND id=$2 FOR UPDATE`, boardID, grantID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, NotFound("grant")
		}
		if err != nil {
			return nil, err
		}
		if g.RevokedAt == nil {
			g.RevokedAt = &b.now
			g.Revision++
			if _, err := b.Exec(ctx, `UPDATE grants SET revoked_at=$2, revision=$3 WHERE id=$1`, g.ID, b.now, g.Revision); err != nil {
				return nil, err
			}
			if err := b.bumpPolicy(); err != nil {
				return nil, err
			}
			if err := b.audit("grant.revoke", "grant", g.ID, "", nil); err != nil {
				return nil, err
			}
		}
		out = g
		return &Result{}, nil
	})
	return &out, err
}

// ---- participants ----

// BoardActors lists participants visible to the caller: actors that can read
// at least one existing channel the caller can read, plus the caller. Board
// administrators see every actor with a grant on the board.
func (s *Service) BoardActors(ctx context.Context, p *Principal, boardID string) ([]ActorView, error) {
	v, err := s.viewBoard(ctx, s.DB, p, boardID)
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT DISTINCT subject_actor_id FROM grants WHERE board_id=$1 AND revoked_at IS NULL AND subject_actor_id IS NOT NULL
		UNION SELECT owner_actor_id FROM boards WHERE id=$1`, boardID)
	if err != nil {
		return nil, err
	}
	cands, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	admin := v.policy.IsBoardAdmin()
	var readable []string
	for _, c := range v.chans {
		if v.policy.Has(c.Path, authz.Read) {
			readable = append(readable, c.Path)
		}
	}
	out := []ActorView{}
	for _, id := range cands {
		visible := admin || id == p.ActorID
		if !visible {
			other := &Principal{ActorID: id, Scopes: []string{authz.AllScopes}}
			pol, _, err := s.loadPolicy(ctx, s.DB, other, &v.board)
			if err != nil {
				return nil, err
			}
			for _, path := range readable {
				if pol.Has(path, authz.Read) {
					visible = true
					break
				}
			}
		}
		if visible {
			a, err := getActor(ctx, s.DB, id)
			if err != nil {
				return nil, err
			}
			out = append(out, *a)
		}
	}
	slices.SortFunc(out, func(a, b ActorView) int { return strings.Compare(a.Handle, b.Handle) })
	return out, nil
}

// ---- audit ----

type AuditView struct {
	ID         string         `json:"id"`
	BoardID    *string        `json:"board_id"`
	ActorID    string         `json:"actor_id"`
	Action     string         `json:"action"`
	TargetType string         `json:"target_type"`
	TargetID   string         `json:"target_id"`
	Reason     *string        `json:"reason"`
	Metadata   map[string]any `json:"metadata"`
	RecordedAt time.Time      `json:"recorded_at"`
}

// ListAudit returns one page of audit events, newest first: (recorded_at,
// id) descending, with the keyset applied in that direction. boardID ""
// lists server-wide events (server administrators only).
func (s *Service) ListAudit(ctx context.Context, p *Principal, boardID string, pg PageRequest) ([]AuditView, *string, error) {
	has, t, id, err := pg.pageAfter()
	if err != nil {
		return nil, nil, err
	}
	if boardID != "" {
		v, err := s.viewBoard(ctx, s.DB, p, boardID)
		if err != nil {
			return nil, nil, err
		}
		if !v.policy.IsBoardAdmin() {
			return nil, nil, Forbidden("board_admin required")
		}
	} else if err := requireServerAdmin(p); err != nil {
		return nil, nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT id, board_id, actor_id, action, target_type, target_id, reason, metadata, recorded_at FROM audit_events
		WHERE ($1='' OR board_id=$1) AND (NOT $2 OR (recorded_at, id) < ($3, $4))
		ORDER BY recorded_at DESC, id DESC LIMIT $5`, boardID, has, t, id, pg.Limit+1)
	if err != nil {
		return nil, nil, err
	}
	l, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (AuditView, error) {
		var a AuditView
		err := r.Scan(&a.ID, &a.BoardID, &a.ActorID, &a.Action, &a.TargetType, &a.TargetID, &a.Reason, &a.Metadata, &a.RecordedAt)
		return a, err
	})
	if err != nil {
		return nil, nil, err
	}
	l, next := trimPage(l, pg.Limit, func(a AuditView) (time.Time, string) { return a.RecordedAt, a.ID })
	return l, next, nil
}
