package domain

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abby-protocol/abby/internal/authz"
	"github.com/abby-protocol/abby/internal/events"
	"github.com/abby-protocol/abby/internal/ids"
)

func encodePageToken(n int64) string {
	return "p1." + base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(n, 10)))
}

// DecodePageToken parses an opaque page token; "" means the first page.
func DecodePageToken(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	raw, ok := cutPrefix(s, "p1.")
	if !ok {
		return 0, Validation("malformed page_token")
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return 0, Validation("malformed page_token")
	}
	n, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil || n < 0 {
		return 0, Validation("malformed page_token")
	}
	return n, nil
}

func cutPrefix(s, p string) (string, bool) {
	if len(s) >= len(p) && s[:len(p)] == p {
		return s[len(p):], true
	}
	return "", false
}

type ResourceRef struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	Revision *int64 `json:"revision"`
}

type EventView struct {
	ID           string          `json:"id"`
	Cursor       string          `json:"cursor"`
	Type         string          `json:"type"`
	BoardID      string          `json:"board_id"`
	ActorID      string          `json:"actor_id"`
	CredentialID *string         `json:"credential_id"`
	RunID        *string         `json:"run_id"`
	Resource     ResourceRef     `json:"resource"`
	ChannelID    *string         `json:"channel_id"`
	ThreadID     *string         `json:"thread_id"`
	RecordedAt   time.Time       `json:"recorded_at"`
	EffectiveAt  time.Time       `json:"effective_at"`
	Payload      json.RawMessage `json:"payload"`
}

type EventPage struct {
	Events     []EventView `json:"events"`
	NextCursor string      `json:"next_cursor"`
	// CaughtUp is true when the page examined every committed event up to
	// the board head at read time.
	CaughtUp bool `json:"caught_up"`
	// TimedOut is true when a long poll ended without a matching event. It
	// is a successful outcome; continue from NextCursor.
	TimedOut bool `json:"timed_out"`
}

type ReadEventsRequest struct {
	Filter events.Filter
	Cursor string // opaque; empty means use From
	From   string // "earliest" (default) | "latest"
	Limit  int
	Wait   time.Duration
	Token  string // bearer token, re-authenticated on every wait iteration
}

// recheck re-authenticates the credential (revocation during waits, §4.2).
func (s *Service) recheck(ctx context.Context, token string) (*Principal, error) {
	return s.Authenticate(ctx, token)
}

func resync(reason, detail string) *Error {
	e := E(409, "resync_required", "%s", detail)
	e.Extra = map[string]any{"reason": reason}
	return e
}

// ReadEvents implements paginated catch-up and bounded long polling over one
// board's committed event order (ADR-006).
func (s *Service) ReadEvents(ctx context.Context, p *Principal, boardID string, req ReadEventsRequest) (*EventPage, error) {
	deadline := time.Now().Add(req.Wait)
	notify, unsubscribe := s.Hub.Subscribe(boardID)
	defer unsubscribe()
	start := int64(-1)
	firstFP, firstEpoch := "", ""
	for iter := 0; ; iter++ {
		if iter > 0 {
			var err error
			if p, err = s.recheck(ctx, req.Token); err != nil {
				return nil, err
			}
		}
		page, pos, fp, epoch, err := s.readEventsOnce(ctx, p, boardID, req, start)
		if err != nil {
			return nil, err
		}
		if firstFP == "" {
			firstFP, firstEpoch = fp, epoch
		} else if epoch != firstEpoch {
			return nil, resync("history_reset", "board history was reset or restored during the wait; take a snapshot")
		} else if fp != firstFP {
			// Access changed during the wait: explicit recovery, never a
			// silent change of what this read position means (§4.2).
			return nil, resync("policy_changed", "effective access changed during the wait; take a snapshot and continue from its cursor")
		}
		if len(page.Events) > 0 || !page.CaughtUp || req.Wait <= 0 {
			return page, nil
		}
		// Caught up with no match: continue from the advanced position.
		start = pos
		remaining := time.Until(deadline)
		if remaining <= 0 {
			page.TimedOut = true
			return page, nil
		}
		poll := min(remaining, 2*time.Second)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-notify:
		case <-time.After(poll):
		}
	}
}

// readEventsOnce evaluates policy, epoch, head, retention, and the event scan
// in one REPEATABLE READ snapshot, so a channel, grant, or retention change
// committed mid-read cannot make the reader skip or misjudge events.
func (s *Service) readEventsOnce(ctx context.Context, p *Principal, boardID string, req ReadEventsRequest, startOverride int64) (page *EventPage, pos int64, fp, epoch string, err error) {
	err = pgx.BeginTxFunc(ctx, s.DB, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var e error
		page, pos, fp, epoch, e = s.readEventsTx(ctx, tx, p, boardID, req, startOverride)
		return e
	})
	return
}

func (s *Service) readEventsTx(ctx context.Context, q pgx.Tx, p *Principal, boardID string, req ReadEventsRequest, startOverride int64) (*EventPage, int64, string, string, error) {
	v, err := s.viewBoard(ctx, q, p, boardID)
	if err != nil {
		return nil, 0, "", "", err
	}
	epoch, err := s.historyEpoch(ctx, q)
	if err != nil {
		return nil, 0, "", "", err
	}
	var head, retained int64
	if err := q.QueryRow(ctx, `SELECT head_position, retained_from_position FROM board_heads WHERE board_id=$1`, boardID).Scan(&head, &retained); err != nil {
		return nil, 0, "", "", err
	}
	qh, fp := req.Filter.Hash(), v.policy.Fingerprint()
	var pos int64
	switch {
	case startOverride >= 0:
		if startOverride < retained-1 {
			return nil, 0, "", "", E(410, "cursor_expired", "events after this read position were removed by retention; take a snapshot")
		}
		pos = startOverride
	case req.Cursor != "":
		c, err := events.DecodeCursor(req.Cursor)
		if err != nil {
			return nil, 0, "", "", Validation("malformed cursor")
		}
		if c.BoardID != boardID || c.Query != qh {
			return nil, 0, "", "", Validation("cursor was issued for a different board or query; obtain a new read position")
		}
		if c.Epoch != epoch {
			return nil, 0, "", "", resync("history_reset", "board history was reset or restored; take a snapshot and continue from its cursor")
		}
		if c.Policy != fp {
			return nil, 0, "", "", resync("policy_changed", "effective access changed; take a snapshot and continue from its cursor")
		}
		if c.Position > head {
			return nil, 0, "", "", resync("history_reset", "cursor is ahead of board history")
		}
		if c.Position < retained-1 {
			e := E(410, "cursor_expired", "events after this cursor are no longer retained; take a snapshot")
			e.Extra = map[string]any{"retained_from_position_available": true}
			return nil, 0, "", "", e
		}
		pos = c.Position
	case req.From == "latest":
		pos = head
	default:
		pos = retained - 1
	}
	mk := func(position int64) string {
		return events.Cursor{BoardID: boardID, Epoch: epoch, Position: position, Query: qh, Policy: fp}.Encode()
	}
	page := &EventPage{Events: []EventView{}}
	for scans := 0; scans < 20; scans++ {
		rows, err := q.Query(ctx, `SELECT position, id, type, actor_id, credential_id, run_id, channel_id, thread_id, resource_type, resource_id,
			resource_revision, payload, facets, recorded_at, effective_at FROM events WHERE board_id=$1 AND position > $2 AND position <= $3
			ORDER BY position LIMIT $4`, boardID, pos, head, s.Cfg.EventScanBatch)
		if err != nil {
			return nil, 0, "", "", err
		}
		n := 0
		for rows.Next() {
			n++
			var ev EventView
			var position int64
			var facets []events.Facet
			if err := rows.Scan(&position, &ev.ID, &ev.Type, &ev.ActorID, &ev.CredentialID, &ev.RunID, &ev.ChannelID, &ev.ThreadID,
				&ev.Resource.Type, &ev.Resource.ID, &ev.Resource.Revision, &ev.Payload, &facets, &ev.RecordedAt, &ev.EffectiveAt); err != nil {
				rows.Close()
				return nil, 0, "", "", err
			}
			pos = position
			if ev.ChannelID == nil {
				continue
			}
			if _, ok := v.canRead(*ev.ChannelID); !ok {
				continue
			}
			if !req.Filter.MatchEvent(ev.Type, facets) {
				continue
			}
			if strings.HasPrefix(ev.Type, "task.") {
				// Dependency IDs are projected per reader (DEP-010).
				ev.Payload = projectTaskJSON(ev.Payload, v.reader())
			}
			ev.BoardID = boardID
			ev.Cursor = mk(position)
			page.Events = append(page.Events, ev)
			if len(page.Events) >= req.Limit {
				break
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, 0, "", "", err
		}
		if len(page.Events) >= req.Limit || n < s.Cfg.EventScanBatch {
			break
		}
	}
	page.CaughtUp = pos >= head
	page.NextCursor = mk(pos)
	return page, pos, fp, epoch, nil
}

// ---- snapshots ----

type SnapshotInfo struct {
	ID        string    `json:"id"`
	BoardID   string    `json:"board_id"`
	ItemCount int64     `json:"item_count"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type SnapshotItem struct {
	Type string          `json:"type"` // "thread" | "message"
	ID   string          `json:"id"`
	Data json.RawMessage `json:"data"`
}

type SnapshotPage struct {
	Snapshot      SnapshotInfo   `json:"snapshot"`
	Items         []SnapshotItem `json:"items"`
	NextPageToken *string        `json:"next_page_token"`
	// ContinuationCursor is present only on the final page. Replaying
	// events from it with the same filter yields every later change.
	ContinuationCursor *string `json:"continuation_cursor"`
}

// CreateSnapshot captures, in one REPEATABLE READ transaction, every
// readable thread with at least one message matching the filter and those
// matching messages, together with the board position they reflect. Items
// are materialized so pages never hold a database transaction open (ADR-005).
func (s *Service) CreateSnapshot(ctx context.Context, p *Principal, boardID string, f events.Filter) (*SnapshotInfo, error) {
	var info SnapshotInfo
	err := pgx.BeginTxFunc(ctx, s.DB, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		v, err := s.viewBoard(ctx, tx, p, boardID)
		if err != nil {
			return err
		}
		var head int64
		if err := tx.QueryRow(ctx, `SELECT head_position FROM board_heads WHERE board_id=$1`, boardID).Scan(&head); err != nil {
			return err
		}
		var readable []string
		for _, c := range v.chans {
			if v.policy.Has(c.Path, authz.Read) && f.MatchFacet(events.Facet{Invalidation: true, ChannelPath: c.Path, ThreadID: f.ThreadID}) {
				readable = append(readable, c.ID)
			}
		}
		now := s.Now()
		info = SnapshotInfo{ID: ids.New("snp"), BoardID: boardID, CreatedAt: now, ExpiresAt: now.Add(s.Cfg.SnapshotTTL)}
		if _, err := tx.Exec(ctx, `INSERT INTO snapshot_sessions(id, board_id, actor_id, credential_id, query_hash, policy_fingerprint, position, item_count, created_at, expires_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,0,$8,$9)`, info.ID, boardID, p.ActorID, p.CredentialID, f.Hash(), v.policy.Fingerprint(), head, now, info.ExpiresAt); err != nil {
			return err
		}
		if len(readable) == 0 {
			return nil
		}
		trows, err := tx.Query(ctx, `SELECT `+threadCols+` FROM threads t WHERE t.board_id=$1 AND t.channel_id = ANY($2) AND ($3='' OR t.id=$3) ORDER BY t.created_position`,
			boardID, readable, f.ThreadID)
		if err != nil {
			return err
		}
		var threads []ThreadView
		for trows.Next() {
			t, _, err := scanThread(trows, v.chans)
			if err != nil {
				trows.Close()
				return err
			}
			threads = append(threads, t)
		}
		trows.Close()
		if err := trows.Err(); err != nil {
			return err
		}
		var ord int64
		scanned := 0
		add := func(typ, id string, data any) error {
			ord++
			if ord > int64(s.Cfg.SnapshotMaxItems) {
				return E(413, "limit_exceeded", "snapshot exceeds %d items; narrow the filter", s.Cfg.SnapshotMaxItems)
			}
			_, err := tx.Exec(ctx, `INSERT INTO snapshot_items(snapshot_id, ordinal, resource_type, resource_id, payload) VALUES ($1,$2,$3,$4,$5)`,
				info.ID, ord, typ, id, jsonOf(data))
			return err
		}
		for _, t := range threads {
			mrows, err := tx.Query(ctx, `SELECT `+msgCols+` FROM messages m JOIN threads t ON t.id=m.thread_id WHERE m.thread_id=$1 ORDER BY m.seq`, t.ID)
			if err != nil {
				return err
			}
			msgs, err := pgx.CollectRows(mrows, func(r pgx.CollectableRow) (MessageView, error) { return scanMessage(r, v.chans) })
			if err != nil {
				return err
			}
			scanned += len(msgs)
			if scanned > 10*s.Cfg.SnapshotMaxItems {
				return E(413, "limit_exceeded", "snapshot would examine more than %d messages; narrow the filter", 10*s.Cfg.SnapshotMaxItems)
			}
			var matched []MessageView
			for _, m := range msgs {
				if f.MatchFacet(m.facet(t.Labels)) {
					matched = append(matched, m)
				}
			}
			if len(matched) == 0 {
				continue
			}
			if err := add("thread", t.ID, t); err != nil {
				return err
			}
			if t.TaskID != nil {
				// TASK-092: the effective task view at capture time, stored
				// unprojected and projected per reader on every page.
				row, err := loadTaskRow(ctx, tx, boardID, *t.TaskID, false)
				if err != nil {
					return err
				}
				tv, err := buildTaskView(ctx, tx, row, v.chans, nil, now, nil)
				if err != nil {
					return err
				}
				if err := add("task", tv.ID, tv); err != nil {
					return err
				}
			}
			for _, m := range matched {
				if err := add("message", m.ID, m); err != nil {
					return err
				}
			}
		}
		info.ItemCount = ord
		_, err = tx.Exec(ctx, `UPDATE snapshot_sessions SET item_count=$2 WHERE id=$1`, info.ID, ord)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &info, nil
}

type snapshotRow struct {
	info        SnapshotInfo
	actorID     string
	queryHash   string
	fingerprint string
	position    int64
	invalidated *time.Time
}

func (s *Service) loadSnapshot(ctx context.Context, p *Principal, boardID, id string) (*snapshotRow, *boardView, error) {
	v, err := s.viewBoard(ctx, s.DB, p, boardID)
	if err != nil {
		return nil, nil, err
	}
	var r snapshotRow
	err = s.DB.QueryRow(ctx, `SELECT id, board_id, item_count, created_at, expires_at, actor_id, query_hash, policy_fingerprint, position, invalidated_at
		FROM snapshot_sessions WHERE id=$1 AND board_id=$2`, id, boardID).
		Scan(&r.info.ID, &r.info.BoardID, &r.info.ItemCount, &r.info.CreatedAt, &r.info.ExpiresAt, &r.actorID, &r.queryHash, &r.fingerprint, &r.position, &r.invalidated)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && r.actorID != p.ActorID) {
		return nil, nil, NotFound("snapshot")
	}
	if err != nil {
		return nil, nil, err
	}
	return &r, v, nil
}

// SnapshotPage returns one page of a materialized snapshot. Authorization is
// revalidated on every page; an access change invalidates the session.
func (s *Service) SnapshotPage(ctx context.Context, p *Principal, boardID, id string, after int64, limit int) (*SnapshotPage, error) {
	r, v, err := s.loadSnapshot(ctx, p, boardID, id)
	if err != nil {
		return nil, err
	}
	if !s.Now().Before(r.info.ExpiresAt) {
		return nil, E(410, "snapshot_expired", "snapshot session expired; create a new snapshot")
	}
	fp := v.policy.Fingerprint()
	if r.invalidated != nil || fp != r.fingerprint {
		if r.invalidated == nil {
			_, _ = s.DB.Exec(ctx, `UPDATE snapshot_sessions SET invalidated_at=now(), invalidated_reason='policy_changed' WHERE id=$1`, id)
		}
		return nil, resync("policy_changed", "effective access changed during the snapshot; create a new snapshot")
	}
	rows, err := s.DB.Query(ctx, `SELECT ordinal, resource_type, resource_id, payload FROM snapshot_items WHERE snapshot_id=$1 AND ordinal > $2 ORDER BY ordinal LIMIT $3`, id, after, limit)
	if err != nil {
		return nil, err
	}
	page := &SnapshotPage{Snapshot: r.info, Items: []SnapshotItem{}}
	var last int64 = after
	for rows.Next() {
		var it SnapshotItem
		if err := rows.Scan(&last, &it.Type, &it.ID, &it.Data); err != nil {
			rows.Close()
			return nil, err
		}
		if it.Type == "task" {
			it.Data = projectTaskJSON(it.Data, v.reader())
		}
		page.Items = append(page.Items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Redaction overlay: a deletion that committed while this snapshot was
	// being captured may not have scrubbed its items yet; project current
	// tombstones at read time so no page can return erased content (RED-001).
	var msgIDs []string
	for _, it := range page.Items {
		if it.Type == "message" {
			msgIDs = append(msgIDs, it.ID)
		}
	}
	if len(msgIDs) > 0 {
		drows, err := s.DB.Query(ctx, `SELECT id FROM messages WHERE board_id=$1 AND id = ANY($2) AND deleted_at IS NOT NULL`, boardID, msgIDs)
		if err != nil {
			return nil, err
		}
		deleted, err := pgx.CollectRows(drows, pgx.RowTo[string])
		if err != nil {
			return nil, err
		}
		for _, id := range deleted {
			m, err := loadMessage(ctx, s.DB, boardID, id, v.chans, false)
			if err != nil {
				return nil, err
			}
			for i := range page.Items {
				if page.Items[i].Type == "message" && page.Items[i].ID == id {
					page.Items[i].Data = jsonOf(m)
				}
			}
		}
	}
	if last < r.info.ItemCount {
		tok := encodePageToken(last)
		page.NextPageToken = &tok
	} else {
		epoch, err := s.historyEpoch(ctx, s.DB)
		if err != nil {
			return nil, err
		}
		c := events.Cursor{BoardID: boardID, Epoch: epoch, Position: r.position, Query: r.queryHash, Policy: r.fingerprint}.Encode()
		page.ContinuationCursor = &c
	}
	return page, nil
}

func (s *Service) DeleteSnapshot(ctx context.Context, p *Principal, boardID, id string) error {
	if _, _, err := s.loadSnapshot(ctx, p, boardID, id); err != nil {
		return err
	}
	_, err := s.DB.Exec(ctx, `DELETE FROM snapshot_sessions WHERE id=$1`, id)
	return err
}
