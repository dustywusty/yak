package domain

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/abby-protocol/abby/internal/authz"
	"github.com/abby-protocol/abby/internal/events"
	"github.com/abby-protocol/abby/internal/ids"
)

type Attribution struct {
	CredentialID string  `json:"credential_id"`
	RunID        *string `json:"run_id"`
}

type DeletionView struct {
	Kind      string    `json:"kind"` // "author" | "redaction"
	At        time.Time `json:"at"`
	ByActorID string    `json:"by_actor_id"`
}

type MessageView struct {
	ID               string            `json:"id"`
	BoardID          string            `json:"board_id"`
	ThreadID         string            `json:"thread_id"`
	ChannelID        string            `json:"channel_id"`
	ChannelPath      string            `json:"channel_path"`
	Seq              int64             `json:"seq"`
	IsRoot           bool              `json:"is_root"`
	AuthorActorID    string            `json:"author_actor_id"`
	Attribution      Attribution       `json:"attribution"`
	ReplyToMessageID *string           `json:"reply_to_message_id"`
	Text             *string           `json:"text"`
	Labels           map[string]string `json:"labels"`
	Mentions         []string          `json:"mentions"`
	Extensions       map[string]any    `json:"extensions"`
	Revision         int64             `json:"revision"`
	CreatedAt        time.Time         `json:"created_at"`
	EditedAt         *time.Time        `json:"edited_at"`
	Deleted          *DeletionView     `json:"deleted"`
}

type ThreadView struct {
	ID            string            `json:"id"`
	BoardID       string            `json:"board_id"`
	ChannelID     string            `json:"channel_id"`
	ChannelPath   string            `json:"channel_path"`
	Title         string            `json:"title"`
	RootMessageID string            `json:"root_message_id"`
	CreatedBy     string            `json:"created_by_actor_id"`
	Labels        map[string]string `json:"labels"` // root message labels
	Revision      int64             `json:"revision"`
	MessageCount  int64             `json:"message_count"`
	LastMessageID string            `json:"last_message_id"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
	// TaskID is the thread's task, or null (P2 TASK-024).
	TaskID *string `json:"task_id"`
}

// ThreadWithRoot is the response to thread creation and thread reads.
type ThreadWithRoot struct {
	Thread ThreadView  `json:"thread"`
	Root   MessageView `json:"root_message"`
}

func (m *MessageView) facet(rootLabels map[string]string) events.Facet {
	f := events.Facet{ChannelPath: m.ChannelPath, ThreadID: m.ThreadID, AuthorID: m.AuthorActorID, Mentions: m.Mentions, Labels: m.Labels, RootLabels: rootLabels}
	if m.Text != nil {
		f.Text = *m.Text
	}
	return f
}

// taskStateLabelSQL is the computed root label abby.task.state of thread
// alias t: the effective task state (ADR-0004), or {} for a thread without a
// task. It is never stored in message_labels (ADR-0016 §5, TASK-027..029).
const taskStateLabelSQL = `COALESCE((SELECT jsonb_build_object('abby.task.state', abby_task_effective_state(tk.state, tk.claim_expires_at, statement_timestamp()))
	FROM tasks tk WHERE tk.thread_id=t.id), '{}'::jsonb)`

const msgCols = `m.id, m.board_id, m.thread_id, t.channel_id, m.seq, (t.root_message_id = m.id), m.author_actor_id, m.credential_id, m.run_id,
	m.reply_to_message_id, m.text, m.extensions, m.revision, m.created_at, m.edited_at, m.deleted_at, m.deleted_by, m.deletion_kind,
	COALESCE((SELECT jsonb_object_agg(key, value) FROM message_labels WHERE message_id=m.id), '{}'::jsonb)
	  || CASE WHEN t.root_message_id = m.id THEN ` + taskStateLabelSQL + ` ELSE '{}'::jsonb END,
	COALESCE((SELECT array_agg(actor_id ORDER BY actor_id) FROM message_mentions WHERE message_id=m.id), '{}')`

func scanMessage(r pgx.Row, chans map[string]channelRow) (MessageView, error) {
	var m MessageView
	var delAt *time.Time
	var delBy, delKind *string
	err := r.Scan(&m.ID, &m.BoardID, &m.ThreadID, &m.ChannelID, &m.Seq, &m.IsRoot, &m.AuthorActorID, &m.Attribution.CredentialID, &m.Attribution.RunID,
		&m.ReplyToMessageID, &m.Text, &m.Extensions, &m.Revision, &m.CreatedAt, &m.EditedAt, &delAt, &delBy, &delKind, &m.Labels, &m.Mentions)
	if err != nil {
		return m, err
	}
	for _, c := range chans {
		if c.ID == m.ChannelID {
			m.ChannelPath = c.Path
			break
		}
	}
	if delAt != nil {
		m.Deleted = &DeletionView{Kind: *delKind, At: *delAt, ByActorID: *delBy}
	}
	return m, nil
}

func loadMessage(ctx context.Context, q querier, boardID, id string, chans map[string]channelRow, lock bool) (MessageView, error) {
	sql := `SELECT ` + msgCols + ` FROM messages m JOIN threads t ON t.id=m.thread_id WHERE m.board_id=$1 AND m.id=$2`
	if lock {
		sql += ` FOR UPDATE OF m`
	}
	m, err := scanMessage(q.QueryRow(ctx, sql, boardID, id), chans)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, NotFound("message")
	}
	return m, err
}

const threadCols = `t.id, t.board_id, t.channel_id, t.title, t.root_message_id, t.created_by, t.revision, t.message_count, t.last_message_id, t.created_at, t.updated_at,
	COALESCE((SELECT jsonb_object_agg(key, value) FROM message_labels WHERE message_id=t.root_message_id), '{}'::jsonb) || ` + taskStateLabelSQL + `,
	t.created_position, (SELECT tk.id FROM tasks tk WHERE tk.thread_id=t.id)`

func scanThread(r pgx.Row, chans map[string]channelRow) (ThreadView, int64, error) {
	var t ThreadView
	var pos int64
	err := r.Scan(&t.ID, &t.BoardID, &t.ChannelID, &t.Title, &t.RootMessageID, &t.CreatedBy, &t.Revision, &t.MessageCount, &t.LastMessageID, &t.CreatedAt, &t.UpdatedAt, &t.Labels, &pos, &t.TaskID)
	if err != nil {
		return t, 0, err
	}
	for _, c := range chans {
		if c.ID == t.ChannelID {
			t.ChannelPath = c.Path
			break
		}
	}
	return t, pos, nil
}

func loadThread(ctx context.Context, q querier, boardID, id string, chans map[string]channelRow, lock bool) (ThreadView, error) {
	sql := `SELECT ` + threadCols + ` FROM threads t WHERE t.board_id=$1 AND t.id=$2`
	if lock {
		sql += ` FOR UPDATE`
	}
	t, _, err := scanThread(q.QueryRow(ctx, sql, boardID, id), chans)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, NotFound("thread")
	}
	return t, err
}

// ---- validation ----

func (s *Service) validateText(text string) error {
	if !utf8.ValidString(text) {
		return Validation("text must be valid UTF-8")
	}
	if len(text) > s.Cfg.MaxTextBytes {
		return E(413, "limit_exceeded", "text exceeds %d bytes", s.Cfg.MaxTextBytes)
	}
	return nil
}

func (s *Service) normalizeLabels(in map[string]string) (map[string]string, error) {
	out := map[string]string{}
	if len(in) > s.Cfg.MaxLabelsPerMessage {
		return nil, E(413, "limit_exceeded", "at most %d labels per message", s.Cfg.MaxLabelsPerMessage)
	}
	for k, v := range in {
		k = strings.ToLower(k)
		if err := events.ValidateLabelKey(k, false); err != nil {
			return nil, Validation("label %q: %s", k, err)
		}
		if err := s.validateLabelValue(v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, nil
}

func (s *Service) validateLabelValue(v string) error {
	if !utf8.ValidString(v) || len(v) > s.Cfg.MaxLabelValueBytes {
		return Validation("label values must be UTF-8 of at most %d bytes", s.Cfg.MaxLabelValueBytes)
	}
	return nil
}

func validateExtensions(ext map[string]any) error {
	for k := range ext {
		if !strings.Contains(k, ".") {
			return Validation("extension keys must be namespaced (e.g. \"vendor.feature\"), got %q", k)
		}
	}
	return nil
}

var mentionRE = regexp.MustCompile(`(?:^|[^A-Za-z0-9_@])@([a-z0-9][a-z0-9_-]{0,62})`)

// resolveMentions resolves explicit mention IDs and @handles in text to
// actors who can currently read the target channel. Unknown or ineligible
// handles remain plain text. Explicit IDs that cannot be resolved fail.
func (b *btx) resolveMentions(path, text string, explicit []string) ([]string, error) {
	set := map[string]bool{}
	canRead := func(actorID string) (bool, error) {
		other := &Principal{ActorID: actorID, Scopes: []string{authz.AllScopes}}
		pol, _, err := b.s.loadPolicy(b.ctx, b.Tx, other, &b.board)
		if err != nil {
			return false, err
		}
		return pol.Has(path, authz.Read), nil
	}
	for _, id := range explicit {
		var exists bool
		if err := b.QueryRow(b.ctx, `SELECT EXISTS(SELECT 1 FROM actors WHERE id=$1 AND disabled_at IS NULL)`, id).Scan(&exists); err != nil {
			return nil, err
		}
		ok := false
		if exists {
			var err error
			if ok, err = canRead(id); err != nil {
				return nil, err
			}
		}
		if !ok {
			return nil, Validation("mention %q is not an actor who can read this channel", id)
		}
		set[id] = true
	}
	for _, m := range mentionRE.FindAllStringSubmatch(text, 50) {
		var id string
		err := b.QueryRow(b.ctx, `SELECT id FROM actors WHERE handle=$1 AND disabled_at IS NULL`, m[1]).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if ok, err := canRead(id); err != nil {
			return nil, err
		} else if ok {
			set[id] = true
		}
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

func (b *btx) insertMessage(m *MessageView, labels map[string]string) error {
	ext, _ := json.Marshal(m.Extensions)
	if m.Extensions == nil {
		ext = nil
	}
	var runID *string
	if b.p.RunID != "" {
		runID = &b.p.RunID
	}
	m.Attribution = Attribution{CredentialID: b.p.CredentialID, RunID: runID}
	if _, err := b.Exec(b.ctx, `INSERT INTO messages(id, board_id, thread_id, seq, author_actor_id, credential_id, run_id, reply_to_message_id, text, extensions, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, m.ID, m.BoardID, m.ThreadID, m.Seq, m.AuthorActorID, b.p.CredentialID, runID, m.ReplyToMessageID, m.Text, ext, m.CreatedAt); err != nil {
		return err
	}
	for k, v := range labels {
		if _, err := b.Exec(b.ctx, `INSERT INTO message_labels(message_id, key, value) VALUES ($1,$2,$3)`, m.ID, k, v); err != nil {
			return err
		}
	}
	for _, a := range m.Mentions {
		if _, err := b.Exec(b.ctx, `INSERT INTO message_mentions(message_id, actor_id) VALUES ($1,$2)`, m.ID, a); err != nil {
			return err
		}
	}
	return nil
}

// ---- threads ----

type CreateThreadRequest struct {
	Channel    string            `json:"channel"`
	Title      string            `json:"title"`
	Text       string            `json:"text"`
	Labels     map[string]string `json:"labels"`
	Mentions   []string          `json:"mentions"`
	Extensions map[string]any    `json:"extensions"`
}

// CreateThread atomically creates a thread and its root message. If the
// channel does not exist and the caller holds channel_manage there, an
// inherited channel is created in the same transaction.
func (s *Service) CreateThread(ctx context.Context, p *Principal, boardID string, idem *Idem, req CreateThreadRequest) (*Result, error) {
	if err := authz.ValidatePath(req.Channel); err != nil {
		return nil, Validation("channel: %s", err)
	}
	if err := validName(req.Title, s.Cfg.MaxTitleBytes); err != nil {
		return nil, Validation("title must be 1..%d bytes of UTF-8", s.Cfg.MaxTitleBytes)
	}
	if err := s.validateText(req.Text); err != nil {
		return nil, err
	}
	labels, err := s.normalizeLabels(req.Labels)
	if err != nil {
		return nil, err
	}
	req.Labels = labels
	req.Mentions = sortedUnique(req.Mentions)
	if err := validateExtensions(req.Extensions); err != nil {
		return nil, err
	}
	idem.Request = req
	return s.boardMutation(ctx, p, boardID, idem, func(b *btx) (*Result, error) {
		// Without read, a channel is indistinguishable from an absent one
		// (AUTH-015); only readable targets reveal a 403.
		if !b.policy.Has(req.Channel, authz.Read) {
			return nil, E(404, "not_found", "channel %q not found", req.Channel)
		}
		if !b.policy.Has(req.Channel, authz.Post) {
			return nil, Forbidden("post required in %q", req.Channel)
		}
		ch, ok := b.chans[req.Channel]
		if !ok {
			if !b.policy.Has(req.Channel, authz.ChannelManage) {
				return nil, E(404, "not_found", "channel %q does not exist and caller may not create it", req.Channel)
			}
			var err error
			if ch, err = b.createChannel(req.Channel, "inherited"); err != nil {
				return nil, err
			}
		}
		body, err := b.insertThread(ch, req, "", "")
		if err != nil {
			return nil, err
		}
		return &Result{Status: 201, ResourceType: "thread", ResourceID: body.Thread.ID, Body: body}, nil
	})
}

// insertThread creates a thread and its root message and emits
// thread.created. For a task thread (taskID set) the views already carry the
// computed root label abby.task.state and task_id (TASK-085: no separate
// view invalidation is needed for L1).
func (b *btx) insertThread(ch channelRow, req CreateThreadRequest, taskID, taskState string) (ThreadWithRoot, error) {
	mentions, err := b.resolveMentions(ch.Path, req.Text, req.Mentions)
	if err != nil {
		return ThreadWithRoot{}, err
	}
	labels := maps.Clone(req.Labels)
	if labels == nil {
		labels = map[string]string{}
	}
	viewLabels := maps.Clone(labels)
	var tidPtr *string
	if taskID != "" {
		viewLabels[TaskStateLabel] = taskState
		tidPtr = &taskID
	}
	boardID := b.board.ID
	tid, mid := ids.New("thr"), ids.New("msg")
	text := req.Text
	m := MessageView{ID: mid, BoardID: boardID, ThreadID: tid, ChannelID: ch.ID, ChannelPath: ch.Path, Seq: 1, IsRoot: true,
		AuthorActorID: b.p.ActorID, Text: &text, Labels: viewLabels, Mentions: mentions, Extensions: req.Extensions, Revision: 1, CreatedAt: b.now}
	t := ThreadView{ID: tid, BoardID: boardID, ChannelID: ch.ID, ChannelPath: ch.Path, Title: req.Title, RootMessageID: mid, CreatedBy: b.p.ActorID,
		Labels: viewLabels, Revision: 1, MessageCount: 1, LastMessageID: mid, CreatedAt: b.now, UpdatedAt: b.now, TaskID: tidPtr}
	pos := b.head + 1
	if _, err := b.Exec(b.ctx, `INSERT INTO threads(id, board_id, channel_id, title, root_message_id, created_by, last_message_id, created_position, activity_position, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$5,$7,$7,$8,$8)`, tid, boardID, ch.ID, req.Title, mid, b.p.ActorID, pos, b.now); err != nil {
		return ThreadWithRoot{}, err
	}
	if err := b.insertMessage(&m, labels); err != nil {
		return ThreadWithRoot{}, err
	}
	if _, err := b.emit(PendingEvent{Type: "thread.created", ChannelID: ch.ID, ThreadID: tid, ResourceType: "thread", ResourceID: tid, Revision: 1,
		Payload: map[string]any{"thread": t, "message": m}, Facets: []events.Facet{m.facet(viewLabels)}}); err != nil {
		return ThreadWithRoot{}, err
	}
	return ThreadWithRoot{Thread: t, Root: m}, nil
}

func (s *Service) GetThread(ctx context.Context, p *Principal, boardID, threadID string) (*ThreadWithRoot, error) {
	v, err := s.viewBoard(ctx, s.DB, p, boardID)
	if err != nil {
		return nil, err
	}
	t, err := loadThread(ctx, s.DB, boardID, threadID, v.chans, false)
	if err != nil {
		return nil, err
	}
	if _, ok := v.canRead(t.ChannelID); !ok {
		return nil, NotFound("thread")
	}
	root, err := loadMessage(ctx, s.DB, boardID, t.RootMessageID, v.chans, false)
	if err != nil {
		return nil, err
	}
	return &ThreadWithRoot{Thread: t, Root: root}, nil
}

// ThreadPage is a page of thread summaries in creation order.
type ThreadPage struct {
	Threads       []ThreadView `json:"threads"`
	NextPageToken *string      `json:"next_page_token"`
}

// ListThreads returns readable threads in creation order (stable under
// concurrent activity). Filters: channel, subtree, author_id (root author),
// label (root labels).
func (s *Service) ListThreads(ctx context.Context, p *Principal, boardID string, f events.Filter, after int64, limit int) (*ThreadPage, error) {
	v, err := s.viewBoard(ctx, s.DB, p, boardID)
	if err != nil {
		return nil, err
	}
	// Thread listing supports channel, subtree, thread_id, author_id (root
	// author), and label (root labels). Message-level clauses are rejected
	// rather than silently ignored.
	if f.Mention != "" || f.Text != "" || len(f.Types) > 0 {
		return nil, Validation("thread listing supports channel, subtree, thread_id, author_id and label filters; use events or snapshots for mention, text and type")
	}
	var readable []string
	for _, c := range v.chans {
		if v.policy.Has(c.Path, authz.Read) && (events.Filter{Channel: f.Channel, Subtree: f.Subtree}).MatchFacet(events.Facet{ChannelPath: c.Path, Invalidation: true}) {
			readable = append(readable, c.ID)
		}
	}
	out := &ThreadPage{Threads: []ThreadView{}}
	if len(readable) == 0 {
		return out, nil
	}
	cursor := after
	batchSize := max(limit+1, 200)
	for batches := 0; ; batches++ {
		if batches == 10 {
			// Bound the work of one request; continue from the last
			// examined thread (the page may be short or empty).
			tok := encodePageToken(cursor)
			out.NextPageToken = &tok
			return out, nil
		}
		rows, err := s.DB.Query(ctx, `SELECT `+threadCols+` FROM threads t WHERE t.board_id=$1 AND t.channel_id = ANY($2) AND t.created_position > $3
			AND ($4 = '' OR t.created_by = $4) AND ($6 = '' OR t.id = $6) ORDER BY t.created_position LIMIT $5`, boardID, readable, cursor, f.AuthorID, batchSize, f.ThreadID)
		if err != nil {
			return nil, err
		}
		var batch []ThreadView
		var positions []int64
		for rows.Next() {
			t, pos, err := scanThread(rows, v.chans)
			if err != nil {
				rows.Close()
				return nil, err
			}
			batch, positions = append(batch, t), append(positions, pos)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		labelOnly := events.Filter{Labels: f.Labels}
		for i, t := range batch {
			if len(out.Threads) == limit {
				tok := encodePageToken(cursor)
				out.NextPageToken = &tok
				return out, nil
			}
			cursor = positions[i]
			if labelOnly.MatchFacet(events.Facet{ChannelPath: t.ChannelPath, ThreadScope: true, RootLabels: t.Labels}) {
				out.Threads = append(out.Threads, t)
			}
		}
		if len(batch) < batchSize {
			return out, nil
		}
	}
}

// ---- messages ----

type ReplyRequest struct {
	Text                  string            `json:"text"`
	ReplyToMessageID      *string           `json:"reply_to_message_id"`
	ExpectLatestMessageID *string           `json:"expect_latest_message_id"`
	Labels                map[string]string `json:"labels"`
	Mentions              []string          `json:"mentions"`
	Extensions            map[string]any    `json:"extensions"`
}

// Reply appends one message. A guarded reply (expect_latest_message_id)
// commits only if that message is still the thread's latest message;
// otherwise 409 newer_message and nothing is committed.
func (s *Service) Reply(ctx context.Context, p *Principal, boardID, threadID string, idem *Idem, req ReplyRequest) (*Result, error) {
	if err := s.validateText(req.Text); err != nil {
		return nil, err
	}
	if req.Text == "" {
		return nil, Validation("text must not be empty")
	}
	labels, err := s.normalizeLabels(req.Labels)
	if err != nil {
		return nil, err
	}
	req.Labels = labels
	req.Mentions = sortedUnique(req.Mentions)
	if err := validateExtensions(req.Extensions); err != nil {
		return nil, err
	}
	idem.Request = req
	return s.boardMutation(ctx, p, boardID, idem, func(b *btx) (*Result, error) {
		t, err := loadThread(ctx, b, boardID, threadID, b.chans, true)
		if err != nil {
			return nil, err
		}
		ch := b.chans[t.ChannelPath]
		if !b.policy.Has(ch.Path, authz.Read) {
			return nil, NotFound("thread")
		}
		if !b.policy.Has(ch.Path, authz.Post) {
			return nil, Forbidden("post required in %q", ch.Path)
		}
		if req.ExpectLatestMessageID != nil && *req.ExpectLatestMessageID != t.LastMessageID {
			e := E(409, "newer_message", "thread has a newer message than %s", *req.ExpectLatestMessageID)
			e.Extra = map[string]any{"latest_message_id": t.LastMessageID}
			return nil, e
		}
		if req.ReplyToMessageID != nil {
			var tid string
			err := b.QueryRow(ctx, `SELECT thread_id FROM messages WHERE id=$1 AND board_id=$2`, *req.ReplyToMessageID, boardID).Scan(&tid)
			if errors.Is(err, pgx.ErrNoRows) || (err == nil && tid != threadID) {
				return nil, Validation("reply_to_message_id must identify a message in this thread")
			}
			if err != nil {
				return nil, err
			}
		}
		mentions, err := b.resolveMentions(ch.Path, req.Text, req.Mentions)
		if err != nil {
			return nil, err
		}
		var lastSeq int64
		if err := b.QueryRow(ctx, `SELECT last_seq FROM threads WHERE id=$1`, threadID).Scan(&lastSeq); err != nil {
			return nil, err
		}
		text := req.Text
		m := MessageView{ID: ids.New("msg"), BoardID: boardID, ThreadID: threadID, ChannelID: ch.ID, ChannelPath: ch.Path, Seq: lastSeq + 1,
			AuthorActorID: p.ActorID, ReplyToMessageID: req.ReplyToMessageID, Text: &text, Labels: labels, Mentions: mentions,
			Extensions: req.Extensions, Revision: 1, CreatedAt: b.now}
		if err := b.insertMessage(&m, labels); err != nil {
			return nil, err
		}
		pos := b.head + 1
		if _, err := b.Exec(ctx, `UPDATE threads SET last_seq=$2, last_message_id=$3, message_count=message_count+1, activity_position=$4, updated_at=$5 WHERE id=$1`,
			threadID, m.Seq, m.ID, pos, b.now); err != nil {
			return nil, err
		}
		if _, err := b.emit(PendingEvent{Type: "message.created", ChannelID: ch.ID, ThreadID: threadID, ResourceType: "message", ResourceID: m.ID, Revision: 1,
			Payload: map[string]any{"message": m}, Facets: []events.Facet{m.facet(t.Labels)}}); err != nil {
			return nil, err
		}
		return &Result{Status: 201, ResourceType: "message", ResourceID: m.ID, Body: m}, nil
	})
}

func (s *Service) GetMessage(ctx context.Context, p *Principal, boardID, messageID string) (*MessageView, error) {
	v, err := s.viewBoard(ctx, s.DB, p, boardID)
	if err != nil {
		return nil, err
	}
	m, err := loadMessage(ctx, s.DB, boardID, messageID, v.chans, false)
	if err != nil {
		return nil, err
	}
	if _, ok := v.canRead(m.ChannelID); !ok {
		return nil, NotFound("message")
	}
	return &m, nil
}

type MessagePage struct {
	Messages      []MessageView `json:"messages"`
	NextPageToken *string       `json:"next_page_token"`
}

func (s *Service) ListMessages(ctx context.Context, p *Principal, boardID, threadID string, afterSeq int64, limit int) (*MessagePage, error) {
	v, err := s.viewBoard(ctx, s.DB, p, boardID)
	if err != nil {
		return nil, err
	}
	t, err := loadThread(ctx, s.DB, boardID, threadID, v.chans, false)
	if err != nil {
		return nil, err
	}
	if _, ok := v.canRead(t.ChannelID); !ok {
		return nil, NotFound("thread")
	}
	rows, err := s.DB.Query(ctx, `SELECT `+msgCols+` FROM messages m JOIN threads t ON t.id=m.thread_id WHERE m.thread_id=$1 AND m.seq > $2 ORDER BY m.seq LIMIT $3`, threadID, afterSeq, limit+1)
	if err != nil {
		return nil, err
	}
	msgs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (MessageView, error) { return scanMessage(r, v.chans) })
	if err != nil {
		return nil, err
	}
	out := &MessagePage{Messages: msgs}
	if len(msgs) > limit {
		out.Messages = msgs[:limit]
		tok := encodePageToken(msgs[limit-1].Seq)
		out.NextPageToken = &tok
	}
	if out.Messages == nil {
		out.Messages = []MessageView{}
	}
	return out, nil
}

type EditMessageRequest struct {
	Text     *string  `json:"text"`
	Mentions []string `json:"mentions"`
}

// EditMessage lets the author change their own message under a revision
// precondition while they still hold read and post.
func (s *Service) EditMessage(ctx context.Context, p *Principal, boardID, messageID string, ifMatch int64, req EditMessageRequest) (*MessageView, error) {
	if req.Text == nil {
		return nil, Validation("text is required")
	}
	if err := s.validateText(*req.Text); err != nil {
		return nil, err
	}
	var out MessageView
	_, err := s.boardMutation(ctx, p, boardID, nil, func(b *btx) (*Result, error) {
		m, err := loadMessage(ctx, b, boardID, messageID, b.chans, true)
		if err != nil {
			return nil, err
		}
		if !b.policy.Has(m.ChannelPath, authz.Read) {
			return nil, NotFound("message")
		}
		if err := b.rootProtected(m); err != nil {
			return nil, err
		}
		if m.AuthorActorID != p.ActorID || !b.policy.Has(m.ChannelPath, authz.Post) {
			return nil, Forbidden("only the author with current read and post may edit")
		}
		if m.Deleted != nil {
			return nil, E(409, "invalid_transition", "message is deleted")
		}
		if m.Revision != ifMatch {
			return nil, E(412, "revision_conflict", "message revision is %d", m.Revision)
		}
		t, err := loadThread(ctx, b, boardID, m.ThreadID, b.chans, false)
		if err != nil {
			return nil, err
		}
		mentions, err := b.resolveMentions(m.ChannelPath, *req.Text, sortedUnique(req.Mentions))
		if err != nil {
			return nil, err
		}
		before := m
		m.Text, m.Mentions, m.Revision, m.EditedAt = req.Text, mentions, m.Revision+1, &b.now
		if _, err := b.Exec(ctx, `UPDATE messages SET text=$2, revision=$3, edited_at=$4 WHERE id=$1`, m.ID, *m.Text, m.Revision, b.now); err != nil {
			return nil, err
		}
		if _, err := b.Exec(ctx, `DELETE FROM message_mentions WHERE message_id=$1`, m.ID); err != nil {
			return nil, err
		}
		for _, a := range mentions {
			if _, err := b.Exec(ctx, `INSERT INTO message_mentions(message_id, actor_id) VALUES ($1,$2)`, m.ID, a); err != nil {
				return nil, err
			}
		}
		if _, err := b.emit(PendingEvent{Type: "message.edited", ChannelID: m.ChannelID, ThreadID: m.ThreadID, ResourceType: "message", ResourceID: m.ID,
			Revision: m.Revision, Payload: map[string]any{"message": m}, Facets: []events.Facet{before.facet(t.Labels), m.facet(t.Labels)}}); err != nil {
			return nil, err
		}
		out = m
		return &Result{}, nil
	})
	return &out, err
}

// DeleteMessage tombstones a message. The author may delete their own
// message with current read and post. Anyone else needs board_admin, current
// read, and a reason (administrative redaction, audited). The former body is
// scrubbed from event payloads/facets and snapshot items in the same
// transaction; idempotency replays regenerate projections and so never
// return it (ADR-005, CT-017).
func (s *Service) DeleteMessage(ctx context.Context, p *Principal, boardID, messageID string, ifMatch int64, reason string) (*MessageView, error) {
	var out MessageView
	_, err := s.boardMutation(ctx, p, boardID, nil, func(b *btx) (*Result, error) {
		m, err := loadMessage(ctx, b, boardID, messageID, b.chans, true)
		if err != nil {
			return nil, err
		}
		if !b.policy.Has(m.ChannelPath, authz.Read) {
			return nil, NotFound("message")
		}
		if err := b.rootProtected(m); err != nil {
			return nil, err
		}
		kind := "author"
		if m.AuthorActorID == p.ActorID {
			if !b.policy.Has(m.ChannelPath, authz.Post) {
				return nil, Forbidden("post required to delete own message")
			}
		} else {
			if !b.policy.IsBoardAdmin() {
				return nil, Forbidden("redacting another actor's message requires board_admin")
			}
			if strings.TrimSpace(reason) == "" {
				return nil, Validation("redaction requires a reason")
			}
			kind = "redaction"
		}
		if m.Deleted != nil {
			out = m
			return &Result{}, nil
		}
		if m.Revision != ifMatch {
			return nil, E(412, "revision_conflict", "message revision is %d", m.Revision)
		}
		if out, err = b.tombstoneMessage(m, kind, reason); err != nil {
			return nil, err
		}
		return &Result{}, nil
	})
	return &out, err
}

// tombstoneMessage deletes a message (kind "author" or "redaction"), scrubs
// every retained copy of its content, audits a redaction, and emits
// message.deleted.
func (b *btx) tombstoneMessage(m MessageView, kind, reason string) (MessageView, error) {
	t, err := loadThread(b.ctx, b, b.board.ID, m.ThreadID, b.chans, false)
	if err != nil {
		return m, err
	}
	m.Revision++
	m.Text, m.Mentions, m.Extensions = nil, []string{}, nil
	m.Deleted = &DeletionView{Kind: kind, At: b.now, ByActorID: b.p.ActorID}
	if _, err := b.Exec(b.ctx, `UPDATE messages SET text=NULL, extensions=NULL, revision=$2, deleted_at=$3, deleted_by=$4, deletion_kind=$5 WHERE id=$1`,
		m.ID, m.Revision, b.now, b.p.ActorID, kind); err != nil {
		return m, err
	}
	if _, err := b.Exec(b.ctx, `DELETE FROM message_mentions WHERE message_id=$1`, m.ID); err != nil {
		return m, err
	}
	if err := b.scrubMessage(m, t.Labels); err != nil {
		return m, err
	}
	if kind == "redaction" {
		if err := b.audit("message.redact", "message", m.ID, reason, map[string]any{"thread_id": m.ThreadID, "author_actor_id": m.AuthorActorID}); err != nil {
			return m, err
		}
	}
	inval := events.Facet{Invalidation: true, ChannelPath: m.ChannelPath, ThreadID: m.ThreadID}
	if _, err := b.emit(PendingEvent{Type: "message.deleted", ChannelID: m.ChannelID, ThreadID: m.ThreadID, ResourceType: "message", ResourceID: m.ID,
		Revision: m.Revision, Payload: map[string]any{"message": m}, Facets: []events.Facet{inval}}); err != nil {
		return m, err
	}
	return m, nil
}

// rootProtected rejects P1 edits and deletions of a task thread's root
// (BRIEF-010; amends AUTH-020 for task threads).
func (b *btx) rootProtected(m MessageView) error {
	if !m.IsRoot {
		return nil
	}
	var taskID string
	err := b.QueryRow(b.ctx, `SELECT id FROM tasks WHERE thread_id=$1`, m.ThreadID).Scan(&taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	e := E(409, "task_root_protected", "the root of a task thread changes only through task operations")
	e.Extra = map[string]any{"task_id": taskID}
	return e
}

// scrubMessage replaces every retained projection of a message's content with
// the tombstone: event payloads and facets, and snapshot items.
func (b *btx) scrubMessage(tomb MessageView, rootLabels map[string]string) error {
	tj, _ := json.Marshal(tomb)
	// Root messages also appear inside their thread.created event, whose
	// resource is the thread; both lookups use the resource_id index.
	rootThread := ""
	if tomb.IsRoot {
		rootThread = tomb.ThreadID
	}
	rows, err := b.Query(b.ctx, `SELECT position, facets FROM events WHERE board_id=$1 AND (resource_id=$2 OR (resource_id=$3 AND type='thread.created'))`,
		b.board.ID, tomb.ID, rootThread)
	if err != nil {
		return err
	}
	type upd struct {
		pos    int64
		facets []events.Facet
	}
	var ups []upd
	for rows.Next() {
		var u upd
		if err := rows.Scan(&u.pos, &u.facets); err != nil {
			rows.Close()
			return err
		}
		for i := range u.facets {
			u.facets[i].Text, u.facets[i].Mentions = "", nil
		}
		ups = append(ups, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, u := range ups {
		fj, _ := json.Marshal(u.facets)
		if _, err := b.Exec(b.ctx, `UPDATE events SET payload = jsonb_set(payload, '{message}', $3::jsonb), facets=$4 WHERE board_id=$1 AND position=$2`,
			b.board.ID, u.pos, tj, fj); err != nil {
			return err
		}
	}
	_, err = b.Exec(b.ctx, `UPDATE snapshot_items SET payload=$2 WHERE resource_type='message' AND resource_id=$1`, tomb.ID, tj)
	return err
}

// ---- labels ----

type LabelExpectation struct {
	Absent  bool    `json:"absent,omitempty"`
	Present bool    `json:"present,omitempty"`
	Value   *string `json:"value,omitempty"`
}

type LabelOp struct {
	Op     string            `json:"op"` // "set" | "unset"
	Key    string            `json:"key"`
	Value  *string           `json:"value,omitempty"`
	Expect *LabelExpectation `json:"expect,omitempty"`
}

type LabelsRequest struct {
	Operations []LabelOp `json:"operations"`
}

// MutateLabels applies all operations atomically or none. Each operation may
// carry an expectation evaluated against the labels before the request; any
// failed expectation returns 412 revision_conflict and commits nothing.
func (s *Service) MutateLabels(ctx context.Context, p *Principal, boardID, messageID string, ifMatch *int64, req LabelsRequest) (*MessageView, error) {
	if len(req.Operations) == 0 {
		return nil, Validation("operations must not be empty")
	}
	for i := range req.Operations {
		op := &req.Operations[i]
		op.Key = strings.ToLower(op.Key)
		if err := events.ValidateLabelKey(op.Key, false); err != nil {
			return nil, Validation("label %q: %s", op.Key, err)
		}
		switch op.Op {
		case "set":
			if op.Value == nil {
				return nil, Validation("set requires value")
			}
			if err := s.validateLabelValue(*op.Value); err != nil {
				return nil, err
			}
		case "unset":
			if op.Value != nil {
				return nil, Validation("unset does not take value")
			}
		default:
			return nil, Validation("op must be set or unset")
		}
	}
	var out MessageView
	_, err := s.boardMutation(ctx, p, boardID, nil, func(b *btx) (*Result, error) {
		m, err := loadMessage(ctx, b, boardID, messageID, b.chans, true)
		if err != nil {
			return nil, err
		}
		if !b.policy.Has(m.ChannelPath, authz.Read) {
			return nil, NotFound("message")
		}
		if !b.policy.Has(m.ChannelPath, authz.Label) {
			return nil, Forbidden("label required in %q", m.ChannelPath)
		}
		if ifMatch != nil && *ifMatch != m.Revision {
			return nil, E(412, "revision_conflict", "message revision is %d", m.Revision)
		}
		before := maps.Clone(m.Labels)
		for _, op := range req.Operations {
			cur, has := before[op.Key]
			if x := op.Expect; x != nil {
				if (x.Absent && has) || (x.Present && !has) || (x.Value != nil && (!has || cur != *x.Value)) {
					e := E(412, "revision_conflict", "label %q expectation failed", op.Key)
					e.Extra = map[string]any{"key": op.Key, "current": func() any {
						if has {
							return cur
						}
						return nil
					}()}
					return nil, e
				}
			}
		}
		after := maps.Clone(before)
		for _, op := range req.Operations {
			if op.Op == "set" {
				after[op.Key] = *op.Value
			} else {
				delete(after, op.Key)
			}
		}
		stored := 0
		for k := range after {
			if k != TaskStateLabel {
				stored++
			}
		}
		if stored > s.Cfg.MaxLabelsPerMessage {
			return nil, E(413, "limit_exceeded", "at most %d labels per message", s.Cfg.MaxLabelsPerMessage)
		}
		if maps.Equal(before, after) {
			out = m
			return &Result{}, nil
		}
		// Clients never write abby.* keys (validated above): server labels
		// stay as stored, and the computed abby.task.state is never stored.
		if _, err := b.Exec(ctx, `DELETE FROM message_labels WHERE message_id=$1 AND key NOT LIKE 'abby.%'`, m.ID); err != nil {
			return nil, err
		}
		for k, v := range after {
			if strings.HasPrefix(k, "abby.") {
				continue
			}
			if _, err := b.Exec(ctx, `INSERT INTO message_labels(message_id, key, value) VALUES ($1,$2,$3)`, m.ID, k, v); err != nil {
				return nil, err
			}
		}
		m.Revision++
		if _, err := b.Exec(ctx, `UPDATE messages SET revision=$2 WHERE id=$1`, m.ID, m.Revision); err != nil {
			return nil, err
		}
		t, err := loadThread(ctx, b, boardID, m.ThreadID, b.chans, false)
		if err != nil {
			return nil, err
		}
		rootBefore, rootAfter := t.Labels, t.Labels
		if m.IsRoot {
			rootBefore = before
			rootAfter = after
		}
		bm := m
		bm.Labels = before
		m.Labels = after
		if _, err := b.emit(PendingEvent{Type: "message.labels_changed", ChannelID: m.ChannelID, ThreadID: m.ThreadID, ResourceType: "message", ResourceID: m.ID,
			Revision: m.Revision, Payload: map[string]any{"message": m, "labels_before": before, "labels_after": after},
			Facets: []events.Facet{bm.facet(rootBefore), m.facet(rootAfter)}}); err != nil {
			return nil, err
		}
		if m.IsRoot {
			// Thread-scoped view invalidation: root labels classify the whole
			// thread, so consumers must refresh or drop its cached messages.
			if _, err := b.Exec(ctx, `UPDATE threads SET revision=revision+1, updated_at=$2 WHERE id=$1`, t.ID, b.now); err != nil {
				return nil, err
			}
			t.Revision++
			t.Labels = after
			t.UpdatedAt = b.now
			if _, err := b.emit(PendingEvent{Type: "thread.view_invalidated", ChannelID: m.ChannelID, ThreadID: t.ID, ResourceType: "thread", ResourceID: t.ID,
				Revision: t.Revision, Payload: map[string]any{"thread": t, "reason": "root_labels_changed", "labels_before": before, "labels_after": after},
				Facets: []events.Facet{
					{ThreadScope: true, ChannelPath: m.ChannelPath, ThreadID: t.ID, RootLabels: before},
					{ThreadScope: true, ChannelPath: m.ChannelPath, ThreadID: t.ID, RootLabels: after},
				}}); err != nil {
				return nil, err
			}
		}
		out = m
		return &Result{}, nil
	})
	return &out, err
}

type PatchThreadRequest struct {
	Title *string `json:"title"`
}

// PatchThread edits thread metadata. Requires read and post, plus either
// root authorship or channel_manage, under a thread revision precondition.
func (s *Service) PatchThread(ctx context.Context, p *Principal, boardID, threadID string, ifMatch int64, req PatchThreadRequest) (*ThreadView, error) {
	if req.Title == nil {
		return nil, Validation("title is required")
	}
	if err := validName(*req.Title, s.Cfg.MaxTitleBytes); err != nil {
		return nil, err
	}
	var out ThreadView
	_, err := s.boardMutation(ctx, p, boardID, nil, func(b *btx) (*Result, error) {
		t, err := loadThread(ctx, b, boardID, threadID, b.chans, true)
		if err != nil {
			return nil, err
		}
		if !b.policy.Has(t.ChannelPath, authz.Read) {
			return nil, NotFound("thread")
		}
		if t.TaskID != nil {
			e := E(409, "task_root_protected", "the title of a task thread changes only through PATCH .../tasks/{id}")
			e.Extra = map[string]any{"task_id": *t.TaskID}
			return nil, e
		}
		if !b.policy.Has(t.ChannelPath, authz.Post) || (t.CreatedBy != p.ActorID && !b.policy.Has(t.ChannelPath, authz.ChannelManage)) {
			return nil, Forbidden("thread edits require post and root authorship or channel_manage")
		}
		if t.Revision != ifMatch {
			return nil, E(412, "revision_conflict", "thread revision is %d", t.Revision)
		}
		t.Title, t.Revision, t.UpdatedAt = *req.Title, t.Revision+1, b.now
		if _, err := b.Exec(ctx, `UPDATE threads SET title=$2, revision=$3, updated_at=$4 WHERE id=$1`, t.ID, t.Title, t.Revision, b.now); err != nil {
			return nil, err
		}
		if _, err := b.emit(PendingEvent{Type: "thread.updated", ChannelID: t.ChannelID, ThreadID: t.ID, ResourceType: "thread", ResourceID: t.ID, Revision: t.Revision,
			Payload: map[string]any{"thread": t}, Facets: []events.Facet{{ThreadScope: true, ChannelPath: t.ChannelPath, ThreadID: t.ID, RootLabels: t.Labels}}}); err != nil {
			return nil, err
		}
		out = t
		return &Result{}, nil
	})
	return &out, err
}
