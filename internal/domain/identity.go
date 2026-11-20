package domain

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dustywusty/abby/internal/authz"
	"github.com/dustywusty/abby/internal/ids"
)

// ActorView is the public projection of an actor.
type ActorView struct {
	ID           string    `json:"id"`
	Kind         string    `json:"kind"`
	Handle       string    `json:"handle"`
	DisplayName  string    `json:"display_name"`
	OwnerActorID *string   `json:"owner_actor_id"`
	ServerAdmin  bool      `json:"server_admin"`
	CreatedAt    time.Time `json:"created_at"`
}

// CredentialView never includes the secret or verifier.
type CredentialView struct {
	ID        string     `json:"id"`
	ActorID   string     `json:"actor_id"`
	Label     string     `json:"label"`
	Scopes    []string   `json:"scopes"`
	BoardIDs  []string   `json:"board_ids"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	RevokedAt *time.Time `json:"revoked_at"`
}

// IssuedCredential is returned exactly once, on first issuance.
type IssuedCredential struct {
	Credential CredentialView `json:"credential"`
	Token      string         `json:"token,omitempty"`
	// TokenUnavailable is true on idempotent replay: the secret is never
	// stored, so a lost response must be handled by revoking and reissuing.
	TokenUnavailable bool `json:"token_unavailable,omitempty"`
}

const tokenPrefix = "abby."

func verifier(secret string) []byte {
	h := sha256.Sum256([]byte(secret))
	return h[:]
}

// Authenticate resolves a bearer token. Token format: "abby.<credential_id>.<secret>".
func (s *Service) Authenticate(ctx context.Context, token string) (*Principal, error) {
	rest, ok := strings.CutPrefix(token, tokenPrefix)
	if !ok {
		return nil, errUnauthenticated
	}
	credID, secret, ok := strings.Cut(rest, ".")
	if !ok || credID == "" || secret == "" {
		return nil, errUnauthenticated
	}
	var (
		p        Principal
		ver      []byte
		revoked  *time.Time
		disabled *time.Time
		actorAdm bool
	)
	err := s.DB.QueryRow(ctx, `SELECT c.id, c.actor_id, a.handle, a.server_admin, c.scopes, c.board_ids, c.verifier, c.expires_at, c.revoked_at, a.disabled_at
		FROM credentials c JOIN actors a ON a.id=c.actor_id WHERE c.id=$1`, credID).
		Scan(&p.CredentialID, &p.ActorID, &p.Handle, &actorAdm, &p.Scopes, &p.BoardIDs, &ver, &p.ExpiresAt, &revoked, &disabled)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errUnauthenticated
		}
		return nil, err
	}
	if subtle.ConstantTimeCompare(ver, verifier(secret)) != 1 || revoked != nil || disabled != nil || !s.Now().Before(p.ExpiresAt) {
		return nil, errUnauthenticated
	}
	// A self-issued credential is only as valid as its issuance chain:
	// revoking a credential disables everything its actor minted from it.
	// The chain stops at credentials issued by a different actor.
	var brokenAncestor bool
	if err := s.DB.QueryRow(ctx, `WITH RECURSIVE chain(id, actor_id, parent, revoked_at, depth) AS (
			SELECT id, actor_id, issued_by_credential_id, revoked_at, 0 FROM credentials WHERE id=$1
			UNION ALL
			SELECT c.id, c.actor_id, c.issued_by_credential_id, c.revoked_at, chain.depth+1
			FROM credentials c JOIN chain ON c.id = chain.parent AND c.actor_id = chain.actor_id WHERE chain.depth < 64)
		SELECT EXISTS(SELECT 1 FROM chain WHERE depth > 0 AND revoked_at IS NOT NULL)`, credID).Scan(&brokenAncestor); err != nil {
		return nil, err
	}
	if brokenAncestor {
		return nil, errUnauthenticated
	}
	p.ServerAdmin = actorAdm && (slices.Contains(p.Scopes, authz.AllScopes) || slices.Contains(p.Scopes, authz.ServerAdmin))
	return &p, nil
}

func requireServerAdmin(p *Principal) error {
	if !p.ServerAdmin {
		return Forbidden("server administration required")
	}
	return nil
}

var handleRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// ErrAlreadyBootstrapped is returned when a server administrator exists.
var ErrAlreadyBootstrapped = errors.New("server already has an administrator; bootstrap refused")

// Bootstrap creates the first server administrator and returns its token. It
// is an operator command executed against the database, refused once any
// server administrator exists. There is no default secret.
func (s *Service) Bootstrap(ctx context.Context, handle, displayName string, ttl time.Duration) (*ActorView, *IssuedCredential, error) {
	if !handleRE.MatchString(handle) {
		return nil, nil, errors.New("handle must match [a-z0-9][a-z0-9_-]{0,62}")
	}
	var av *ActorView
	var ic *IssuedCredential
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `LOCK TABLE actors IN EXCLUSIVE MODE`); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM actors WHERE server_admin`).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrAlreadyBootstrapped
		}
		now := s.Now()
		a := ActorView{ID: ids.New("act"), Kind: "human", Handle: handle, DisplayName: displayName, ServerAdmin: true, CreatedAt: now}
		if _, err := tx.Exec(ctx, `INSERT INTO actors(id, kind, handle, display_name, server_admin, created_at) VALUES ($1,$2,$3,$4,true,$5)`,
			a.ID, a.Kind, a.Handle, a.DisplayName, now); err != nil {
			return err
		}
		var err error
		ic, err = insertCredential(ctx, tx, now, a.ID, nil, "bootstrap", []string{authz.AllScopes}, nil, now.Add(ttl))
		if err != nil {
			return err
		}
		sys := &Principal{ActorID: a.ID, CredentialID: ic.Credential.ID}
		av = &a
		return writeAudit(ctx, tx, now, nil, sys, "server.bootstrap", "actor", a.ID, "", nil)
	})
	return av, ic, err
}

func insertCredential(ctx context.Context, tx pgx.Tx, now time.Time, actorID string, issuer *string, label string, scopes, boardIDs []string, exp time.Time) (*IssuedCredential, error) {
	id := ids.New("cred")
	secret := ids.Secret()
	if _, err := tx.Exec(ctx, `INSERT INTO credentials(id, actor_id, issued_by_credential_id, label, scopes, board_ids, verifier, created_at, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, actorID, issuer, label, scopes, boardIDs, verifier(secret), now, exp); err != nil {
		return nil, err
	}
	return &IssuedCredential{
		Credential: CredentialView{ID: id, ActorID: actorID, Label: label, Scopes: scopes, BoardIDs: boardIDs, CreatedAt: now, ExpiresAt: exp},
		Token:      tokenPrefix + id + "." + secret,
	}, nil
}

// CreateActorRequest is the normalized body of POST /v1/actors.
type CreateActorRequest struct {
	Kind         string  `json:"kind"`
	Handle       string  `json:"handle"`
	DisplayName  string  `json:"display_name"`
	OwnerActorID *string `json:"owner_actor_id"`
}

func (s *Service) CreateActor(ctx context.Context, p *Principal, idem *Idem, req CreateActorRequest) (*Result, error) {
	if err := requireServerAdmin(p); err != nil {
		return nil, err
	}
	req.Handle = strings.ToLower(req.Handle)
	if !handleRE.MatchString(req.Handle) {
		return nil, Validation("handle must match [a-z0-9][a-z0-9_-]{0,62}")
	}
	if !slices.Contains([]string{"human", "agent", "service"}, req.Kind) {
		return nil, Validation("kind must be human, agent, or service")
	}
	if req.DisplayName == "" {
		req.DisplayName = req.Handle
	}
	if len(req.DisplayName) > 200 {
		return nil, Validation("display_name exceeds 200 bytes")
	}
	idem.Request = req
	return s.serverMutation(ctx, p, idem, func(tx pgx.Tx, now time.Time) (*Result, error) {
		a := ActorView{ID: ids.New("act"), Kind: req.Kind, Handle: req.Handle, DisplayName: req.DisplayName, OwnerActorID: req.OwnerActorID, CreatedAt: now}
		if _, err := tx.Exec(ctx, `INSERT INTO actors(id, kind, handle, display_name, owner_actor_id, created_at) VALUES ($1,$2,$3,$4,$5,$6)`,
			a.ID, a.Kind, a.Handle, a.DisplayName, a.OwnerActorID, now); err != nil {
			return nil, err
		}
		if err := writeAudit(ctx, tx, now, nil, p, "actor.create", "actor", a.ID, "", map[string]any{"handle": a.Handle}); err != nil {
			return nil, err
		}
		return &Result{Status: 201, ResourceType: "actor", ResourceID: a.ID, Body: a}, nil
	})
}

func getActor(ctx context.Context, q querier, id string) (*ActorView, error) {
	var a ActorView
	err := q.QueryRow(ctx, `SELECT id, kind, handle, display_name, owner_actor_id, server_admin, created_at FROM actors WHERE id=$1`, id).
		Scan(&a.ID, &a.Kind, &a.Handle, &a.DisplayName, &a.OwnerActorID, &a.ServerAdmin, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, NotFound("actor")
	}
	return &a, err
}

// GetActor: server admins see any actor; others see only themselves (board
// participant discovery is board-scoped).
func (s *Service) GetActor(ctx context.Context, p *Principal, id string) (*ActorView, error) {
	if id != p.ActorID && !p.ServerAdmin {
		return nil, NotFound("actor")
	}
	return getActor(ctx, s.DB, id)
}

// ListAllActors returns one page of all actors in (created_at, id) order
// (server administrators only).
func (s *Service) ListAllActors(ctx context.Context, p *Principal, pg PageRequest) ([]ActorView, *string, error) {
	after, err := pg.after()
	if err != nil {
		return nil, nil, err
	}
	has, t, id := after.sqlArgs()
	if err := requireServerAdmin(p); err != nil {
		return nil, nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT id, kind, handle, display_name, owner_actor_id, server_admin, created_at FROM actors
		WHERE NOT $1 OR (created_at, id) > ($2, $3) ORDER BY created_at, id LIMIT $4`, has, t, id, pg.Limit+1)
	if err != nil {
		return nil, nil, err
	}
	l, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ActorView, error) {
		var a ActorView
		err := r.Scan(&a.ID, &a.Kind, &a.Handle, &a.DisplayName, &a.OwnerActorID, &a.ServerAdmin, &a.CreatedAt)
		return a, err
	})
	if err != nil {
		return nil, nil, err
	}
	l, next := trimPage(l, pg.Limit, func(a ActorView) keyRef { return keyRef{a.CreatedAt, a.ID} })
	return l, next, nil
}

// MeView answers GET /v1/me.
type MeView struct {
	Actor      ActorView      `json:"actor"`
	Credential CredentialView `json:"credential"`
}

func (s *Service) Me(ctx context.Context, p *Principal) (*MeView, error) {
	a, err := getActor(ctx, s.DB, p.ActorID)
	if err != nil {
		return nil, err
	}
	c, err := getCredential(ctx, s.DB, p.CredentialID)
	if err != nil {
		return nil, err
	}
	return &MeView{Actor: *a, Credential: *c}, nil
}

func getCredential(ctx context.Context, q querier, id string) (*CredentialView, error) {
	var c CredentialView
	err := q.QueryRow(ctx, `SELECT id, actor_id, label, scopes, board_ids, created_at, expires_at, revoked_at FROM credentials WHERE id=$1`, id).
		Scan(&c.ID, &c.ActorID, &c.Label, &c.Scopes, &c.BoardIDs, &c.CreatedAt, &c.ExpiresAt, &c.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, NotFound("credential")
	}
	return &c, err
}

// GetCredential is used for idempotent replay projections.
func (s *Service) GetCredential(ctx context.Context, p *Principal, id string) (*CredentialView, error) {
	c, err := getCredential(ctx, s.DB, id)
	if err != nil {
		return nil, err
	}
	if c.ActorID != p.ActorID && !p.ServerAdmin {
		return nil, NotFound("credential")
	}
	return c, nil
}

// CreateCredentialRequest is the normalized body of POST /v1/credentials.
type CreateCredentialRequest struct {
	ActorID          string   `json:"actor_id"`
	Label            string   `json:"label"`
	Scopes           []string `json:"scopes"`
	BoardIDs         []string `json:"board_ids"`
	ExpiresInSeconds int64    `json:"expires_in_seconds"`
}

// CreateCredential issues a credential. An actor may issue credentials for
// itself whose scopes, boards and expiry are within the calling credential's
// ceiling. A server administrator may issue for any actor.
func (s *Service) CreateCredential(ctx context.Context, p *Principal, idem *Idem, req CreateCredentialRequest) (*Result, error) {
	if req.ActorID == "" {
		req.ActorID = p.ActorID
	}
	if req.ActorID != p.ActorID && !p.ServerAdmin {
		return nil, Forbidden("only server administrators may issue credentials for other actors")
	}
	if len(req.Scopes) == 0 {
		return nil, Validation("scopes must list at least one scope")
	}
	for _, sc := range req.Scopes {
		if !authz.ValidScope(sc) {
			return nil, Validation("unknown scope %q", sc)
		}
	}
	req.Scopes = sortedUnique(req.Scopes)
	if req.BoardIDs != nil {
		req.BoardIDs = sortedUnique(req.BoardIDs)
	}
	if req.ExpiresInSeconds == 0 {
		req.ExpiresInSeconds = int64(s.Cfg.CredentialDefaultTTL / time.Second)
	}
	if req.ExpiresInSeconds < 60 || time.Duration(req.ExpiresInSeconds)*time.Second > s.Cfg.CredentialMaxTTL {
		return nil, Validation("expires_in_seconds must be between 60 and %d", int64(s.Cfg.CredentialMaxTTL/time.Second))
	}
	if req.ActorID == p.ActorID {
		// Ceiling: a credential never mints broader authority than itself,
		// including a server administrator's own credentials (AUTH-005).
		if !slices.Contains(p.Scopes, authz.AllScopes) {
			for _, sc := range req.Scopes {
				if !slices.Contains(p.Scopes, sc) {
					return nil, Forbidden("scope %q exceeds the calling credential's ceiling", sc)
				}
			}
		}
		if p.BoardIDs != nil {
			if req.BoardIDs == nil {
				return nil, Forbidden("calling credential is board-restricted; board_ids is required")
			}
			for _, b := range req.BoardIDs {
				if !slices.Contains(p.BoardIDs, b) {
					return nil, Forbidden("board %q exceeds the calling credential's ceiling", b)
				}
			}
		}
	}
	idem.Request = req
	return s.serverMutation(ctx, p, idem, func(tx pgx.Tx, now time.Time) (*Result, error) {
		if _, err := getActor(ctx, tx, req.ActorID); err != nil {
			return nil, err
		}
		exp := now.Add(time.Duration(req.ExpiresInSeconds) * time.Second)
		// A self-issued credential never outlives its issuer. Credentials a
		// server administrator issues to other actors are independent grants
		// of authority and are not tied to the administrator's token.
		if req.ActorID == p.ActorID && exp.After(p.ExpiresAt) {
			exp = p.ExpiresAt
		}
		issuer := p.CredentialID
		ic, err := insertCredential(ctx, tx, now, req.ActorID, &issuer, req.Label, req.Scopes, req.BoardIDs, exp)
		if err != nil {
			return nil, err
		}
		if err := writeAudit(ctx, tx, now, nil, p, "credential.issue", "credential", ic.Credential.ID, "",
			map[string]any{"actor_id": req.ActorID, "scopes": req.Scopes}); err != nil {
			return nil, err
		}
		return &Result{Status: 201, ResourceType: "credential", ResourceID: ic.Credential.ID, Body: ic}, nil
	})
}

// ListCredentials returns one page of an actor's credentials in
// (created_at, id) order.
func (s *Service) ListCredentials(ctx context.Context, p *Principal, actorID string, pg PageRequest) ([]CredentialView, *string, error) {
	after, err := pg.after()
	if err != nil {
		return nil, nil, err
	}
	has, t, id := after.sqlArgs()
	if actorID == "" {
		actorID = p.ActorID
	}
	if actorID != p.ActorID && !p.ServerAdmin {
		return nil, nil, Forbidden("server administration required")
	}
	rows, err := s.DB.Query(ctx, `SELECT id, actor_id, label, scopes, board_ids, created_at, expires_at, revoked_at FROM credentials
		WHERE actor_id=$1 AND (NOT $2 OR (created_at, id) > ($3, $4)) ORDER BY created_at, id LIMIT $5`, actorID, has, t, id, pg.Limit+1)
	if err != nil {
		return nil, nil, err
	}
	l, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (CredentialView, error) {
		var c CredentialView
		err := r.Scan(&c.ID, &c.ActorID, &c.Label, &c.Scopes, &c.BoardIDs, &c.CreatedAt, &c.ExpiresAt, &c.RevokedAt)
		return c, err
	})
	if err != nil {
		return nil, nil, err
	}
	l, next := trimPage(l, pg.Limit, func(c CredentialView) keyRef { return keyRef{c.CreatedAt, c.ID} })
	return l, next, nil
}

// RevokeCredential revokes the caller's own credential or, for a server
// administrator, any credential. Revocation is effective for every
// authorization check after commit. Repeating it is idempotent.
func (s *Service) RevokeCredential(ctx context.Context, p *Principal, id string) (*CredentialView, error) {
	var out *CredentialView
	_, err := s.serverMutation(ctx, p, nil, func(tx pgx.Tx, now time.Time) (*Result, error) {
		c, err := getCredential(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if c.ActorID != p.ActorID && !p.ServerAdmin {
			return nil, NotFound("credential")
		}
		if c.RevokedAt == nil {
			if _, err := tx.Exec(ctx, `UPDATE credentials SET revoked_at=$2 WHERE id=$1`, id, now); err != nil {
				return nil, err
			}
			c.RevokedAt = &now
			if err := writeAudit(ctx, tx, now, nil, p, "credential.revoke", "credential", id, "", nil); err != nil {
				return nil, err
			}
		}
		out = c
		return &Result{}, nil
	})
	return out, err
}

func sortedUnique(in []string) []string {
	out := slices.Clone(in)
	sort.Strings(out)
	return slices.Compact(out)
}
