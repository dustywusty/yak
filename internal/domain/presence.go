package domain

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/dustywusty/yak/internal/authz"
	"github.com/dustywusty/yak/internal/events"
	"github.com/jackc/pgx/v5"
)

const PresenceTTLDefault = 120
const PresenceTTLMin = 15
const PresenceTTLMax = 300

type ActorPresence struct {
	BoardID    string     `json:"board_id"`
	ActorID    string     `json:"actor_id"`
	State      string     `json:"state"`
	LastSeenAt *time.Time `json:"last_seen_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	Revision   int64      `json:"revision"`
}
type PresenceHeartbeatRequest struct {
	State      string `json:"state"`
	TTLSeconds *int   `json:"ttl_seconds,omitempty"`
}

func presenceReadable(pol *authz.Policy, chans map[string]channelRow, post bool) bool {
	for _, c := range chans {
		if pol.Has(c.Path, authz.Read) && (!post || pol.Has(c.Path, authz.Post)) {
			return true
		}
	}
	return false
}

// presenceAudience resolves board membership inside the caller's current
// transaction. Board metadata access alone does not confer presence access.
func presenceAudience(ctx context.Context, q querier, p *Principal, pol *authz.Policy, chans map[string]channelRow, board string) (func(string) bool, error) {
	if !presenceReadable(pol, chans, false) {
		return func(string) bool { return false }, nil
	}
	rows, err := q.Query(ctx, `SELECT subject_type, subject_actor_id FROM grants WHERE board_id=$1 AND revoked_at IS NULL AND effect='allow'`, board)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := map[string]bool{p.ActorID: true}
	all := false
	for rows.Next() {
		var typ string
		var actor *string
		if err := rows.Scan(&typ, &actor); err != nil {
			return nil, err
		}
		if typ == "any_actor" {
			all = true
		}
		if actor != nil {
			members[*actor] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return func(actor string) bool { return all || members[actor] }, nil
}

func presenceEventMatches(f events.Filter, typ string) bool {
	return f.Channel == "" && f.Subtree == "" && f.ThreadID == "" && f.AuthorID == "" && f.Mention == "" && f.Text == "" && len(f.Labels) == 0 && (len(f.Types) == 0 || slices.Contains(f.Types, typ))
}

func (b *btx) loadPresence(actor string) (*ActorPresence, error) {
	out := &ActorPresence{BoardID: b.board.ID, ActorID: actor, State: "offline"}
	err := b.QueryRow(b.ctx, `SELECT state,revision,last_seen_at,expires_at FROM actor_presence WHERE board_id=$1 AND actor_id=$2`, b.board.ID, actor).Scan(&out.State, &out.Revision, &out.LastSeenAt, &out.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	return out, err
}
func (b *btx) emitPresence(out *ActorPresence, expiry bool) error {
	original := b.p
	at := b.now
	if expiry {
		b.p = &Principal{ActorID: out.ActorID, System: true}
		at = *out.ExpiresAt
	}
	defer func() { b.p = original }()
	_, err := b.emit(PendingEvent{Type: "actor.presence_changed", ResourceType: "actor_presence", ResourceID: out.ActorID, Revision: out.Revision, Payload: map[string]any{"presence": out}, EffectiveAt: at})
	return err
}
func (b *btx) expirePresence(out *ActorPresence) (bool, error) {
	if out.State == "offline" || out.ExpiresAt == nil || b.now.Before(*out.ExpiresAt) {
		return false, nil
	}
	out.State = "offline"
	out.Revision++
	if _, err := b.Exec(b.ctx, `UPDATE actor_presence SET state='offline',revision=$3 WHERE board_id=$1 AND actor_id=$2`, b.board.ID, out.ActorID, out.Revision); err != nil {
		return false, err
	}
	return true, b.emitPresence(out, true)
}

// GetPresence immediately records a due expiry under the board lock. It never
// depends on the housekeeping worker and never grants access to channel data.
func (s *Service) GetPresence(ctx context.Context, p *Principal, board, actor string) (*ActorPresence, error) {
	res, err := s.boardMutation(ctx, p, board, nil, func(b *btx) (*Result, error) {
		audience, err := presenceAudience(ctx, b, p, b.policy, b.chans, board)
		if err != nil {
			return nil, err
		}
		if !audience(actor) {
			return nil, NotFound("presence")
		}
		if _, err := getActor(ctx, b, actor); err != nil {
			return nil, err
		}
		out, err := b.loadPresence(actor)
		if err != nil {
			return nil, err
		}
		if _, err := b.expirePresence(out); err != nil {
			return nil, err
		}
		return &Result{Status: 200, Body: out}, nil
	})
	if err != nil {
		return nil, err
	}
	return res.Body.(*ActorPresence), nil
}

func (s *Service) HeartbeatPresence(ctx context.Context, p *Principal, board string, idem *Idem, req PresenceHeartbeatRequest) (*ActorPresence, bool, error) {
	switch req.State {
	case "online", "away":
		if req.TTLSeconds == nil {
			n := PresenceTTLDefault
			req.TTLSeconds = &n
		}
		if *req.TTLSeconds < PresenceTTLMin || *req.TTLSeconds > PresenceTTLMax {
			return nil, false, Validation("ttl_seconds must be %d..%d", PresenceTTLMin, PresenceTTLMax)
		}
	case "offline":
		if req.TTLSeconds != nil {
			return nil, false, Validation("offline presence must not carry ttl_seconds")
		}
	default:
		return nil, false, Validation("state must be online, away or offline")
	}
	if idem == nil || idem.Key == "" {
		return nil, false, Validation("Idempotency-Key is required")
	}
	idem.Request = req
	res, err := s.boardMutation(ctx, p, board, idem, func(b *btx) (*Result, error) {
		if !presenceReadable(b.policy, b.chans, false) {
			return nil, NotFound("presence")
		}
		if !presenceReadable(b.policy, b.chans, true) {
			return nil, Forbidden("read and post required on a board channel")
		}
		out, err := b.loadPresence(p.ActorID)
		if err != nil {
			return nil, err
		}
		if _, err := b.expirePresence(out); err != nil {
			return nil, err
		}
		out.State = req.State
		out.Revision++
		out.LastSeenAt = &b.now
		out.ExpiresAt = nil
		if req.TTLSeconds != nil {
			at := b.now.Add(time.Duration(*req.TTLSeconds) * time.Second)
			out.ExpiresAt = &at
		}
		if _, err := b.Exec(ctx, `INSERT INTO actor_presence(board_id,actor_id,state,revision,last_seen_at,expires_at) VALUES($1,$2,$3,$4,$5,$6)
   ON CONFLICT(board_id,actor_id) DO UPDATE SET state=EXCLUDED.state,revision=EXCLUDED.revision,last_seen_at=EXCLUDED.last_seen_at,expires_at=EXCLUDED.expires_at`, board, p.ActorID, out.State, out.Revision, out.LastSeenAt, out.ExpiresAt); err != nil {
			return nil, err
		}
		if err := b.emitPresence(out, false); err != nil {
			return nil, err
		}
		return &Result{Status: 200, ResourceType: "actor_presence", ResourceID: p.ActorID, Body: out}, nil
	})
	if err != nil {
		return nil, false, err
	}
	if res.Replayed {
		out, err := s.GetPresence(ctx, p, board, p.ActorID)
		return out, true, err
	}
	return res.Body.(*ActorPresence), false, nil
}

// SweepPresenceExpiry uses the same board lock and transition as reads and
// heartbeats. A racing heartbeat is never overwritten by a stale worker.
func (s *Service) SweepPresenceExpiry(ctx context.Context, limit int) (int, error) {
	if limit < 1 {
		return 0, Validation("expiry limit must be positive")
	}
	rows, err := s.DB.Query(ctx, `SELECT DISTINCT board_id FROM actor_presence WHERE state<>'offline' AND expires_at <= $1`, s.Now())
	if err != nil {
		return 0, err
	}
	boards, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	total := 0
	for _, board := range boards {
		_, err := s.boardMutation(ctx, &Principal{System: true}, board, nil, func(b *btx) (*Result, error) {
			rows, err := b.Query(ctx, `SELECT actor_id FROM actor_presence WHERE board_id=$1 AND state<>'offline' AND expires_at <= $2 ORDER BY expires_at,actor_id LIMIT $3`, board, b.now, limit)
			if err != nil {
				return nil, err
			}
			actors, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return nil, err
			}
			for _, actor := range actors {
				out, err := b.loadPresence(actor)
				if err != nil {
					return nil, err
				}
				changed, err := b.expirePresence(out)
				if err != nil {
					return nil, err
				}
				if changed {
					total++
				}
			}
			return &Result{Status: 200}, nil
		})
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
