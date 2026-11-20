package domain

// P2 task model: storage rows, the effective view (ADR-0004), per-reader
// projection of dependencies (DEP-009/010), lapse materialization, and task
// event emission (spec/p2-tasks.md §15).

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dustywusty/yak/internal/authz"
	"github.com/dustywusty/yak/internal/events"
)

// TaskStateLabel is the computed root label of task threads (TASK-027).
const TaskStateLabel = "yak.task.state"

// BriefRevisionLabel is the server label on brief-update notices (BRIEF-003).
const BriefRevisionLabel = "yak.task.brief_revision"

const (
	StateWaiting   = "waiting"
	StateReady     = "ready"
	StateClaimed   = "claimed"
	StateBlocked   = "blocked"
	StateCompleted = "completed"
	StateFailed    = "failed"
	StateCanceled  = "canceled"
)

// TaskStates is the closed state set of protocol v1 (TASK-020).
var TaskStates = []string{StateWaiting, StateReady, StateClaimed, StateBlocked, StateCompleted, StateFailed, StateCanceled}

func nonterminal(s string) bool {
	return s == StateWaiting || s == StateReady || s == StateClaimed || s == StateBlocked
}

// ---- views ----

// ClaimView is a claim without its proof (ADR-0016 §4).
type ClaimView struct {
	ID            string     `json:"id"`
	TaskID        string     `json:"task_id"`
	ActorID       string     `json:"actor_id"`
	ExecutionID   string     `json:"execution_id"`
	RunID         *string    `json:"run_id"`
	Epoch         int64      `json:"epoch"`
	BriefRevision int        `json:"brief_revision"`
	Status        string     `json:"status"`
	Revision      int64      `json:"revision"`
	LeaseSeconds  int        `json:"lease_seconds"`
	AcquiredAt    time.Time  `json:"acquired_at"`
	ExpiresAt     time.Time  `json:"expires_at"`
	EndedAt       *time.Time `json:"ended_at"`
	EndRecordedAt *time.Time `json:"end_recorded_at"`
}

// DependencyView is one prerequisite. ChannelID is set only in stored
// copies (event payloads, snapshot items) and is removed by the per-reader
// projection, which also drops unreadable prerequisites (DEP-009/010).
type DependencyView struct {
	TaskID    string `json:"task_id"`
	State     string `json:"state"`
	ChannelID string `json:"channel_id,omitempty"`
}

// TaskView is the effective task view (TASK-021, TASK-025).
type TaskView struct {
	ID                            string           `json:"id"`
	BoardID                       string           `json:"board_id"`
	ThreadID                      string           `json:"thread_id"`
	ChannelID                     string           `json:"channel_id"`
	ChannelPath                   string           `json:"channel_path"`
	Title                         string           `json:"title"`
	OwnerActorID                  string           `json:"owner_actor_id"`
	AssigneeActorID               *string          `json:"assignee_actor_id"`
	CreatedByActorID              string           `json:"created_by_actor_id"`
	Origin                        string           `json:"origin"`
	State                         string           `json:"state"`
	Revision                      int64            `json:"revision"`
	BriefRevision                 int              `json:"brief_revision"`
	Epoch                         int64            `json:"epoch"`
	RetryCount                    int              `json:"retry_count"`
	Dependencies                  []DependencyView `json:"dependencies"`
	UnreadableDependenciesPending bool             `json:"unreadable_dependencies_pending"`
	CurrentClaim                  *ClaimView       `json:"current_claim"`
	LastClaim                     *ClaimView       `json:"last_claim"`
	Block                         json.RawMessage  `json:"block"`
	Outcome                       json.RawMessage  `json:"outcome"`
	RootRedacted                  bool             `json:"root_redacted"`
	CreatedAt                     time.Time        `json:"created_at"`
	UpdatedAt                     time.Time        `json:"updated_at"`
	StateChangedAt                time.Time        `json:"state_changed_at"`
}

// BlockInfo is the task's block record (spec §19.1).
type BlockInfo struct {
	Reason    string    `json:"reason"`
	Cause     string    `json:"cause"`
	ByActorID string    `json:"by_actor_id"`
	At        time.Time `json:"at"`
}

// Evidence is one item of a completion or failure record (CLAIM-031).
type Evidence struct {
	Kind string  `json:"kind"`
	Ref  string  `json:"ref"`
	Note *string `json:"note,omitempty"`
}

// Outcome is the settlement record of a terminal task (spec §19.1).
type Outcome struct {
	Kind        string         `json:"kind"`
	Reason      *string        `json:"reason"`
	Summary     *string        `json:"summary"`
	Evidence    []Evidence     `json:"evidence"`
	Data        map[string]any `json:"data"`
	ByActorID   string         `json:"by_actor_id"`
	ClaimID     *string        `json:"claim_id"`
	ExecutionID *string        `json:"execution_id"`
	At          time.Time      `json:"at"`
}

// ---- storage rows ----

type taskRow struct {
	ID, BoardID, ThreadID, ChannelID, Owner string
	Assignee                                *string
	CreatedBy, Origin                       string
	BatchID                                 *string
	State                                   string
	Revision, Epoch                         int64
	RetryCount, BriefRevision               int
	CurrentClaimID                          *string
	ClaimExpiresAt                          *time.Time
	CreatedPosition                         int64
	Block, Outcome                          []byte
	RootRedactedAt                          *time.Time
	CreatedAt, UpdatedAt, StateChangedAt    time.Time
	Title, RootMessageID                    string
}

const taskCols = `tk.id, tk.board_id, tk.thread_id, tk.channel_id, tk.owner_actor_id, tk.assignee_actor_id, tk.created_by, tk.origin, tk.batch_id,
	tk.state, tk.revision, tk.epoch, tk.retry_count, tk.brief_revision, tk.current_claim_id, tk.claim_expires_at, tk.created_position,
	tk.block, tk.outcome, tk.root_redacted_at, tk.created_at, tk.updated_at, tk.state_changed_at, th.title, th.root_message_id`

const taskFrom = ` FROM tasks tk JOIN threads th ON th.id = tk.thread_id `

func scanTask(r pgx.Row) (taskRow, error) {
	var t taskRow
	err := r.Scan(&t.ID, &t.BoardID, &t.ThreadID, &t.ChannelID, &t.Owner, &t.Assignee, &t.CreatedBy, &t.Origin, &t.BatchID,
		&t.State, &t.Revision, &t.Epoch, &t.RetryCount, &t.BriefRevision, &t.CurrentClaimID, &t.ClaimExpiresAt, &t.CreatedPosition,
		&t.Block, &t.Outcome, &t.RootRedactedAt, &t.CreatedAt, &t.UpdatedAt, &t.StateChangedAt, &t.Title, &t.RootMessageID)
	return t, err
}

func loadTaskRow(ctx context.Context, q querier, boardID, id string, lock bool) (taskRow, error) {
	sql := `SELECT ` + taskCols + taskFrom + `WHERE tk.board_id=$1 AND tk.id=$2`
	if lock {
		sql += ` FOR UPDATE OF tk`
	}
	t, err := scanTask(q.QueryRow(ctx, sql, boardID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return t, NotFound("task")
	}
	return t, err
}

// lapsed reports whether the recorded claim's lease is over at now.
func (t *taskRow) lapsed(now time.Time) bool {
	return t.State == StateClaimed && t.ClaimExpiresAt != nil && !now.Before(*t.ClaimExpiresAt)
}

// effective returns the CLAIM-051 projection of the row at now. It is
// deterministic: it depends only on the row and expires_at, and it equals
// what materialization writes.
func (t taskRow) effective(now time.Time) taskRow {
	if !t.lapsed(now) {
		return t
	}
	exp := *t.ClaimExpiresAt
	t.State = StateReady
	t.Revision++
	t.UpdatedAt, t.StateChangedAt = exp, exp
	t.CurrentClaimID, t.ClaimExpiresAt = nil, nil
	return t
}

type claimRow struct {
	ClaimView
	BoardID, CredentialID string
	Verifier, Ciphertext  []byte
	KeyID                 *string
}

const claimCols = `id, task_id, actor_id, execution_id, run_id, epoch, brief_revision, status, revision, lease_seconds, acquired_at, expires_at,
	ended_at, end_recorded_at, board_id, credential_id, proof_verifier, proof_ciphertext, proof_key_id`

func scanClaim(r pgx.Row) (claimRow, error) {
	var c claimRow
	err := r.Scan(&c.ID, &c.TaskID, &c.ActorID, &c.ExecutionID, &c.RunID, &c.Epoch, &c.BriefRevision, &c.Status, &c.Revision, &c.LeaseSeconds,
		&c.AcquiredAt, &c.ExpiresAt, &c.EndedAt, &c.EndRecordedAt, &c.BoardID, &c.CredentialID, &c.Verifier, &c.Ciphertext, &c.KeyID)
	return c, err
}

// effective projects a lapsed active claim as expired (CLAIM-051).
func (c claimRow) effective(now time.Time) ClaimView {
	v := c.ClaimView
	if v.Status == "active" && !now.Before(v.ExpiresAt) {
		exp := v.ExpiresAt
		v.Status, v.EndedAt, v.EndRecordedAt = "expired", &exp, nil
	}
	return v
}

func (c claimRow) activeAt(now time.Time) bool {
	return c.Status == "active" && now.Before(c.ExpiresAt)
}

func loadClaim(ctx context.Context, q querier, id string) (claimRow, error) {
	c, err := scanClaim(q.QueryRow(ctx, `SELECT `+claimCols+` FROM claims WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, NotFound("claim")
	}
	return c, err
}

// ---- building views ----

// readFn reports whether the viewer can read a channel ID; nil builds the
// stored (unprojected) copy used in events and snapshots.
type readFn func(channelID string) bool

func chanByID(chans map[string]channelRow, id string) channelRow {
	for _, c := range chans {
		if c.ID == id {
			return c
		}
	}
	return channelRow{}
}

// buildTaskView returns the effective view of a recorded task row at now.
// deps, if non-nil, replaces the stored dependency list (batch creation,
// whose edges are inserted after every task of the batch exists).
func buildTaskView(ctx context.Context, q querier, row taskRow, chans map[string]channelRow, can readFn, now time.Time, deps []DependencyView) (TaskView, error) {
	eff := row.effective(now)
	v := TaskView{ID: eff.ID, BoardID: eff.BoardID, ThreadID: eff.ThreadID, ChannelID: eff.ChannelID, ChannelPath: chanByID(chans, eff.ChannelID).Path,
		Title: eff.Title, OwnerActorID: eff.Owner, AssigneeActorID: eff.Assignee, CreatedByActorID: eff.CreatedBy, Origin: eff.Origin,
		State: eff.State, Revision: eff.Revision, BriefRevision: eff.BriefRevision, Epoch: eff.Epoch, RetryCount: eff.RetryCount,
		Block: nullJSON(eff.Block), Outcome: nullJSON(eff.Outcome), RootRedacted: eff.RootRedactedAt != nil,
		CreatedAt: eff.CreatedAt, UpdatedAt: eff.UpdatedAt, StateChangedAt: eff.StateChangedAt}
	rows, err := q.Query(ctx, `SELECT `+claimCols+` FROM claims WHERE task_id=$1 ORDER BY epoch DESC LIMIT 2`, row.ID)
	if err != nil {
		return v, err
	}
	claims, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (claimRow, error) { return scanClaim(r) })
	if err != nil {
		return v, err
	}
	if eff.State == StateClaimed && len(claims) > 0 {
		cur := claims[0].ClaimView
		v.CurrentClaim = &cur
		if len(claims) > 1 {
			last := claims[1].effective(now)
			v.LastClaim = &last
		}
	} else if len(claims) > 0 {
		last := claims[0].effective(now)
		v.LastClaim = &last
	}
	if deps == nil {
		drows, err := q.Query(ctx, `SELECT p.id, p.state, p.claim_expires_at, p.channel_id FROM task_dependencies d JOIN tasks p ON p.id = d.depends_on_task_id
			WHERE d.task_id=$1 ORDER BY p.created_position`, row.ID)
		if err != nil {
			return v, err
		}
		deps = []DependencyView{}
		for drows.Next() {
			var d DependencyView
			var exp *time.Time
			if err := drows.Scan(&d.TaskID, &d.State, &exp, &d.ChannelID); err != nil {
				drows.Close()
				return v, err
			}
			if d.State == StateClaimed && exp != nil && !now.Before(*exp) {
				d.State = StateReady
			}
			deps = append(deps, d)
		}
		drows.Close()
		if err := drows.Err(); err != nil {
			return v, err
		}
	}
	v.Dependencies = deps
	if can != nil {
		projectTaskView(&v, can)
	}
	return v, nil
}

func nullJSON(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("null")
	}
	return json.RawMessage(b)
}

// projectTaskView applies DEP-009 for one reader: only readable
// prerequisites, no count of others, and the derivable pending bit.
func projectTaskView(v *TaskView, can readFn) {
	out := []DependencyView{}
	allDone := true
	for _, d := range v.Dependencies {
		if d.ChannelID != "" && !can(d.ChannelID) {
			continue
		}
		if d.State != StateCompleted {
			allDone = false
		}
		out = append(out, DependencyView{TaskID: d.TaskID, State: d.State})
	}
	v.Dependencies = out
	v.UnreadableDependenciesPending = v.State == StateWaiting && allDone
}

// projectTaskJSON applies the per-reader projection to a stored task-event
// payload or snapshot item (DEP-010, TASK-092).
func projectTaskJSON(raw json.RawMessage, can readFn) json.RawMessage {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw
	}
	projectTask := func(b json.RawMessage) json.RawMessage {
		var v TaskView
		if err := json.Unmarshal(b, &v); err != nil {
			return b
		}
		projectTaskView(&v, can)
		return jsonOf(v)
	}
	if _, isTask := m["dependencies"]; isTask {
		return projectTask(raw)
	}
	if t, ok := m["task"]; ok {
		m["task"] = projectTask(t)
	}
	if ch, ok := m["prerequisite_channel_id"]; ok {
		var chID string
		_ = json.Unmarshal(ch, &chID)
		if !can(chID) {
			delete(m, "prerequisite_task_id")
		}
		delete(m, "prerequisite_channel_id")
	}
	for _, k := range []string{"added", "removed"} {
		if lst, ok := m[k]; ok {
			var refs []DependencyView
			if err := json.Unmarshal(lst, &refs); err == nil {
				ids := []string{}
				for _, r := range refs {
					if r.ChannelID == "" || can(r.ChannelID) {
						ids = append(ids, r.TaskID)
					}
				}
				m[k] = jsonOf(ids)
			}
		}
	}
	return jsonOf(m)
}

// ---- inside a board transaction ----

func (b *btx) chanOf(t *taskRow) channelRow { return chanByID(b.chans, t.ChannelID) }

func (b *btx) canReadChan(id string) bool {
	c := chanByID(b.chans, id)
	return c.ID != "" && b.policy.Has(c.Path, authz.Read)
}

// lockTask loads and locks a task the caller can read; otherwise 404
// (TASK-040). It then applies a lapsed lease (TASK-031).
func (b *btx) lockTask(id string) (*taskRow, error) {
	t, err := loadTaskRow(b.ctx, b, b.board.ID, id, true)
	if err != nil {
		return nil, err
	}
	if !b.policy.Has(b.chanOf(&t).Path, authz.Read) {
		return nil, NotFound("task")
	}
	if err := b.materialize(&t); err != nil {
		return nil, err
	}
	return &t, nil
}

// isManager: owner with task_manage, or task_admin; both with read (§1).
func (b *btx) isManager(t *taskRow) bool {
	path := b.chanOf(t).Path
	if !b.policy.Has(path, authz.Read) {
		return false
	}
	return b.policy.Has(path, authz.TaskAdmin) || (t.Owner == b.p.ActorID && b.policy.Has(path, authz.TaskManage))
}

func (b *btx) requireManager(t *taskRow) error {
	if !b.isManager(t) {
		return forbiddenReason("missing_capability", "this operation requires the task owner with task_manage, or task_admin")
	}
	return nil
}

// storedRootLabels returns the stored root labels (without the computed
// yak.task.state).
func (b *btx) storedRootLabels(rootMessageID string) (map[string]string, error) {
	var m map[string]string
	err := b.QueryRow(b.ctx, `SELECT COALESCE(jsonb_object_agg(key, value), '{}'::jsonb) FROM message_labels WHERE message_id=$1`, rootMessageID).Scan(&m)
	return m, err
}

// saveTask writes every mutable column of the row.
func (b *btx) saveTask(t *taskRow) error {
	_, err := b.Exec(b.ctx, `UPDATE tasks SET owner_actor_id=$2, assignee_actor_id=$3, state=$4, revision=$5, epoch=$6, retry_count=$7,
		brief_revision=$8, current_claim_id=$9, claim_expires_at=$10, block=$11, outcome=$12, root_redacted_at=$13, updated_at=$14, state_changed_at=$15
		WHERE id=$1`, t.ID, t.Owner, t.Assignee, t.State, t.Revision, t.Epoch, t.RetryCount, t.BriefRevision, t.CurrentClaimID, t.ClaimExpiresAt,
		rawOrNil(t.Block), rawOrNil(t.Outcome), t.RootRedactedAt, t.UpdatedAt, t.StateChangedAt)
	return err
}

func rawOrNil(b []byte) any {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	return b
}

// touch records a task change at b.now (revision +1).
func (b *btx) touch(t *taskRow, stateChanged bool) {
	t.Revision++
	t.UpdatedAt = b.now
	if stateChanged {
		t.StateChangedAt = b.now
	}
}

// storedView is the unprojected view for event payloads.
func (b *btx) storedView(t *taskRow, deps []DependencyView) (TaskView, error) {
	return buildTaskView(b.ctx, b, *t, b.chans, nil, b.now, deps)
}

// readerView is the caller's projection.
func (b *btx) readerView(t *taskRow) (TaskView, error) {
	return buildTaskView(b.ctx, b, *t, b.chans, b.canReadChan, b.now, nil)
}

type taskEvent struct {
	Type        string
	StateBefore string // "" = no task before (creation)
	Extra       map[string]any
	TVI         bool // emit thread.view_invalidated if the state changed
	EffectiveAt time.Time
	Deps        []DependencyView // see buildTaskView
}

// emitTask appends a task event (resource = task at its new revision) and,
// if the state changed, thread.view_invalidated (TASK-082..085).
func (b *btx) emitTask(t *taskRow, ev taskEvent) error {
	stored, err := b.storedRootLabels(t.RootMessageID)
	if err != nil {
		return err
	}
	before, after := maps.Clone(stored), maps.Clone(stored)
	if ev.StateBefore != "" {
		before[TaskStateLabel] = ev.StateBefore
	}
	after[TaskStateLabel] = t.State
	view, err := b.storedView(t, ev.Deps)
	if err != nil {
		return err
	}
	payload := map[string]any{"task": view}
	maps.Copy(payload, ev.Extra)
	path := b.chanOf(t).Path
	facets := []events.Facet{
		{ThreadScope: true, ChannelPath: path, ThreadID: t.ThreadID, RootLabels: before},
		{ThreadScope: true, ChannelPath: path, ThreadID: t.ThreadID, RootLabels: after},
	}
	if _, err := b.emit(PendingEvent{Type: ev.Type, ChannelID: t.ChannelID, ThreadID: t.ThreadID, ResourceType: "task", ResourceID: t.ID,
		Revision: t.Revision, Payload: payload, Facets: facets, EffectiveAt: ev.EffectiveAt}); err != nil {
		return err
	}
	if ev.TVI && ev.StateBefore != t.State {
		th, err := loadThread(b.ctx, b, b.board.ID, t.ThreadID, b.chans, false)
		if err != nil {
			return err
		}
		th.Labels = after
		if _, err := b.emit(PendingEvent{Type: "thread.view_invalidated", ChannelID: t.ChannelID, ThreadID: t.ThreadID, ResourceType: "thread", ResourceID: t.ThreadID,
			Revision: th.Revision, Payload: map[string]any{"thread": th, "reason": "task_state_changed", "labels_before": before, "labels_after": after},
			Facets: facets}); err != nil {
			return err
		}
	}
	return nil
}

// endClaim ends the current claim with status (released, completed,
// failed, revoked) at b.now and destroys its replay material (CLAIM-030,
// CLAIM-043). It returns the ended claim view, or nil if there was none.
func (b *btx) endClaim(t *taskRow, status string) (*ClaimView, error) {
	if t.CurrentClaimID == nil {
		return nil, nil
	}
	c, err := scanClaim(b.QueryRow(b.ctx, `UPDATE claims SET status=$2, ended_at=$3, end_recorded_at=$3, proof_ciphertext=NULL, proof_key_id=NULL
		WHERE id=$1 RETURNING `+claimCols, *t.CurrentClaimID, status, b.now))
	if err != nil {
		return nil, err
	}
	t.CurrentClaimID, t.ClaimExpiresAt = nil, nil
	v := c.ClaimView
	return &v, nil
}

// materialize records a lapsed lease exactly as the effective view shows it
// and emits task.claim_expired (effective_at = expires_at) and the view
// invalidation (CLAIM-052). At most once: afterwards the row is not lapsed.
func (b *btx) materialize(t *taskRow) error {
	if !t.lapsed(b.now) {
		return nil
	}
	exp := *t.ClaimExpiresAt
	c, err := scanClaim(b.QueryRow(b.ctx, `UPDATE claims SET status='expired', ended_at=expires_at, end_recorded_at=$2, proof_ciphertext=NULL, proof_key_id=NULL
		WHERE id=$1 RETURNING `+claimCols, *t.CurrentClaimID, b.now))
	if err != nil {
		return err
	}
	*t = t.effective(b.now)
	if err := b.saveTask(t); err != nil {
		return err
	}
	return b.emitTask(t, taskEvent{Type: "task.claim_expired", StateBefore: StateClaimed, TVI: true, EffectiveAt: exp,
		Extra: map[string]any{"claim": c.ClaimView}})
}

// prereqsDone reports whether every prerequisite is completed (completed
// is terminal, so no lapse projection is needed).
func (b *btx) prereqsDone(taskID string) (bool, error) {
	var ok bool
	err := b.QueryRow(b.ctx, `SELECT COALESCE(bool_and(p.state = 'completed'), true) FROM task_dependencies d JOIN tasks p ON p.id = d.depends_on_task_id
		WHERE d.task_id=$1`, taskID).Scan(&ok)
	return ok, err
}

// ---- errors ----

func forbiddenReason(reason, format string, a ...any) *Error {
	e := Forbidden(format, a...)
	e.Extra = map[string]any{"reason": reason}
	return e
}

func invalidTransition(state, hint, format string, a ...any) *Error {
	e := E(409, "invalid_transition", format, a...)
	e.Extra = map[string]any{"current_state": state}
	if hint != "" {
		e.Extra["hint"] = hint
	}
	return e
}

func claimLost(t *taskRow) *Error {
	e := E(409, "claim_lost", "the proof is not the active claim of this task")
	e.Extra = map[string]any{"state": t.State, "epoch": t.Epoch}
	return e
}

func revisionConflict(rev int64, what string) *Error {
	e := E(412, "revision_conflict", "%s revision is %d", what, rev)
	e.Extra = map[string]any{"revision": rev}
	return e
}

func limitExceeded(limit int, format string, a ...any) *Error {
	e := E(413, "limit_exceeded", format, a...)
	e.Extra = map[string]any{"limit": limit}
	return e
}
