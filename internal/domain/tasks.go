package domain

// P2 task creation (L1, L2, batches) and task reads (spec/p2-tasks.md §3,
// §4, §9, §11, §12 BRIEF-001).

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dustywusty/yak/internal/authz"
	"github.com/dustywusty/yak/internal/events"
	"github.com/dustywusty/yak/internal/ids"
)

// TaskThreadSpec is the new thread of a task (ADR-0016 §3).
type TaskThreadSpec struct {
	Channel string            `json:"channel"`
	Title   string            `json:"title"`
	Text    string            `json:"text"`
	Labels  map[string]string `json:"labels,omitempty"`
}

// CreateTaskRequest has exactly one of Thread (L1) or ThreadID (L2).
type CreateTaskRequest struct {
	Thread          *TaskThreadSpec `json:"thread,omitempty"`
	ThreadID        string          `json:"thread_id,omitempty"`
	AssigneeActorID *string         `json:"assignee_actor_id"`
	DependsOn       []string        `json:"depends_on"`
}

type BatchDependency struct {
	Ref    string `json:"ref,omitempty"`
	TaskID string `json:"task_id,omitempty"`
}

type BatchTaskItem struct {
	Ref             string            `json:"ref"`
	Thread          *TaskThreadSpec   `json:"thread"`
	AssigneeActorID *string           `json:"assignee_actor_id"`
	DependsOn       []BatchDependency `json:"depends_on"`
}

type CreateTaskBatchRequest struct {
	Tasks []BatchTaskItem `json:"tasks"`
}

type BatchEntry struct {
	Ref  string   `json:"ref"`
	Task TaskView `json:"task"`
}

type TaskBatchView struct {
	BatchID string       `json:"batch_id"`
	Tasks   []BatchEntry `json:"tasks"`
}

// BatchError is one entry of a rejected batch's problem document (BATCH-003).
type BatchError struct {
	Index  int    `json:"index"`
	Ref    string `json:"ref"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

func (s *Service) validateTaskThread(t *TaskThreadSpec) error {
	if err := authz.ValidatePath(t.Channel); err != nil {
		return Validation("thread.channel: %s", err)
	}
	if err := validName(t.Title, s.Cfg.MaxTitleBytes); err != nil {
		return Validation("thread.title must be 1..%d bytes of UTF-8", s.Cfg.MaxTitleBytes)
	}
	if err := s.validateText(t.Text); err != nil {
		return err
	}
	if strings.TrimSpace(t.Text) == "" {
		return Validation("thread.text (the brief) must not be empty")
	}
	labels, err := s.normalizeLabels(t.Labels)
	if err != nil {
		return err
	}
	t.Labels = labels
	return nil
}

func (s *Service) validateDeps(ids []string) ([]string, error) {
	out := sortedUnique(ids)
	if out == nil {
		out = []string{}
	}
	if len(out) > s.Cfg.MaxDependenciesPerTask {
		return nil, limitExceeded(s.Cfg.MaxDependenciesPerTask, "at most %d prerequisites per task", s.Cfg.MaxDependenciesPerTask)
	}
	for _, id := range out {
		if id == "" {
			return nil, Validation("prerequisite task IDs must not be empty")
		}
	}
	return out, nil
}

// CreateTask implements L1 (new thread) and L2 (convert a thread).
func (s *Service) CreateTask(ctx context.Context, p *Principal, boardID string, idem *Idem, req CreateTaskRequest) (*Result, error) {
	if (req.Thread == nil) == (req.ThreadID == "") {
		return nil, Validation("exactly one of thread or thread_id is required")
	}
	if req.Thread != nil {
		if err := s.validateTaskThread(req.Thread); err != nil {
			return nil, err
		}
	}
	deps, err := s.validateDeps(req.DependsOn)
	if err != nil {
		return nil, err
	}
	req.DependsOn = deps
	if req.AssigneeActorID != nil && *req.AssigneeActorID == "" {
		return nil, Validation("assignee_actor_id must be an actor ID or null")
	}
	idem.Request = req
	return s.boardMutation(ctx, p, boardID, idem, func(b *btx) (*Result, error) {
		var t *taskRow
		var err error
		if req.Thread != nil {
			t, err = b.createWithThread(*req.Thread, req.AssigneeActorID, req.DependsOn, "new_thread", nil)
		} else {
			t, err = b.convertThread(req.ThreadID, req.AssigneeActorID, req.DependsOn)
		}
		if err != nil {
			return nil, err
		}
		v, err := b.readerView(t)
		if err != nil {
			return nil, err
		}
		return &Result{Status: 201, ResourceType: "task", ResourceID: t.ID, Body: v}, nil
	})
}

// taskChannel authorizes L1 on a channel path: read (else 404), post and
// task_manage (else 403); a missing channel is created with channel_manage
// (CHAN-002).
func (b *btx) taskChannel(path string) (channelRow, error) {
	if !b.policy.Has(path, authz.Read) {
		return channelRow{}, E(404, "not_found", "channel %q not found", path)
	}
	if !b.policy.Has(path, authz.Post) || !b.policy.Has(path, authz.TaskManage) {
		return channelRow{}, forbiddenReason("missing_capability", "creating a task requires post and task_manage in %q", path)
	}
	ch, ok := b.chans[path]
	if ok {
		return ch, nil
	}
	if !b.policy.Has(path, authz.ChannelManage) {
		return channelRow{}, E(404, "not_found", "channel %q does not exist and caller may not create it", path)
	}
	return b.createChannel(path, "inherited")
}

// checkAssignee enforces TASK-051: an actor that can currently read path.
func (b *btx) checkAssignee(assignee *string, path string) error {
	if assignee == nil {
		return nil
	}
	ok, err := b.actorCanRead(*assignee, path)
	if err != nil {
		return err
	}
	if !ok {
		return Validation("assignee_actor_id %q is not an actor who can read this channel", *assignee)
	}
	return nil
}

func (b *btx) actorCanRead(actorID, path string) (bool, error) {
	var exists bool
	if err := b.QueryRow(b.ctx, `SELECT EXISTS(SELECT 1 FROM actors WHERE id=$1 AND disabled_at IS NULL)`, actorID).Scan(&exists); err != nil || !exists {
		return false, err
	}
	other := &Principal{ActorID: actorID, Scopes: []string{authz.AllScopes}}
	pol, _, err := b.s.loadPolicy(b.ctx, b.Tx, other, &b.board)
	if err != nil {
		return false, err
	}
	return pol.Has(path, authz.Read), nil
}

// resolvePrereqs loads prerequisites that must exist on this board and be
// readable; otherwise 404 with the offending task_id (DEP-001, DEP-003).
func (b *btx) resolvePrereqs(ids []string) ([]DependencyView, error) {
	out := make([]DependencyView, 0, len(ids))
	for _, id := range ids {
		var d DependencyView
		var exp *time.Time
		err := b.QueryRow(b.ctx, `SELECT id, state, claim_expires_at, channel_id FROM tasks WHERE board_id=$1 AND id=$2`, b.board.ID, id).Scan(&d.TaskID, &d.State, &exp, &d.ChannelID)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !b.canReadChan(d.ChannelID)) {
			e := E(404, "not_found", "prerequisite task %q not found", id)
			e.Extra = map[string]any{"task_id": id}
			return nil, e
		}
		if err != nil {
			return nil, err
		}
		if d.State == StateClaimed && exp != nil && !b.now.Before(*exp) {
			d.State = StateReady
		}
		out = append(out, d)
	}
	return out, nil
}

func initialState(deps []DependencyView) string {
	for _, d := range deps {
		if d.State != StateCompleted {
			return StateWaiting
		}
	}
	return StateReady
}

// insertTask writes the task row and brief revision 1 (TASK-014). Its
// created_position is the position of the task.created event emitted next.
func (b *btx) insertTask(id string, th ThreadView, root MessageView, origin string, batchID *string, assignee *string, state string) (*taskRow, error) {
	t := &taskRow{ID: id, BoardID: b.board.ID, ThreadID: th.ID, ChannelID: th.ChannelID, Owner: b.p.ActorID, Assignee: assignee, CreatedBy: b.p.ActorID,
		Origin: origin, BatchID: batchID, State: state, Revision: 1, BriefRevision: 1, CreatedPosition: b.head + 1,
		CreatedAt: b.now, UpdatedAt: b.now, StateChangedAt: b.now, Title: th.Title, RootMessageID: th.RootMessageID}
	if _, err := b.Exec(b.ctx, `INSERT INTO tasks(id, board_id, thread_id, channel_id, owner_actor_id, assignee_actor_id, created_by, origin, batch_id,
		state, created_position, created_at, updated_at, state_changed_at) VALUES ($1,$2,$3,$4,$5,$6,$5,$7,$8,$9,$10,$11,$11,$11)`,
		t.ID, t.BoardID, t.ThreadID, t.ChannelID, t.Owner, t.Assignee, t.Origin, t.BatchID, t.State, t.CreatedPosition, b.now); err != nil {
		return nil, err
	}
	if _, err := b.Exec(b.ctx, `INSERT INTO task_briefs(board_id, task_id, revision, text, source_kind, source_message_id, source_message_revision, created_by, created_at)
		VALUES ($1,$2,1,$3,'root',$4,$5,$6,$7)`, t.BoardID, t.ID, root.Text, root.ID, root.Revision, b.p.ActorID, b.now); err != nil {
		return nil, err
	}
	return t, nil
}

func (b *btx) insertDeps(taskID string, deps []string) error {
	for _, d := range deps {
		if _, err := b.Exec(b.ctx, `INSERT INTO task_dependencies(board_id, task_id, depends_on_task_id, created_by, created_at) VALUES ($1,$2,$3,$4,$5)`,
			b.board.ID, taskID, d, b.p.ActorID, b.now); err != nil {
			return err
		}
	}
	return nil
}

// createWithThread is L1: thread.created, then task.created.
func (b *btx) createWithThread(spec TaskThreadSpec, assignee *string, depIDs []string, origin string, batchID *string) (*taskRow, error) {
	ch, err := b.taskChannel(spec.Channel)
	if err != nil {
		return nil, err
	}
	if err := b.checkAssignee(assignee, ch.Path); err != nil {
		return nil, err
	}
	deps, err := b.resolvePrereqs(depIDs)
	if err != nil {
		return nil, err
	}
	state := initialState(deps)
	taskID := ids.New("task")
	tw, err := b.insertThread(ch, CreateThreadRequest{Channel: ch.Path, Title: spec.Title, Text: spec.Text, Labels: spec.Labels}, taskID, state)
	if err != nil {
		return nil, err
	}
	t, err := b.insertTask(taskID, tw.Thread, tw.Root, origin, batchID, assignee, state)
	if err != nil {
		return nil, err
	}
	if err := b.insertDeps(t.ID, depIDs); err != nil {
		return nil, err
	}
	return t, b.emitTask(t, taskEvent{Type: "task.created", Extra: map[string]any{"origin": origin, "batch_id": batchID}})
}

// convertThread is L2 (TASK-011..013).
func (b *btx) convertThread(threadID string, assignee *string, depIDs []string) (*taskRow, error) {
	th, err := loadThread(b.ctx, b, b.board.ID, threadID, b.chans, true)
	if err != nil {
		return nil, err
	}
	path := th.ChannelPath
	if !b.policy.Has(path, authz.Read) {
		return nil, NotFound("thread")
	}
	if !b.policy.Has(path, authz.Post) {
		return nil, forbiddenReason("missing_capability", "converting a thread requires post in %q", path)
	}
	if !b.policy.Has(path, authz.TaskAdmin) && !(th.CreatedBy == b.p.ActorID && b.policy.Has(path, authz.TaskManage)) {
		return nil, forbiddenReason("missing_capability", "converting a thread requires root authorship with task_manage, or task_admin")
	}
	if th.TaskID != nil {
		e := E(409, "already_exists", "thread already has a task")
		e.Extra = map[string]any{"task_id": *th.TaskID}
		return nil, e
	}
	root, err := loadMessage(b.ctx, b, b.board.ID, th.RootMessageID, b.chans, true)
	if err != nil {
		return nil, err
	}
	if root.Deleted != nil {
		return nil, invalidTransition("", "", "the thread root is deleted; create a new task instead")
	}
	if err := b.checkAssignee(assignee, path); err != nil {
		return nil, err
	}
	deps, err := b.resolvePrereqs(depIDs)
	if err != nil {
		return nil, err
	}
	state := initialState(deps)
	t, err := b.insertTask(ids.New("task"), th, root, "converted", nil, assignee, state)
	if err != nil {
		return nil, err
	}
	if err := b.insertDeps(t.ID, depIDs); err != nil {
		return nil, err
	}
	return t, b.emitTask(t, taskEvent{Type: "task.created", TVI: true, Extra: map[string]any{"origin": "converted", "batch_id": nil}})
}

// ---- batches ----

var batchRefRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func batchReject(errs []BatchError, status int) error {
	first := errs[0]
	e := E(status, first.Code, "batch rejected: item %d (%s): %s", first.Index, first.Ref, first.Detail)
	e.Extra = map[string]any{"errors": errs}
	return e
}

func asBatchError(i int, ref string, err error) (BatchError, int, bool) {
	var de *Error
	if !errors.As(err, &de) {
		return BatchError{}, 0, false
	}
	return BatchError{Index: i, Ref: ref, Code: de.Code, Detail: de.Detail}, de.Status, true
}

// CreateTaskBatch creates 1..max_batch_tasks tasks atomically (BATCH-001..005).
func (s *Service) CreateTaskBatch(ctx context.Context, p *Principal, boardID string, idem *Idem, req CreateTaskBatchRequest) (*Result, error) {
	n := len(req.Tasks)
	if n == 0 {
		return nil, Validation("tasks must contain at least one item")
	}
	if n > s.Cfg.MaxBatchTasks {
		return nil, limitExceeded(s.Cfg.MaxBatchTasks, "a batch holds at most %d tasks", s.Cfg.MaxBatchTasks)
	}
	var errs []BatchError
	status := 0
	fail := func(i int, ref string, err error) {
		be, st, ok := asBatchError(i, ref, err)
		if !ok {
			be, st = BatchError{Index: i, Ref: ref, Code: "validation_failed", Detail: err.Error()}, 400
		}
		if status == 0 {
			status = st
		}
		errs = append(errs, be)
	}
	refIdx := map[string]int{}
	for i := range req.Tasks {
		it := &req.Tasks[i]
		if !batchRefRE.MatchString(it.Ref) {
			fail(i, it.Ref, Validation("ref must be 1..64 bytes of [A-Za-z0-9._-]"))
			continue
		}
		if _, dup := refIdx[it.Ref]; dup {
			fail(i, it.Ref, Validation("ref %q is not unique in the batch", it.Ref))
			continue
		}
		refIdx[it.Ref] = i
	}
	for i := range req.Tasks {
		it := &req.Tasks[i]
		if it.Thread == nil {
			fail(i, it.Ref, Validation("thread is required"))
			continue
		}
		if err := s.validateTaskThread(it.Thread); err != nil {
			fail(i, it.Ref, err)
			continue
		}
		if it.AssigneeActorID != nil && *it.AssigneeActorID == "" {
			fail(i, it.Ref, Validation("assignee_actor_id must be an actor ID or null"))
			continue
		}
		if len(it.DependsOn) > s.Cfg.MaxDependenciesPerTask {
			fail(i, it.Ref, limitExceeded(s.Cfg.MaxDependenciesPerTask, "at most %d prerequisites per task", s.Cfg.MaxDependenciesPerTask))
			continue
		}
		seen := map[BatchDependency]bool{}
		var clean []BatchDependency
		for _, d := range it.DependsOn {
			switch {
			case (d.Ref == "") == (d.TaskID == ""):
				fail(i, it.Ref, Validation("each depends_on entry needs exactly one of ref or task_id"))
			case d.Ref != "" && d.Ref == it.Ref:
				fail(i, it.Ref, E(409, "dependency_cycle", "item depends on itself"))
			case d.Ref != "":
				if _, ok := refIdx[d.Ref]; !ok {
					fail(i, it.Ref, Validation("depends_on ref %q is not an item of this batch", d.Ref))
				}
			}
			if !seen[d] {
				seen[d] = true
				clean = append(clean, d)
			}
		}
		sort.Slice(clean, func(a, b int) bool {
			if clean[a].Ref != clean[b].Ref {
				return clean[a].Ref < clean[b].Ref
			}
			return clean[a].TaskID < clean[b].TaskID
		})
		it.DependsOn = clean
	}
	if len(errs) == 0 {
		// Only batch items can form a cycle: no existing task can depend on
		// a task that does not exist yet (DEP-002).
		if cyc := refCycle(req.Tasks, refIdx); cyc != nil {
			i := refIdx[cyc[0]]
			e := E(409, "dependency_cycle", "batch items form a dependency cycle: %s", strings.Join(cyc, " -> "))
			fail(i, cyc[0], e)
		}
	}
	if len(errs) > 0 {
		return nil, batchReject(errs, status)
	}
	idem.Request = req
	return s.boardMutation(ctx, p, boardID, idem, func(b *btx) (*Result, error) {
		// Validate every authorization and reference before writing anything
		// (BATCH-003). Writes below can still fail, which rolls back all.
		var errs []BatchError
		status := 0
		for i, it := range req.Tasks {
			var err error
			path := it.Thread.Channel
			switch {
			case !b.policy.Has(path, authz.Read):
				err = E(404, "not_found", "channel %q not found", path)
			case !b.policy.Has(path, authz.Post) || !b.policy.Has(path, authz.TaskManage):
				err = forbiddenReason("missing_capability", "creating a task requires post and task_manage in %q", path)
			case b.chans[path].ID == "" && !b.policy.Has(path, authz.ChannelManage):
				err = E(404, "not_found", "channel %q does not exist and caller may not create it", path)
			}
			if err == nil {
				err = b.checkAssignee(it.AssigneeActorID, path)
			}
			if err == nil {
				for _, d := range it.DependsOn {
					if d.TaskID != "" {
						if _, err = b.resolvePrereqs([]string{d.TaskID}); err != nil {
							break
						}
					}
				}
			}
			if err != nil {
				be, st, ok := asBatchError(i, it.Ref, err)
				if !ok {
					return nil, err
				}
				if status == 0 {
					status = st
				}
				errs = append(errs, be)
			}
		}
		if len(errs) > 0 {
			return nil, batchReject(errs, status)
		}
		batchID := ids.New("batch")
		if _, err := b.Exec(b.ctx, `INSERT INTO task_batches(id, board_id, created_by, items, created_at) VALUES ($1,$2,$3,'[]',$4)`,
			batchID, b.board.ID, b.p.ActorID, b.now); err != nil {
			return nil, err
		}
		taskIDs := make([]string, len(req.Tasks))
		states := make([]string, len(req.Tasks))
		chanIDs := make([]string, len(req.Tasks))
		existing := make([][]DependencyView, len(req.Tasks))
		chs := make([]channelRow, len(req.Tasks))
		for i, it := range req.Tasks {
			taskIDs[i] = ids.New("task")
			// Missing channels are created first (channel.created), so the
			// thread.created/task.created pairs stay in request order.
			ch, err := b.taskChannel(it.Thread.Channel)
			if err != nil {
				return nil, err
			}
			chs[i], chanIDs[i] = ch, ch.ID
		}
		for i, it := range req.Tasks {
			states[i] = StateReady
			for _, d := range it.DependsOn {
				if d.Ref != "" {
					states[i] = StateWaiting
					continue
				}
				dv, err := b.resolvePrereqs([]string{d.TaskID})
				if err != nil {
					return nil, err
				}
				existing[i] = append(existing[i], dv[0])
				if dv[0].State != StateCompleted {
					states[i] = StateWaiting
				}
			}
		}
		rows := make([]*taskRow, len(req.Tasks))
		for i, it := range req.Tasks {
			ch := chs[i]
			tw, err := b.insertThread(ch, CreateThreadRequest{Channel: ch.Path, Title: it.Thread.Title, Text: it.Thread.Text, Labels: it.Thread.Labels}, taskIDs[i], states[i])
			if err != nil {
				return nil, err
			}
			t, err := b.insertTask(taskIDs[i], tw.Thread, tw.Root, "batch", &batchID, it.AssigneeActorID, states[i])
			if err != nil {
				return nil, err
			}
			rows[i] = t
			deps := append([]DependencyView{}, existing[i]...)
			for _, d := range it.DependsOn {
				if d.Ref != "" {
					j := refIdx[d.Ref]
					deps = append(deps, DependencyView{TaskID: taskIDs[j], State: states[j], ChannelID: chanIDs[j]})
				}
			}
			if err := b.emitTask(t, taskEvent{Type: "task.created", Deps: deps, Extra: map[string]any{"origin": "batch", "batch_id": batchID}}); err != nil {
				return nil, err
			}
		}
		items := make([]map[string]string, len(req.Tasks))
		for i, it := range req.Tasks {
			var depIDs []string
			for _, d := range it.DependsOn {
				if d.Ref != "" {
					depIDs = append(depIDs, taskIDs[refIdx[d.Ref]])
				} else {
					depIDs = append(depIDs, d.TaskID)
				}
			}
			if err := b.insertDeps(taskIDs[i], depIDs); err != nil {
				return nil, err
			}
			items[i] = map[string]string{"ref": it.Ref, "task_id": taskIDs[i]}
		}
		if _, err := b.Exec(b.ctx, `UPDATE task_batches SET items=$2 WHERE id=$1`, batchID, jsonOf(items)); err != nil {
			return nil, err
		}
		out := TaskBatchView{BatchID: batchID, Tasks: make([]BatchEntry, len(rows))}
		for i, t := range rows {
			v, err := b.readerView(t)
			if err != nil {
				return nil, err
			}
			out.Tasks[i] = BatchEntry{Ref: req.Tasks[i].Ref, Task: v}
		}
		return &Result{Status: 201, ResourceType: "task_batch", ResourceID: batchID, Body: out}, nil
	})
}

// refCycle returns a cycle of refs among batch items, or nil.
func refCycle(items []BatchTaskItem, refIdx map[string]int) []string {
	const (
		white = iota
		grey
		black
	)
	color := make([]int, len(items))
	var stack []string
	var found []string
	var visit func(i int) bool
	visit = func(i int) bool {
		color[i] = grey
		stack = append(stack, items[i].Ref)
		for _, d := range items[i].DependsOn {
			if d.Ref == "" {
				continue
			}
			j := refIdx[d.Ref]
			if color[j] == grey {
				k := slices.Index(stack, d.Ref)
				found = append(slices.Clone(stack[k:]), d.Ref)
				return true
			}
			if color[j] == white && visit(j) {
				return true
			}
		}
		stack = stack[:len(stack)-1]
		color[i] = black
		return false
	}
	for i := range items {
		if color[i] == white && visit(i) {
			return found
		}
	}
	return nil
}

// GetTaskBatch regenerates a batch mapping (replay, BATCH-005).
func (s *Service) GetTaskBatch(ctx context.Context, p *Principal, boardID, batchID string) (*TaskBatchView, error) {
	v, err := s.viewBoard(ctx, s.DB, p, boardID)
	if err != nil {
		return nil, err
	}
	var items []map[string]string
	if err := s.DB.QueryRow(ctx, `SELECT items FROM task_batches WHERE id=$1 AND board_id=$2`, batchID, boardID).Scan(&items); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, NotFound("batch")
		}
		return nil, err
	}
	out := &TaskBatchView{BatchID: batchID, Tasks: []BatchEntry{}}
	now := s.Now()
	for _, it := range items {
		tv, err := s.taskViewFor(ctx, v, boardID, it["task_id"], now)
		if err != nil {
			return nil, err
		}
		out.Tasks = append(out.Tasks, BatchEntry{Ref: it["ref"], Task: *tv})
	}
	return out, nil
}

// ---- reads ----

func (v *boardView) reader() readFn {
	return func(id string) bool { _, ok := v.canRead(id); return ok }
}

func (s *Service) taskViewFor(ctx context.Context, v *boardView, boardID, taskID string, now time.Time) (*TaskView, error) {
	row, err := loadTaskRow(ctx, s.DB, boardID, taskID, false)
	if err != nil {
		return nil, err
	}
	if _, ok := v.canRead(row.ChannelID); !ok {
		return nil, NotFound("task")
	}
	tv, err := buildTaskView(ctx, s.DB, row, v.chans, v.reader(), now, nil)
	if err != nil {
		return nil, err
	}
	return &tv, nil
}

// GetTask returns the effective view (TASK-025).
func (s *Service) GetTask(ctx context.Context, p *Principal, boardID, taskID string) (*TaskView, error) {
	v, err := s.viewBoard(ctx, s.DB, p, boardID)
	if err != nil {
		return nil, err
	}
	return s.taskViewFor(ctx, v, boardID, taskID, s.Now())
}

// TaskFilter is the GET …/tasks query (TASK-026).
type TaskFilter struct {
	Channel  string
	Subtree  string
	States   []string
	Assignee string // actor ID, "none", or ""
	Owner    string
	Labels   map[string][]string
}

type TaskPage struct {
	Items         []TaskView `json:"items"`
	NextPageToken *string    `json:"next_page_token"`
}

// ListTasks lists readable tasks in creation order with effective-state
// filters (TASK-026).
func (s *Service) ListTasks(ctx context.Context, p *Principal, boardID string, f TaskFilter, after int64, limit int) (*TaskPage, error) {
	for _, st := range f.States {
		if !slices.Contains(TaskStates, st) {
			return nil, Validation("unknown state %q", st)
		}
	}
	v, err := s.viewBoard(ctx, s.DB, p, boardID)
	if err != nil {
		return nil, err
	}
	var readable []string
	cf := events.Filter{Channel: f.Channel, Subtree: f.Subtree}
	for _, c := range v.chans {
		if v.policy.Has(c.Path, authz.Read) && cf.MatchFacet(events.Facet{ChannelPath: c.Path, Invalidation: true}) {
			readable = append(readable, c.ID)
		}
	}
	out := &TaskPage{Items: []TaskView{}}
	if len(readable) == 0 {
		return out, nil
	}
	now := s.Now()
	states := f.States
	if states == nil {
		states = []string{}
	}
	cursor := after
	batch := max(limit+1, 200)
	lf := events.Filter{Labels: f.Labels}
	for scans := 0; ; scans++ {
		if scans == 10 {
			tok := encodePageToken(cursor)
			out.NextPageToken = &tok
			return out, nil
		}
		rows, err := s.DB.Query(ctx, `SELECT `+taskCols+`,
			COALESCE((SELECT jsonb_object_agg(key, value) FROM message_labels WHERE message_id=th.root_message_id), '{}'::jsonb)
			FROM tasks tk JOIN threads th ON th.id = tk.thread_id
			WHERE tk.board_id=$1 AND tk.channel_id = ANY($2) AND tk.created_position > $3
			  AND (cardinality($4::text[]) = 0 OR abby_task_effective_state(tk.state, tk.claim_expires_at, $5) = ANY($4))
			  AND ($6 = '' OR ($6 = 'none' AND tk.assignee_actor_id IS NULL) OR tk.assignee_actor_id = $6)
			  AND ($7 = '' OR tk.owner_actor_id = $7)
			ORDER BY tk.created_position LIMIT $8`, boardID, readable, cursor, states, now, f.Assignee, f.Owner, batch)
		if err != nil {
			return nil, err
		}
		type cand struct {
			row    taskRow
			labels map[string]string
		}
		var cands []cand
		for rows.Next() {
			var c cand
			if err := rows.Scan(&c.row.ID, &c.row.BoardID, &c.row.ThreadID, &c.row.ChannelID, &c.row.Owner, &c.row.Assignee, &c.row.CreatedBy, &c.row.Origin, &c.row.BatchID,
				&c.row.State, &c.row.Revision, &c.row.Epoch, &c.row.RetryCount, &c.row.BriefRevision, &c.row.CurrentClaimID, &c.row.ClaimExpiresAt, &c.row.CreatedPosition,
				&c.row.Block, &c.row.Outcome, &c.row.RootRedactedAt, &c.row.CreatedAt, &c.row.UpdatedAt, &c.row.StateChangedAt, &c.row.Title, &c.row.RootMessageID, &c.labels); err != nil {
				rows.Close()
				return nil, err
			}
			cands = append(cands, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		for _, c := range cands {
			if len(out.Items) == limit {
				tok := encodePageToken(cursor)
				out.NextPageToken = &tok
				return out, nil
			}
			cursor = c.row.CreatedPosition
			c.labels[TaskStateLabel] = c.row.effective(now).State
			if !lf.MatchFacet(events.Facet{ThreadScope: true, ChannelPath: chanByID(v.chans, c.row.ChannelID).Path, RootLabels: c.labels}) {
				continue
			}
			tv, err := buildTaskView(ctx, s.DB, c.row, v.chans, v.reader(), now, nil)
			if err != nil {
				return nil, err
			}
			out.Items = append(out.Items, tv)
		}
		if len(cands) < batch {
			return out, nil
		}
	}
}

// ---- briefs (BRIEF-001) ----

type BriefSource struct {
	Kind            string `json:"kind"`
	MessageID       string `json:"message_id"`
	MessageRevision int64  `json:"message_revision"`
}

type BriefView struct {
	TaskID           string      `json:"task_id"`
	Revision         int         `json:"revision"`
	Text             *string     `json:"text"`
	Redacted         bool        `json:"redacted"`
	Source           BriefSource `json:"source"`
	Reason           *string     `json:"reason"`
	CreatedByActorID string      `json:"created_by_actor_id"`
	CreatedAt        time.Time   `json:"created_at"`
	Task             *TaskView   `json:"task,omitempty"`
}

type BriefPage struct {
	Items         []BriefView `json:"items"`
	NextPageToken *string     `json:"next_page_token"`
}

const briefCols = `task_id, revision, text, redacted_at IS NOT NULL, source_kind, source_message_id, source_message_revision, reason, created_by, created_at`

func scanBrief(r pgx.Row) (BriefView, error) {
	var bv BriefView
	err := r.Scan(&bv.TaskID, &bv.Revision, &bv.Text, &bv.Redacted, &bv.Source.Kind, &bv.Source.MessageID, &bv.Source.MessageRevision, &bv.Reason, &bv.CreatedByActorID, &bv.CreatedAt)
	return bv, err
}

func (s *Service) readableTask(ctx context.Context, p *Principal, boardID, taskID string) (*boardView, taskRow, error) {
	v, err := s.viewBoard(ctx, s.DB, p, boardID)
	if err != nil {
		return nil, taskRow{}, err
	}
	row, err := loadTaskRow(ctx, s.DB, boardID, taskID, false)
	if err != nil {
		return nil, row, err
	}
	if _, ok := v.canRead(row.ChannelID); !ok {
		return nil, row, NotFound("task")
	}
	return v, row, nil
}

func (s *Service) ListBriefs(ctx context.Context, p *Principal, boardID, taskID string, after int64, limit int) (*BriefPage, error) {
	if _, _, err := s.readableTask(ctx, p, boardID, taskID); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT `+briefCols+` FROM task_briefs WHERE task_id=$1 AND revision > $2 ORDER BY revision LIMIT $3`, taskID, after, limit+1)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (BriefView, error) { return scanBrief(r) })
	if err != nil {
		return nil, err
	}
	out := &BriefPage{Items: items}
	if len(items) > limit {
		out.Items = items[:limit]
		tok := encodePageToken(int64(items[limit-1].Revision))
		out.NextPageToken = &tok
	}
	if out.Items == nil {
		out.Items = []BriefView{}
	}
	return out, nil
}

func (s *Service) GetBrief(ctx context.Context, p *Principal, boardID, taskID string, revision int) (*BriefView, error) {
	if _, _, err := s.readableTask(ctx, p, boardID, taskID); err != nil {
		return nil, err
	}
	bv, err := scanBrief(s.DB.QueryRow(ctx, `SELECT `+briefCols+` FROM task_briefs WHERE task_id=$1 AND revision=$2`, taskID, revision))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, NotFound("brief")
	}
	if err != nil {
		return nil, err
	}
	return &bv, nil
}

// GetBriefWithTask is the brief-update response and its replay.
func (s *Service) GetBriefWithTask(ctx context.Context, p *Principal, boardID, ref string) (*BriefView, error) {
	taskID, revStr, _ := strings.Cut(ref, "/")
	var rev int
	if _, err := fmt.Sscan(revStr, &rev); err != nil {
		return nil, NotFound("brief")
	}
	bv, err := s.GetBrief(ctx, p, boardID, taskID, rev)
	if err != nil {
		return nil, err
	}
	tv, err := s.GetTask(ctx, p, boardID, taskID)
	if err != nil {
		return nil, err
	}
	bv.Task = tv
	return bv, nil
}

// ---- claims history ----

type ClaimPage struct {
	Items         []ClaimView `json:"items"`
	NextPageToken *string     `json:"next_page_token"`
}

// ListClaims lists a task's claims newest first, without proofs.
func (s *Service) ListClaims(ctx context.Context, p *Principal, boardID, taskID string, before int64, limit int) (*ClaimPage, error) {
	if _, _, err := s.readableTask(ctx, p, boardID, taskID); err != nil {
		return nil, err
	}
	if before == 0 {
		before = 1 << 62
	}
	rows, err := s.DB.Query(ctx, `SELECT `+claimCols+` FROM claims WHERE task_id=$1 AND epoch < $2 ORDER BY epoch DESC LIMIT $3`, taskID, before, limit+1)
	if err != nil {
		return nil, err
	}
	cs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (claimRow, error) { return scanClaim(r) })
	if err != nil {
		return nil, err
	}
	now := s.Now()
	out := &ClaimPage{Items: []ClaimView{}}
	for i, c := range cs {
		if i == limit {
			tok := encodePageToken(cs[limit-1].Epoch)
			out.NextPageToken = &tok
			break
		}
		out.Items = append(out.Items, c.effective(now))
	}
	return out, nil
}

// GetClaim returns the current claim view (renew replay, CLAIM-042).
func (s *Service) GetClaim(ctx context.Context, p *Principal, boardID, claimID string) (*ClaimView, error) {
	c, err := loadClaim(ctx, s.DB, claimID)
	if err != nil {
		return nil, err
	}
	if _, _, err := s.readableTask(ctx, p, boardID, c.TaskID); err != nil {
		return nil, err
	}
	v := c.effective(s.Now())
	return &v, nil
}
