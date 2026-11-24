package domain

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/dustywusty/yak/internal/authz"
	"github.com/dustywusty/yak/internal/ids"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// InboxItem is a durable reference, never a copy of protected source content.
type InboxItem struct {
	ID         string    `json:"id"`
	BoardID    string    `json:"board_id"`
	ActorID    string    `json:"actor_id"`
	SourceKind string    `json:"source_kind"`
	SourceID   string    `json:"source_id"`
	MessageID  string    `json:"message_id"`
	ThreadID   string    `json:"thread_id"`
	CreatedAt  time.Time `json:"created_at"`
}

type InboxPage struct {
	Items       []InboxItem `json:"items"`
	NextCursor  string      `json:"next_cursor"`
	AckCursor   string      `json:"ack_cursor"`
	CaughtUp    bool        `json:"caught_up"`
	TimedOut    bool        `json:"timed_out"`
	UnreadCount int64       `json:"unread_count"`
}

type ReadInboxRequest struct {
	Cursor string
	Limit  int
	Wait   time.Duration
	Token  string // Re-authenticated on every wait iteration, including timeout.
}

type InboxAckRequest struct {
	Cursor string `json:"cursor"`
}
type InboxAck struct {
	BoardID        string    `json:"board_id"`
	ActorID        string    `json:"actor_id"`
	AcknowledgedAt time.Time `json:"acknowledged_at"`
}

// enqueueInbox is the shared producer interface for mentions and future P3
// sources. The source message and the inbox obligation commit in this btx.
func (b *btx) enqueueInbox(m *MessageView, kind, sourceID string, recipients []string) error {
	recipients = sortedUnique(recipients)
	for _, actorID := range recipients {
		other := &Principal{ActorID: actorID, Scopes: []string{authz.AllScopes}}
		pol, _, err := b.s.loadPolicy(b.ctx, b, other, &b.board)
		if err != nil {
			return err
		}
		if !pol.Has(m.ChannelPath, authz.Read) {
			continue
		}
		var enabled bool
		if err := b.QueryRow(b.ctx, `SELECT EXISTS(SELECT 1 FROM actors WHERE id=$1 AND disabled_at IS NULL)`, actorID).Scan(&enabled); err != nil {
			return err
		}
		if !enabled {
			continue
		}
		if _, err := b.Exec(b.ctx, `INSERT INTO actor_inboxes(board_id,actor_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, b.board.ID, actorID); err != nil {
			return err
		}
		var head int64
		if err := b.QueryRow(b.ctx, `SELECT head_position FROM actor_inboxes WHERE board_id=$1 AND actor_id=$2 FOR UPDATE`, b.board.ID, actorID).Scan(&head); err != nil {
			return err
		}
		tag, err := b.Exec(b.ctx, `INSERT INTO inbox_items(id,board_id,actor_id,position,source_kind,source_id,message_id,thread_id,created_at)
   VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(board_id,actor_id,source_kind,source_id) DO NOTHING`,
			ids.New("inb"), b.board.ID, actorID, head+1, kind, sourceID, m.ID, m.ThreadID, b.now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		if _, err := b.Exec(b.ctx, `UPDATE actor_inboxes SET head_position=$3 WHERE board_id=$1 AND actor_id=$2`, b.board.ID, actorID, head+1); err != nil {
			return err
		}
		if _, err := b.Exec(b.ctx, `SELECT pg_notify('yak_events',$1)`, b.board.ID); err != nil {
			return err
		}
	}
	return nil
}

func inboxReadScope(p *Principal) error {
	if !slices.Contains(p.Scopes, authz.Read) && !slices.Contains(p.Scopes, authz.AllScopes) {
		return Forbidden("inbox requires read scope")
	}
	return nil
}

// inboxCursor checks an issued receipt; opaque guesses cannot acknowledge
// an arbitrary prefix. Credential ceilings are part of the policy fingerprint.
func inboxCursor(ctx context.Context, q querier, p *Principal, boardID, token, fp, epoch string) (int64, error) {
	if len(token) > 256 || !strings.HasPrefix(token, "i1.") {
		return 0, Validation("malformed inbox cursor")
	}
	var board, actor, policy, history string
	var pos int64
	err := q.QueryRow(ctx, `SELECT board_id,actor_id,position,policy_fingerprint,history_epoch FROM inbox_cursors WHERE token=$1`, token).Scan(&board, &actor, &pos, &policy, &history)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, Validation("inbox cursor was not issued by this server")
	}
	if err != nil {
		return 0, err
	}
	if board != boardID || actor != p.ActorID {
		return 0, Validation("inbox cursor was issued for another board or actor")
	}
	if history != epoch {
		return 0, resync("history_reset", "inbox history changed; read without a cursor from the saved acknowledgment")
	}
	if policy != fp {
		return 0, resync("policy_changed", "inbox access changed; read without a cursor from the saved acknowledgment")
	}
	return pos, nil
}

func (s *Service) ReadInbox(ctx context.Context, p *Principal, boardID string, req ReadInboxRequest) (*InboxPage, error) {
	if req.Limit == 0 {
		req.Limit = s.Cfg.DefaultPageSize
	}
	if req.Limit < 1 || req.Limit > s.Cfg.MaxPageSize {
		return nil, Validation("limit must be 1..%d", s.Cfg.MaxPageSize)
	}
	if req.Wait < 0 || req.Wait > s.Cfg.MaxWait {
		return nil, Validation("wait exceeds inbox wait limit")
	}
	notify, unsubscribe := s.Hub.Subscribe(boardID)
	defer unsubscribe()
	deadline := time.Now().Add(req.Wait)
	firstFP, firstEpoch := "", ""
	for {
		if req.Token != "" {
			var err error
			p, err = s.recheck(ctx, req.Token)
			if err != nil {
				return nil, err
			}
		}
		page, fp, epoch, err := s.readInboxOnce(ctx, p, boardID, req)
		if err != nil {
			return nil, err
		}
		if firstFP != "" && firstEpoch != epoch {
			return nil, resync("history_reset", "inbox history changed during wait; read without a cursor")
		}
		if firstFP != "" && firstFP != fp {
			return nil, resync("policy_changed", "inbox policy changed during wait; read without a cursor")
		}
		firstFP, firstEpoch = fp, epoch
		if len(page.Items) > 0 || !page.CaughtUp || req.Wait == 0 {
			return page, nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			page.TimedOut = true
			return page, nil
		}
		// Progress past hidden items without advancing the durable acknowledgment.
		req.Cursor = page.NextCursor
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-notify:
		case <-time.After(min(remaining, 2*time.Second)):
		}
	}
}

// A concurrent reader can issue the same boundary after our repeatable-read
// snapshot. PostgreSQL then returns 40001. Retry the entire snapshot, never
// just the receipt write, so authorization, counts and projection stay aligned.
func (s *Service) readInboxOnce(ctx context.Context, p *Principal, boardID string, req ReadInboxRequest) (*InboxPage, string, string, error) {
	for attempt := 0; ; attempt++ {
		page, fp, epoch, err := s.readInboxSnapshot(ctx, p, boardID, req)
		var pgerr *pgconn.PgError
		if err == nil || !errors.As(err, &pgerr) || pgerr.Code != "40001" || attempt >= 3 {
			return page, fp, epoch, err
		}
		if err := ctx.Err(); err != nil {
			return nil, "", "", err
		}
	}
}

func (s *Service) readInboxSnapshot(ctx context.Context, p *Principal, boardID string, req ReadInboxRequest) (page *InboxPage, fp, epoch string, err error) {
	err = pgx.BeginTxFunc(ctx, s.DB, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		if err := inboxReadScope(p); err != nil {
			return err
		}
		v, e := s.viewBoard(ctx, tx, p, boardID)
		if e != nil {
			return e
		}
		fp = v.policy.Fingerprint()
		epoch, e = s.historyEpoch(ctx, tx)
		if e != nil {
			return e
		}
		var head, ack int64
		e = tx.QueryRow(ctx, `SELECT head_position,ack_position FROM actor_inboxes WHERE board_id=$1 AND actor_id=$2`, boardID, p.ActorID).Scan(&head, &ack)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		pos := ack
		if req.Cursor != "" {
			n, e := inboxCursor(ctx, tx, p, boardID, req.Cursor, fp, epoch)
			if e != nil {
				return e
			}
			if n > head {
				return resync("history_reset", "inbox cursor is ahead of history")
			}
			pos = max(pos, n)
		}
		start := pos
		page = &InboxPage{Items: []InboxItem{}}
		rows, e := tx.Query(ctx, `SELECT i.position,i.id,i.source_kind,i.source_id,i.message_id,i.thread_id,i.created_at,t.channel_id
   FROM inbox_items i JOIN messages m ON m.id=i.message_id AND m.board_id=i.board_id AND m.thread_id=i.thread_id
   JOIN threads t ON t.id=i.thread_id AND t.board_id=i.board_id
   WHERE i.board_id=$1 AND i.actor_id=$2 AND i.position>$3 AND i.position<=$4 AND m.deleted_at IS NULL ORDER BY i.position`, boardID, p.ActorID, ack, head)
		if e != nil {
			return e
		}
		for rows.Next() {
			var n int64
			var ch string
			it := InboxItem{BoardID: boardID, ActorID: p.ActorID}
			if e := rows.Scan(&n, &it.ID, &it.SourceKind, &it.SourceID, &it.MessageID, &it.ThreadID, &it.CreatedAt, &ch); e != nil {
				rows.Close()
				return e
			}
			_, visible := v.canRead(ch)
			if visible {
				page.UnreadCount++
			}
			if n <= start || len(page.Items) >= req.Limit {
				continue
			}
			pos = n
			if visible {
				page.Items = append(page.Items, it)
			}
		}
		if e := rows.Err(); e != nil {
			rows.Close()
			return e
		}
		rows.Close()
		if len(page.Items) < req.Limit {
			pos = head
		}
		page.CaughtUp = pos >= head
		// One durable random token per examined boundary/policy avoids creating
		// another receipt on every polling tick while preserving issued-only ack.
		var token string
		e = tx.QueryRow(ctx, `SELECT token FROM inbox_cursors WHERE board_id=$1 AND actor_id=$2 AND position=$3 AND history_epoch=$4 AND policy_fingerprint=$5`, boardID, p.ActorID, pos, epoch, fp).Scan(&token)
		if errors.Is(e, pgx.ErrNoRows) {
			token = "i1." + ids.New("cur")
			e = tx.QueryRow(ctx, `INSERT INTO inbox_cursors(token,board_id,actor_id,position,history_epoch,policy_fingerprint)
          VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(board_id,actor_id,position,history_epoch,policy_fingerprint)
          DO UPDATE SET token=inbox_cursors.token RETURNING token`, token, boardID, p.ActorID, pos, epoch, fp).Scan(&token)
		}
		if e != nil {
			return e
		}
		page.NextCursor, page.AckCursor = token, token
		return nil
	})
	return
}

func (s *Service) AckInbox(ctx context.Context, p *Principal, boardID string, idem *Idem, req InboxAckRequest) (*InboxAck, bool, error) {
	if req.Cursor == "" {
		return nil, false, Validation("cursor is required")
	}
	if idem != nil {
		idem.Request = req
	}
	res, err := s.boardMutation(ctx, p, boardID, idem, func(b *btx) (*Result, error) {
		if err := inboxReadScope(p); err != nil {
			return nil, err
		}
		epoch, err := s.historyEpoch(ctx, b)
		if err != nil {
			return nil, err
		}
		pos, err := inboxCursor(ctx, b, p, boardID, req.Cursor, b.policy.Fingerprint(), epoch)
		if err != nil {
			return nil, err
		}
		if _, err := b.Exec(ctx, `INSERT INTO actor_inboxes(board_id,actor_id,acknowledged_at) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, boardID, p.ActorID, b.now); err != nil {
			return nil, err
		}
		var head int64
		if err := b.QueryRow(ctx, `SELECT head_position FROM actor_inboxes WHERE board_id=$1 AND actor_id=$2 FOR UPDATE`, boardID, p.ActorID).Scan(&head); err != nil {
			return nil, err
		}
		if pos > head {
			return nil, resync("history_reset", "inbox cursor is ahead of history")
		}
		if _, err := b.Exec(ctx, `UPDATE actor_inboxes SET ack_position=$3,acknowledged_at=$4 WHERE board_id=$1 AND actor_id=$2 AND ack_position<$3`, boardID, p.ActorID, pos, b.now); err != nil {
			return nil, err
		}
		return &Result{Status: 200, ResourceType: "inbox_ack", ResourceID: p.ActorID}, nil
	})
	if err != nil {
		return nil, false, err
	}
	// Receipts store identities, not response bytes. Replays recheck the cursor
	// policy and regenerate the current authorized acknowledgment projection.
	var out InboxAck
	err = pgx.BeginTxFunc(ctx, s.DB, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if err := inboxReadScope(p); err != nil {
			return err
		}
		v, err := s.viewBoard(ctx, tx, p, boardID)
		if err != nil {
			return err
		}
		epoch, err := s.historyEpoch(ctx, tx)
		if err != nil {
			return err
		}
		if _, err := inboxCursor(ctx, tx, p, boardID, req.Cursor, v.policy.Fingerprint(), epoch); err != nil {
			return err
		}
		out.BoardID, out.ActorID = boardID, p.ActorID
		return tx.QueryRow(ctx, `SELECT acknowledged_at FROM actor_inboxes WHERE board_id=$1 AND actor_id=$2`, boardID, p.ActorID).Scan(&out.AcknowledgedAt)
	})
	return &out, res.Replayed, err
}
