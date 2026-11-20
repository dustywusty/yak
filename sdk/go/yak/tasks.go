package yak

// P2 task helpers (spec/p2-tasks.md, ADR-0016). Every helper uses only the
// public HTTP API through Client.Do. Claimant operations send the claim
// proof in the Yak-Claim-Proof header and never in a body or query
// (CLAIM-012).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// HeaderClaimProof is the request header that carries a claim proof.
const HeaderClaimProof = "Yak-Claim-Proof"

// Task states of protocol v1 (TASK-020). The set is closed.
const (
	StateWaiting   = "waiting"
	StateReady     = "ready"
	StateClaimed   = "claimed"
	StateBlocked   = "blocked"
	StateCompleted = "completed"
	StateFailed    = "failed"
	StateCanceled  = "canceled"
)

// KnownTaskState reports whether s is a v1 task state. Clients must treat
// an unknown state as an error and never as done (TASK-020).
func KnownTaskState(s string) bool {
	switch s {
	case StateWaiting, StateReady, StateClaimed, StateBlocked, StateCompleted, StateFailed, StateCanceled:
		return true
	}
	return false
}

// Dependency is one readable prerequisite (DEP-009).
type Dependency struct {
	TaskID string `json:"task_id"`
	State  string `json:"state"`
}

// Claim is a claim view. It never contains a proof (CLAIM-011).
type Claim struct {
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

// Block describes why a task is blocked.
type Block struct {
	Reason    string    `json:"reason"`
	Cause     string    `json:"cause"`
	ByActorID string    `json:"by_actor_id"`
	At        time.Time `json:"at"`
}

// Evidence is one settlement evidence item (CLAIM-031). Kind is message,
// commit, url, artifact, or other.
type Evidence struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
	Note string `json:"note,omitempty"`
}

// Outcome is the settlement record of a terminal task (§19.1).
type Outcome struct {
	Kind        string          `json:"kind"`
	Reason      *string         `json:"reason,omitempty"`
	Summary     *string         `json:"summary,omitempty"`
	Evidence    []Evidence      `json:"evidence,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
	ByActorID   string          `json:"by_actor_id,omitempty"`
	ClaimID     *string         `json:"claim_id,omitempty"`
	ExecutionID *string         `json:"execution_id,omitempty"`
	At          *time.Time      `json:"at,omitempty"`
}

// Task is the effective task view (TASK-021).
type Task struct {
	DirectActionRequired          bool         `json:"direct_action_required"`
	ID                            string       `json:"id"`
	BoardID                       string       `json:"board_id"`
	ThreadID                      string       `json:"thread_id"`
	ChannelID                     string       `json:"channel_id"`
	ChannelPath                   string       `json:"channel_path"`
	Title                         string       `json:"title"`
	OwnerActorID                  string       `json:"owner_actor_id"`
	AssigneeActorID               *string      `json:"assignee_actor_id"`
	CreatedByActorID              string       `json:"created_by_actor_id"`
	Origin                        string       `json:"origin"`
	State                         string       `json:"state"`
	Revision                      int64        `json:"revision"`
	BriefRevision                 int          `json:"brief_revision"`
	Epoch                         int64        `json:"epoch"`
	RetryCount                    int          `json:"retry_count"`
	Dependencies                  []Dependency `json:"dependencies"`
	UnreadableDependenciesPending bool         `json:"unreadable_dependencies_pending"`
	CurrentClaim                  *Claim       `json:"current_claim"`
	LastClaim                     *Claim       `json:"last_claim"`
	Block                         *Block       `json:"block"`
	Outcome                       *Outcome     `json:"outcome"`
	RootRedacted                  bool         `json:"root_redacted"`
	CreatedAt                     time.Time    `json:"created_at"`
	UpdatedAt                     time.Time    `json:"updated_at"`
	StateChangedAt                time.Time    `json:"state_changed_at"`
}

// CheckState returns an error if the task state is not a v1 state.
func (t *Task) CheckState() error {
	if !KnownTaskState(t.State) {
		return fmt.Errorf("task %s has unknown state %q; treat it as an error, not as done (TASK-020)", t.ID, t.State)
	}
	return nil
}

// BriefSource names where a brief version came from.
type BriefSource struct {
	Kind            string `json:"kind"`
	MessageID       string `json:"message_id"`
	MessageRevision int64  `json:"message_revision"`
}

// Brief is one immutable brief version (BRIEF-001). Text is nil if redacted.
type Brief struct {
	TaskID           string       `json:"task_id"`
	Revision         int          `json:"revision"`
	Text             *string      `json:"text"`
	Redacted         bool         `json:"redacted"`
	Source           *BriefSource `json:"source,omitempty"`
	Reason           *string      `json:"reason,omitempty"`
	CreatedByActorID string       `json:"created_by_actor_id,omitempty"`
	CreatedAt        *time.Time   `json:"created_at,omitempty"`
	Task             *Task        `json:"task,omitempty"` // set on POST …/briefs
}

// ClaimBrief is the brief carried in a claim response.
type ClaimBrief struct {
	Revision int     `json:"revision"`
	Text     *string `json:"text"`
	Redacted bool    `json:"redacted"`
}

// ClaimResponse is the result of claim and claim-next (§19.2). For
// claim-next, Outcome is "claimed" or "no_work" (then TimedOut is set and
// the other fields are empty).
type ClaimResponse struct {
	Outcome          string      `json:"outcome"`
	TimedOut         bool        `json:"timed_out,omitempty"`
	Claim            *Claim      `json:"claim,omitempty"`
	Proof            *string     `json:"proof,omitempty"`
	ProofUnavailable bool        `json:"proof_unavailable,omitempty"`
	Brief            *ClaimBrief `json:"brief,omitempty"`
	Task             *Task       `json:"task,omitempty"`
	// Replayed is true when the server answered from an idempotency receipt.
	Replayed bool `json:"-"`
}

// Claimed reports whether the response holds a claim.
func (r *ClaimResponse) Claimed() bool { return r != nil && r.Outcome == "claimed" && r.Claim != nil }

// ProofValue returns the proof, or "" if the server did not return one
// (replay without proof material, CLAIM-041).
func (r *ClaimResponse) ProofValue() string {
	if r == nil || r.Proof == nil {
		return ""
	}
	return *r.Proof
}

// NewThread is the thread part of a create or batch item.
type NewThread struct {
	Channel string            `json:"channel"`
	Title   string            `json:"title"`
	Text    string            `json:"text"`
	Labels  map[string]string `json:"labels,omitempty"`
}

// CreateTaskRequest creates a task with a new thread (Thread set) or
// converts an existing thread (ThreadID set). Exactly one must be set.
type CreateTaskRequest struct {
	DirectActionRequired bool       `json:"direct_action_required"`
	Thread               *NewThread `json:"thread,omitempty"`
	ThreadID             string     `json:"thread_id,omitempty"`
	AssigneeActorID      *string    `json:"assignee_actor_id,omitempty"`
	DependsOn            []string   `json:"depends_on,omitempty"`
}

// PatchTaskRequest edits title, owner or the direct action flag (L19).
type PatchTaskRequest struct {
	DirectActionRequired *bool   `json:"direct_action_required,omitempty"`
	Title                *string `json:"title,omitempty"`
	OwnerActorID         *string `json:"owner_actor_id,omitempty"`
}

// ClaimRequest is the body of POST …/tasks/{id}/claims.
type ClaimRequest struct {
	LeaseSeconds        int  `json:"lease_seconds,omitempty"`
	ExpectBriefRevision *int `json:"expect_brief_revision,omitempty"`
}

// ClaimNextRequest is the body of POST …/tasks/claim-next. A label key
// mapped to an empty (non-nil) list means "key present".
type ClaimNextRequest struct {
	Channel      string              `json:"channel,omitempty"`
	Subtree      string              `json:"subtree,omitempty"`
	Labels       map[string][]string `json:"labels,omitempty"`
	Assignee     string              `json:"assignee,omitempty"` // eligible | me | unassigned
	LeaseSeconds int                 `json:"lease_seconds,omitempty"`
	WaitMS       int                 `json:"wait_ms,omitempty"`
}

// Result is the complete body's result (CLAIM-031).
type Result struct {
	Summary  string          `json:"summary,omitempty"`
	Evidence []Evidence      `json:"evidence,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
}

// FailRequest is the fail body. Reason is required.
type FailRequest struct {
	Reason   string          `json:"reason"`
	Evidence []Evidence      `json:"evidence,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
}

// BatchDep is one dependency of a batch item: Ref (an item of the same
// batch) or TaskID (an existing task).
type BatchDep struct {
	Ref    string `json:"ref,omitempty"`
	TaskID string `json:"task_id,omitempty"`
}

// BatchItem is one item of POST …/tasks/batch.
type BatchItem struct {
	DirectActionRequired bool       `json:"direct_action_required,omitempty"`
	Ref                  string     `json:"ref"`
	Thread               NewThread  `json:"thread"`
	AssigneeActorID      *string    `json:"assignee_actor_id,omitempty"`
	DependsOn            []BatchDep `json:"depends_on,omitempty"`
}

// CreateTaskBatchRequest holds 1..max_batch_tasks items.
type CreateTaskBatchRequest struct {
	Tasks []BatchItem `json:"tasks"`
}

// TaskBatch is the batch response: refs mapped to tasks in request order.
type TaskBatch struct {
	BatchID string `json:"batch_id"`
	Tasks   []struct {
		Ref  string `json:"ref"`
		Task Task   `json:"task"`
	} `json:"tasks"`
}

// TaskPage, BriefPage, and ClaimPage are list pages (PAGE-001).
type TaskPage struct {
	Items         []Task  `json:"items"`
	NextPageToken *string `json:"next_page_token"`
}

type BriefPage struct {
	Items         []Brief `json:"items"`
	NextPageToken *string `json:"next_page_token"`
}

type ClaimPage struct {
	Items         []Claim `json:"items"`
	NextPageToken *string `json:"next_page_token"`
}

// TaskFilter selects tasks for GET …/tasks (TASK-026).
type TaskFilter struct {
	Channel  string
	Subtree  string
	States   []string
	Assignee string // actor ID or "none"
	Owner    string
	Labels   []string // P1 syntax: key or key:value
}

func (f TaskFilter) values() url.Values {
	q := url.Values{}
	if f.Channel != "" {
		q.Set("channel", f.Channel)
	}
	if f.Subtree != "" {
		q.Set("subtree", f.Subtree)
	}
	if len(f.States) > 0 {
		q["state"] = append([]string(nil), f.States...)
	}
	if f.Assignee != "" {
		q.Set("assignee", f.Assignee)
	}
	if f.Owner != "" {
		q.Set("owner", f.Owner)
	}
	if len(f.Labels) > 0 {
		q["label"] = append([]string(nil), f.Labels...)
	}
	return q
}

// ---- error helpers ----

// IsClaimLost reports a 409 claim_lost: the proof is not the active claim.
func IsClaimLost(err error) bool { return IsCode(err, "claim_lost") }

// IsTaskClaimed reports a 409 task_claimed: another claim is active.
func IsTaskClaimed(err error) bool { return IsCode(err, "task_claimed") }

// IsOwnershipLost reports an error after which a claimant must stop at
// once (RUN-004): claim_lost, or a lost read/claim right (403, 404).
func IsOwnershipLost(err error) bool {
	var p *Problem
	if !errors.As(err, &p) {
		return false
	}
	return p.Code == "claim_lost" || p.Status == http.StatusForbidden || p.Status == http.StatusNotFound
}

// IsTransient reports an error whose outcome is unknown and that may be
// retried with the same Idempotency-Key: network failures, 408, 429, and
// 5xx. A transient error is not evidence that the request did not commit.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	var p *Problem
	if errors.As(err, &p) {
		return p.Status == http.StatusRequestTimeout || p.Status == http.StatusTooManyRequests || p.Status >= 500
	}
	// The caller's own deadline or cancellation is not a server fault.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	// Transport errors that are not net.Error (for example an unexpected
	// EOF while reading a body) also leave the outcome unknown.
	var ue *url.Error
	return errors.As(err, &ue) || errors.Is(err, io.ErrUnexpectedEOF)
}

// ---- proof transport ----

type proofTransport struct {
	base  http.RoundTripper
	proof string
}

func (p proofTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	r2.Header.Set(HeaderClaimProof, p.proof)
	return p.base.RoundTrip(r2)
}

// withProof returns a shallow copy of c whose requests carry the proof
// header. The proof stays in memory; it is never put in a URL or body.
func (c *Client) withProof(proof string) *Client {
	cc := *c
	var hc http.Client
	if c.HTTP != nil {
		hc = *c.HTTP
	}
	base := hc.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	hc.Transport = proofTransport{base: base, proof: proof}
	cc.HTTP = &hc
	return &cc
}

// ---- task client ----

// Tasks is a board-scoped task client.
type Tasks struct {
	C     *Client
	Board string
}

// Tasks returns the task client for a board.
func (c *Client) Tasks(boardID string) *Tasks { return &Tasks{C: c, Board: boardID} }

func (t *Tasks) path(rel string) string { return BoardPath(t.Board, "/tasks"+rel) }

func taskPath(id string, rel string) string { return "/" + url.PathEscape(id) + rel }

func needKey(key string) string {
	if key == "" {
		return NewIdempotencyKey()
	}
	return key
}

func (t *Tasks) taskCall(ctx context.Context, c *Client, r Request) (*Task, error) {
	var out Task
	if _, err := c.Do(ctx, r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (t *Tasks) claimant(proof string) (*Client, error) {
	if proof == "" {
		return nil, errors.New("claim proof is required for claimant operations")
	}
	return t.C.withProof(proof), nil
}

// Create creates a task with a new thread (L1). key "" means a random key;
// pass your own key to retry safely.
func (t *Tasks) Create(ctx context.Context, req CreateTaskRequest, key string) (*Task, error) {
	if (req.Thread == nil) == (req.ThreadID == "") {
		return nil, errors.New("set exactly one of Thread or ThreadID")
	}
	return t.taskCall(ctx, t.C, Request{Method: http.MethodPost, Path: t.path(""), Body: req, IdempotencyKey: needKey(key)})
}

// Convert turns an existing thread into a task (L2).
func (t *Tasks) Convert(ctx context.Context, threadID string, assignee *string, dependsOn []string, key string) (*Task, error) {
	return t.Create(ctx, CreateTaskRequest{ThreadID: threadID, AssigneeActorID: assignee, DependsOn: dependsOn}, key)
}

// Get reads the effective task view. The ETag revision is t.Revision.
func (t *Tasks) Get(ctx context.Context, id string) (*Task, error) {
	return t.taskCall(ctx, t.C, Request{Method: http.MethodGet, Path: t.path(taskPath(id, ""))})
}

// List reads one page of tasks in creation order.
func (t *Tasks) List(ctx context.Context, f TaskFilter, pageToken string, limit int) (*TaskPage, error) {
	q := f.values()
	if pageToken != "" {
		q.Set("page_token", pageToken)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var pg TaskPage
	if _, err := t.C.Do(ctx, Request{Method: http.MethodGet, Path: t.path(""), Query: q}, &pg); err != nil {
		return nil, err
	}
	return &pg, nil
}

// ListAll follows page tokens and calls fn for every task.
func (t *Tasks) ListAll(ctx context.Context, f TaskFilter, fn func(Task) error) error {
	tok := ""
	for {
		pg, err := t.List(ctx, f, tok, 200)
		if err != nil {
			return err
		}
		for _, it := range pg.Items {
			if err := fn(it); err != nil {
				return err
			}
		}
		if pg.NextPageToken == nil || *pg.NextPageToken == "" {
			return nil
		}
		tok = *pg.NextPageToken
	}
}

// Patch edits title, owner or the direct action flag under If-Match (L19).
func (t *Tasks) Patch(ctx context.Context, id string, ifMatch int64, req PatchTaskRequest) (*Task, error) {
	return t.taskCall(ctx, t.C, Request{Method: http.MethodPatch, Path: t.path(taskPath(id, "")), Body: req, IfMatch: strconv.FormatInt(ifMatch, 10)})
}

// Briefs lists brief versions.
func (t *Tasks) Briefs(ctx context.Context, id, pageToken string, limit int) (*BriefPage, error) {
	q := url.Values{}
	if pageToken != "" {
		q.Set("page_token", pageToken)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var pg BriefPage
	if _, err := t.C.Do(ctx, Request{Method: http.MethodGet, Path: t.path(taskPath(id, "/briefs")), Query: q}, &pg); err != nil {
		return nil, err
	}
	return &pg, nil
}

// Brief reads one brief version.
func (t *Tasks) Brief(ctx context.Context, id string, revision int) (*Brief, error) {
	var b Brief
	if _, err := t.C.Do(ctx, Request{Method: http.MethodGet, Path: t.path(taskPath(id, "/briefs/"+strconv.Itoa(revision)))}, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// UpdateBrief stores a new brief version (L17). ifMatch is the task revision.
func (t *Tasks) UpdateBrief(ctx context.Context, id string, ifMatch int64, text, reason, key string) (*Brief, error) {
	var b Brief
	_, err := t.C.Do(ctx, Request{Method: http.MethodPost, Path: t.path(taskPath(id, "/briefs")), IfMatch: strconv.FormatInt(ifMatch, 10),
		IdempotencyKey: needKey(key), Body: map[string]any{"text": text, "reason": reason}}, &b)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// RootRedaction tombstones the task root (L18). rootRevision is the root
// message revision (If-Match).
func (t *Tasks) RootRedaction(ctx context.Context, id string, rootRevision int64, reason, key string) (*Task, error) {
	return t.taskCall(ctx, t.C, Request{Method: http.MethodPost, Path: t.path(taskPath(id, "/root-redaction")), IfMatch: strconv.FormatInt(rootRevision, 10),
		IdempotencyKey: needKey(key), Body: map[string]any{"reason": reason}})
}

// Claims lists the claim history of a task, newest first (no proofs).
func (t *Tasks) Claims(ctx context.Context, id, pageToken string, limit int) (*ClaimPage, error) {
	q := url.Values{}
	if pageToken != "" {
		q.Set("page_token", pageToken)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var pg ClaimPage
	if _, err := t.C.Do(ctx, Request{Method: http.MethodGet, Path: t.path(taskPath(id, "/claims")), Query: q}, &pg); err != nil {
		return nil, err
	}
	return &pg, nil
}

// Claim claims one task (L3). Keep key until the outcome is known: a replay
// returns the same claim (CLAIM-041).
func (t *Tasks) Claim(ctx context.Context, id string, req ClaimRequest, key string) (*ClaimResponse, error) {
	var out ClaimResponse
	resp, err := t.C.Do(ctx, Request{Method: http.MethodPost, Path: t.path(taskPath(id, "/claims")), Body: req, IdempotencyKey: needKey(key)}, &out)
	if err != nil {
		return nil, err
	}
	out.Replayed = resp.Replayed
	if out.Outcome == "" && out.Claim != nil {
		out.Outcome = "claimed"
	}
	return &out, nil
}

// ClaimNext makes one claim-next request. On no work the response has
// Outcome "no_work". A no_work result does not reserve the key (CLAIM-045).
func (t *Tasks) ClaimNext(ctx context.Context, req ClaimNextRequest, key string) (*ClaimResponse, error) {
	var out ClaimResponse
	resp, err := t.C.Do(ctx, Request{Method: http.MethodPost, Path: t.path("/claim-next"), Body: req, IdempotencyKey: needKey(key)}, &out)
	if err != nil {
		return nil, err
	}
	out.Replayed = resp.Replayed
	return &out, nil
}

// MaxClaimWait is the reference max_claim_wait_ms (TASK-002).
const MaxClaimWait = 30 * time.Second

// ClaimNextWait repeats claim-next until it claims a task or wait elapses.
// Each request asks the server to wait up to min(remaining, perRequest)
// (perRequest <= 0 means MaxClaimWait). All attempts use the same key, so
// a claim that committed while a response was lost is recovered by replay
// (SEL-008); no_work never reserves the key.
func (t *Tasks) ClaimNextWait(ctx context.Context, req ClaimNextRequest, wait, perRequest time.Duration, key string) (*ClaimResponse, error) {
	key = needKey(key)
	if perRequest <= 0 {
		perRequest = MaxClaimWait
	}
	deadline := time.Now().Add(wait)
	for {
		r := req
		rem := time.Until(deadline)
		if rem > 0 {
			r.WaitMS = int(min(rem, perRequest) / time.Millisecond)
		} else {
			r.WaitMS = 0
		}
		out, err := t.ClaimNext(ctx, r, key)
		if err != nil {
			return nil, err
		}
		if out.Claimed() || time.Until(deadline) <= 0 {
			return out, nil
		}
		if !out.TimedOut {
			// The server answered at once (for example wait_ms rounded to
			// 0); avoid a hot loop.
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(min(time.Until(deadline), 50*time.Millisecond)):
			}
		}
	}
}

// Renew extends the claim's lease (L4). Use a new key per renewal
// (RUN-002); reuse it only to retry the same renewal. leaseSeconds 0 means
// the server default.
func (t *Tasks) Renew(ctx context.Context, id, claimID, proof string, leaseSeconds int, key string) (*Claim, error) {
	c, err := t.claimant(proof)
	if err != nil {
		return nil, err
	}
	body := map[string]any{}
	if leaseSeconds > 0 {
		body["lease_seconds"] = leaseSeconds
	}
	var out Claim
	if _, err := c.Do(ctx, Request{Method: http.MethodPost, Path: t.path(taskPath(id, "/claims/"+url.PathEscape(claimID)+"/renew")), Body: body, IdempotencyKey: needKey(key)}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func optReason(reason string) map[string]any {
	if reason == "" {
		return map[string]any{}
	}
	return map[string]any{"reason": reason}
}

// Release gives the task back (L5).
func (t *Tasks) Release(ctx context.Context, id, proof, reason, key string) (*Task, error) {
	c, err := t.claimant(proof)
	if err != nil {
		return nil, err
	}
	return t.taskCall(ctx, c, Request{Method: http.MethodPost, Path: t.path(taskPath(id, "/release")), Body: optReason(reason), IdempotencyKey: needKey(key)})
}

// Complete settles the claim as completed (L7).
func (t *Tasks) Complete(ctx context.Context, id, proof string, result Result, key string) (*Task, error) {
	c, err := t.claimant(proof)
	if err != nil {
		return nil, err
	}
	return t.taskCall(ctx, c, Request{Method: http.MethodPost, Path: t.path(taskPath(id, "/complete")), Body: map[string]any{"result": result}, IdempotencyKey: needKey(key)})
}

// Fail settles the claim as failed (L8). req.Reason is required.
func (t *Tasks) Fail(ctx context.Context, id, proof string, req FailRequest, key string) (*Task, error) {
	if req.Reason == "" {
		return nil, errors.New("fail reason is required")
	}
	c, err := t.claimant(proof)
	if err != nil {
		return nil, err
	}
	return t.taskCall(ctx, c, Request{Method: http.MethodPost, Path: t.path(taskPath(id, "/fail")), Body: req, IdempotencyKey: needKey(key)})
}

// Block blocks the task (L9, L10). With a proof it is a claimant block;
// without one the caller must be a manager. ifMatch 0 sends no If-Match.
func (t *Tasks) Block(ctx context.Context, id, proof, reason string, ifMatch int64, key string) (*Task, error) {
	if reason == "" {
		return nil, errors.New("block reason is required")
	}
	c := t.C
	if proof != "" {
		c = t.C.withProof(proof)
	}
	return t.taskCall(ctx, c, Request{Method: http.MethodPost, Path: t.path(taskPath(id, "/block")), Body: map[string]any{"reason": reason}, IdempotencyKey: needKey(key), IfMatch: optRev(ifMatch)})
}

func optRev(r int64) string {
	if r <= 0 {
		return ""
	}
	return strconv.FormatInt(r, 10)
}

func (t *Tasks) manager(ctx context.Context, id, op string, body map[string]any, ifMatch int64, key string) (*Task, error) {
	return t.taskCall(ctx, t.C, Request{Method: http.MethodPost, Path: t.path(taskPath(id, "/"+op)), Body: body, IdempotencyKey: needKey(key), IfMatch: optRev(ifMatch)})
}

// Unblock returns a blocked task to ready or waiting (L11).
func (t *Tasks) Unblock(ctx context.Context, id, reason string, ifMatch int64, key string) (*Task, error) {
	return t.manager(ctx, id, "unblock", optReason(reason), ifMatch, key)
}

// Cancel cancels a nonterminal task (L12). reason is required.
func (t *Tasks) Cancel(ctx context.Context, id, reason string, ifMatch int64, key string) (*Task, error) {
	if reason == "" {
		return nil, errors.New("cancel reason is required")
	}
	return t.manager(ctx, id, "cancel", map[string]any{"reason": reason}, ifMatch, key)
}

// Reopen reopens a failed or canceled task (L13).
func (t *Tasks) Reopen(ctx context.Context, id, reason string, ifMatch int64, key string) (*Task, error) {
	return t.manager(ctx, id, "reopen", optReason(reason), ifMatch, key)
}

// SetAssignee replaces the assignee (L16); nil clears it.
func (t *Tasks) SetAssignee(ctx context.Context, id string, ifMatch int64, assignee *string, key string) (*Task, error) {
	return t.taskCall(ctx, t.C, Request{Method: http.MethodPut, Path: t.path(taskPath(id, "/assignee")), IfMatch: strconv.FormatInt(ifMatch, 10),
		IdempotencyKey: key, Body: map[string]any{"assignee_actor_id": assignee}})
}

// SetDependencies replaces the readable prerequisites (L15).
func (t *Tasks) SetDependencies(ctx context.Context, id string, ifMatch int64, taskIDs []string, key string) (*Task, error) {
	if taskIDs == nil {
		taskIDs = []string{}
	}
	return t.taskCall(ctx, t.C, Request{Method: http.MethodPut, Path: t.path(taskPath(id, "/dependencies")), IfMatch: strconv.FormatInt(ifMatch, 10),
		IdempotencyKey: key, Body: map[string]any{"task_ids": taskIDs}})
}

// Batch creates 1..max_batch_tasks tasks atomically (BATCH-001..005).
func (t *Tasks) Batch(ctx context.Context, req CreateTaskBatchRequest, key string) (*TaskBatch, error) {
	var out TaskBatch
	if _, err := t.C.Do(ctx, Request{Method: http.MethodPost, Path: t.path("/batch"), Body: req, IdempotencyKey: needKey(key)}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- multi-batch plan helper (BATCH-006) ----

// PlanLabelID and PlanLabelRef are the client labels the plan helper puts
// on every root so a rerun can find tasks after receipts expire.
const (
	PlanLabelID  = "plan.id"
	PlanLabelRef = "plan.ref"
)

// PlanProgress is the resumable state of a plan submission.
type PlanProgress struct {
	PlanID      string            `json:"plan_id"`
	BatchesDone []int             `json:"batches_done"`
	BatchIDs    map[string]string `json:"batch_ids,omitempty"` // batch number -> batch_id
	Tasks       map[string]string `json:"tasks"`               // ref -> task ID
}

func (p *PlanProgress) done(n int) bool {
	for _, d := range p.BatchesDone {
		if d == n {
			return true
		}
	}
	return false
}

// PlanOptions configures SubmitPlan.
type PlanOptions struct {
	PlanID    string
	BatchSize int // default 100 (max_batch_tasks)
	// Progress is the state from an earlier run (may be nil).
	Progress *PlanProgress
	// Save is called after every confirmed batch (may be nil).
	Save func(*PlanProgress) error
	// Log receives one line per batch (may be nil).
	Log func(format string, a ...any)
}

// PlanID derives a deterministic plan ID from the plan's content.
func PlanID(items []BatchItem) string {
	b, _ := json.Marshal(items)
	s := sha256.Sum256(b)
	return "plan-" + hex.EncodeToString(s[:8])
}

// PlanBatches orders items topologically (keeping file order where the
// graph allows) and cuts them into batches of at most size. Every ref
// dependency points to an item in the same or an earlier batch.
func PlanBatches(items []BatchItem, size int) ([][]BatchItem, error) {
	if size <= 0 {
		size = 100
	}
	idx := map[string]int{}
	for i, it := range items {
		if it.Ref == "" {
			return nil, fmt.Errorf("item %d: ref is required", i)
		}
		if _, dup := idx[it.Ref]; dup {
			return nil, fmt.Errorf("item %d: duplicate ref %q", i, it.Ref)
		}
		idx[it.Ref] = i
	}
	indeg := make([]int, len(items))
	out := make([][]int, len(items))
	for i, it := range items {
		for _, d := range it.DependsOn {
			if (d.Ref == "") == (d.TaskID == "") {
				return nil, fmt.Errorf("item %q: each depends_on entry needs exactly one of ref or task_id", it.Ref)
			}
			if d.Ref == "" {
				continue
			}
			j, ok := idx[d.Ref]
			if !ok {
				return nil, fmt.Errorf("item %q: unknown ref %q", it.Ref, d.Ref)
			}
			indeg[i]++
			out[j] = append(out[j], i)
		}
	}
	// Kahn's algorithm; the ready set is kept sorted by file position.
	var ready []int
	for i := range items {
		if indeg[i] == 0 {
			ready = append(ready, i)
		}
	}
	var order []int
	for len(ready) > 0 {
		sort.Ints(ready)
		i := ready[0]
		ready = ready[1:]
		order = append(order, i)
		for _, j := range out[i] {
			if indeg[j]--; indeg[j] == 0 {
				ready = append(ready, j)
			}
		}
	}
	if len(order) != len(items) {
		var cyc []string
		for i := range items {
			if indeg[i] > 0 {
				cyc = append(cyc, items[i].Ref)
			}
		}
		return nil, fmt.Errorf("plan has a dependency cycle among refs %s", strings.Join(cyc, ", "))
	}
	var batches [][]BatchItem
	for start := 0; start < len(order); start += size {
		end := min(start+size, len(order))
		var b []BatchItem
		for _, i := range order[start:end] {
			b = append(b, items[i])
		}
		batches = append(batches, b)
	}
	return batches, nil
}

// SubmitPlan creates a plan of any size as a sequence of atomic batches.
// It is not atomic as a whole (BATCH-006). Batch n uses the key
// "<plan-id>:<n>"; each root gets the labels plan.id and plan.ref. It is
// safe to rerun: batches in Progress are skipped, a batch whose receipt
// still exists replays, and a batch whose tasks already exist (found by
// plan labels) is adopted without a new request.
func (t *Tasks) SubmitPlan(ctx context.Context, items []BatchItem, opt PlanOptions) (*PlanProgress, error) {
	logf := opt.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if opt.PlanID == "" {
		opt.PlanID = PlanID(items)
	}
	prog := opt.Progress
	if prog == nil || prog.PlanID != opt.PlanID {
		if prog != nil && prog.PlanID != "" {
			return nil, fmt.Errorf("progress is for plan %q, not %q", prog.PlanID, opt.PlanID)
		}
		prog = &PlanProgress{PlanID: opt.PlanID}
	}
	if prog.Tasks == nil {
		prog.Tasks = map[string]string{}
	}
	if prog.BatchIDs == nil {
		prog.BatchIDs = map[string]string{}
	}
	batches, err := PlanBatches(items, opt.BatchSize)
	if err != nil {
		return prog, err
	}
	for bi, batch := range batches {
		n := bi + 1
		if prog.done(n) {
			logf("batch %d/%d: already done", n, len(batches))
			continue
		}
		inBatch := map[string]bool{}
		for _, it := range batch {
			inBatch[it.Ref] = true
		}
		req := CreateTaskBatchRequest{}
		for _, it := range batch {
			it2 := it
			labels := map[string]string{}
			for k, v := range it.Thread.Labels {
				labels[k] = v
			}
			labels[PlanLabelID] = opt.PlanID
			labels[PlanLabelRef] = it.Ref
			it2.Thread.Labels = labels
			it2.DependsOn = nil
			for _, d := range it.DependsOn {
				switch {
				case d.TaskID != "":
					it2.DependsOn = append(it2.DependsOn, d)
				case inBatch[d.Ref]:
					it2.DependsOn = append(it2.DependsOn, BatchDep{Ref: d.Ref})
				default:
					id, ok := prog.Tasks[d.Ref]
					if !ok {
						return prog, fmt.Errorf("batch %d: ref %q has no task yet (earlier batch missing)", n, d.Ref)
					}
					it2.DependsOn = append(it2.DependsOn, BatchDep{TaskID: id})
				}
			}
			req.Tasks = append(req.Tasks, it2)
		}
		// Receipts may have expired: look for tasks this plan already made.
		found, err := t.findPlanTasks(ctx, opt.PlanID, batch)
		if err != nil {
			return prog, err
		}
		switch {
		case len(found) == len(batch):
			for r, id := range found {
				prog.Tasks[r] = id
			}
			logf("batch %d/%d: %d tasks already exist; adopted", n, len(batches), len(found))
		case len(found) > 0:
			return prog, fmt.Errorf("batch %d: %d of %d tasks already exist; the plan changed or a batch was split; resolve by hand", n, len(found), len(batch))
		default:
			key := fmt.Sprintf("%s:%d", opt.PlanID, n)
			res, err := t.Batch(ctx, req, key)
			if err != nil {
				return prog, fmt.Errorf("batch %d/%d (key %s): %w", n, len(batches), key, err)
			}
			for _, m := range res.Tasks {
				prog.Tasks[m.Ref] = m.Task.ID
			}
			prog.BatchIDs[strconv.Itoa(n)] = res.BatchID
			logf("batch %d/%d: %d tasks (batch %s)", n, len(batches), len(res.Tasks), res.BatchID)
		}
		prog.BatchesDone = append(prog.BatchesDone, n)
		if opt.Save != nil {
			if err := opt.Save(prog); err != nil {
				return prog, err
			}
		}
	}
	return prog, nil
}

// findPlanTasks returns ref -> task ID for batch items that already exist.
func (t *Tasks) findPlanTasks(ctx context.Context, planID string, batch []BatchItem) (map[string]string, error) {
	f := TaskFilter{Labels: []string{PlanLabelID + ":" + planID}}
	for _, it := range batch {
		f.Labels = append(f.Labels, PlanLabelRef+":"+it.Ref)
	}
	var hits []Task
	if err := t.ListAll(ctx, f, func(tk Task) error { hits = append(hits, tk); return nil }); err != nil {
		return nil, err
	}
	found := map[string]string{}
	if len(hits) == 0 {
		return found, nil
	}
	// Task views carry no labels, so map each ref with its own query.
	for _, it := range batch {
		var ids []string
		err := t.ListAll(ctx, TaskFilter{Labels: []string{PlanLabelID + ":" + planID, PlanLabelRef + ":" + it.Ref}}, func(tk Task) error {
			ids = append(ids, tk.ID)
			return nil
		})
		if err != nil {
			return nil, err
		}
		switch len(ids) {
		case 0:
		case 1:
			found[it.Ref] = ids[0]
		default:
			return nil, fmt.Errorf("ref %q matches %d tasks (%s)", it.Ref, len(ids), strings.Join(ids, ", "))
		}
	}
	return found, nil
}
