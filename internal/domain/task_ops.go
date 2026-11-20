package domain

// P2 task commands: claims (§7), lifecycle (§5), assignment (§8),
// dependencies (§9), claim-next (§10), briefs and root redaction (§12),
// and the expiry sweeper (§7.6).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/dustywusty/abby/internal/authz"
	"github.com/dustywusty/abby/internal/events"
	"github.com/dustywusty/abby/internal/ids"
)

// ---- request shapes (ADR-0016 §3) ----

type ClaimRequest struct {
	LeaseSeconds        *int `json:"lease_seconds,omitempty"`
	ExpectBriefRevision *int `json:"expect_brief_revision,omitempty"`
}

type ClaimNextRequest struct {
	Channel      string              `json:"channel,omitempty"`
	Subtree      string              `json:"subtree,omitempty"`
	Labels       map[string][]string `json:"labels,omitempty"`
	Assignee     string              `json:"assignee,omitempty"`
	LeaseSeconds *int                `json:"lease_seconds,omitempty"`
	WaitMs       *int                `json:"wait_ms,omitempty"`
}

type RenewRequest struct {
	LeaseSeconds *int `json:"lease_seconds,omitempty"`
}

type ReasonRequest struct {
	Reason *string `json:"reason,omitempty"`
}

type TaskResult struct {
	Summary  *string        `json:"summary,omitempty"`
	Evidence []Evidence     `json:"evidence,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
}

type CompleteRequest struct {
	Result *TaskResult `json:"result,omitempty"`
}

type FailRequest struct {
	Reason   *string        `json:"reason,omitempty"`
	Evidence []Evidence     `json:"evidence,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
}

type AssigneeRequest struct {
	AssigneeActorID *string `json:"assignee_actor_id"`
}

type DependenciesRequest struct {
	TaskIDs []string `json:"task_ids"`
}

type PatchTaskRequest struct {
	Title        *string `json:"title,omitempty"`
	OwnerActorID *string `json:"owner_actor_id,omitempty"`
}

type BriefUpdateRequest struct {
	Text   string  `json:"text"`
	Reason *string `json:"reason,omitempty"`
}

// ClaimResponse is the claim and claim-next result (spec §19.2).
type ClaimResponse struct {
	Outcome          string       `json:"outcome"`
	Claim            ClaimView    `json:"claim"`
	Proof            *string      `json:"proof"`
	ProofUnavailable bool         `json:"proof_unavailable"`
	Brief            BriefSummary `json:"brief"`
	Task             TaskView     `json:"task"`
}

type BriefSummary struct {
	Revision int     `json:"revision"`
	Text     *string `json:"text"`
	Redacted bool    `json:"redacted"`
}

// NoWork is the claim-next result without a claim (SEL-007).
type NoWork struct {
	Outcome  string `json:"outcome"`
	TimedOut bool   `json:"timed_out"`
}

// ---- validation ----

func (s *Service) leaseSeconds(v *int) (int, error) {
	if v == nil {
		return int(s.Cfg.ClaimLeaseDefault / time.Second), nil
	}
	lo, hi := int(s.Cfg.ClaimLeaseMin/time.Second), int(s.Cfg.ClaimLeaseMax/time.Second)
	if *v < lo || *v > hi {
		e := Validation("lease_seconds must be %d..%d", lo, hi)
		e.Extra = map[string]any{"limit": hi}
		return 0, e
	}
	return *v, nil
}

func (s *Service) reasonText(r *string, required bool, what string) (*string, error) {
	if r == nil || strings.TrimSpace(*r) == "" {
		if required {
			return nil, Validation("%s requires a reason (1..%d bytes)", what, s.Cfg.MaxTextBytes)
		}
		return nil, nil
	}
	if !utf8.ValidString(*r) {
		return nil, Validation("reason must be valid UTF-8")
	}
	if len(*r) > s.Cfg.MaxTextBytes {
		return nil, limitExceeded(s.Cfg.MaxTextBytes, "reason exceeds %d bytes", s.Cfg.MaxTextBytes)
	}
	return r, nil
}

var evidenceKinds = []string{"message", "commit", "url", "artifact", "other"}

func (s *Service) validateEvidence(ev []Evidence, data map[string]any) ([]Evidence, error) {
	if len(ev) > s.Cfg.MaxEvidenceItems {
		return nil, limitExceeded(s.Cfg.MaxEvidenceItems, "at most %d evidence items", s.Cfg.MaxEvidenceItems)
	}
	for _, e := range ev {
		if !slices.Contains(evidenceKinds, e.Kind) {
			return nil, Validation("evidence kind must be one of %s", strings.Join(evidenceKinds, ", "))
		}
		if e.Ref == "" || !utf8.ValidString(e.Ref) || len(e.Ref) > s.Cfg.MaxTextBytes {
			return nil, Validation("evidence ref must be 1..%d bytes of UTF-8", s.Cfg.MaxTextBytes)
		}
		if e.Note != nil && (!utf8.ValidString(*e.Note) || len(*e.Note) > s.Cfg.MaxTextBytes) {
			return nil, Validation("evidence note must be at most %d bytes of UTF-8", s.Cfg.MaxTextBytes)
		}
	}
	if data != nil {
		if b, _ := json.Marshal(data); len(b) > s.Cfg.MaxTaskResultBytes {
			return nil, limitExceeded(s.Cfg.MaxTaskResultBytes, "data exceeds %d bytes", s.Cfg.MaxTaskResultBytes)
		}
	}
	if ev == nil {
		ev = []Evidence{}
	}
	return ev, nil
}

// proofRequest is the fingerprinted form of a proof-bearing command: the
// body plus the claim ID named by the proof, never the secret (CLAIM-040).
type proofRequest struct {
	Body    any    `json:"body"`
	ClaimID string `json:"claim_id"`
}

// ---- commands ----

// taskCommand runs fn on a locked, readable task whose lapse (if any) was
// applied first (TASK-031), and returns the caller's task view.
func (s *Service) taskCommand(ctx context.Context, p *Principal, boardID, taskID string, idem *Idem, fn func(b *btx, t *taskRow) error) (*Result, error) {
	return s.boardMutation(ctx, p, boardID, idem, func(b *btx) (*Result, error) {
		t, err := b.lockTask(taskID)
		if err != nil {
			return nil, err
		}
		if err := fn(b, t); err != nil {
			return nil, err
		}
		v, err := b.readerView(t)
		if err != nil {
			return nil, err
		}
		return &Result{Status: 200, ResourceType: "task", ResourceID: t.ID, Body: v}, nil
	})
}

func checkIfMatch(t *taskRow, ifMatch *int64) error {
	if ifMatch != nil && *ifMatch != t.Revision {
		return revisionConflict(t.Revision, "task")
	}
	return nil
}

// verifyClaimant checks C (TASK-042, CLAIM-014, TASK-043) and returns the
// active claim.
func (b *btx) verifyClaimant(t *taskRow, proof *ClaimProof) (*claimRow, error) {
	if !b.policy.Has(b.chanOf(t).Path, authz.Claim) {
		return nil, forbiddenReason("missing_capability", "claim required in %q", b.chanOf(t).Path)
	}
	c, err := loadClaim(b.ctx, b, proof.ClaimID)
	if err != nil {
		var de *Error
		if errors.As(err, &de) && de.Status == 404 {
			return nil, claimLost(t)
		}
		return nil, err
	}
	if c.TaskID != t.ID || !proof.verifies(c.Verifier) {
		return nil, claimLost(t)
	}
	if c.ActorID != b.p.ActorID {
		return nil, forbiddenReason("proof_actor_mismatch", "the proof belongs to another actor")
	}
	if t.State != StateClaimed || t.CurrentClaimID == nil || *t.CurrentClaimID != c.ID || !c.activeAt(b.now) {
		return nil, claimLost(t)
	}
	return &c, nil
}

// claim performs L3 on a locked, materialized task (CLAIM-001..005).
func (b *btx) claim(t *taskRow, lease int, expectBrief *int) (*ClaimResponse, error) {
	path := b.chanOf(t).Path
	if !b.policy.Has(path, authz.Claim) {
		return nil, forbiddenReason("missing_capability", "claim required in %q", path)
	}
	if t.Assignee != nil && *t.Assignee != b.p.ActorID {
		return nil, forbiddenReason("not_assignee", "the task is assigned to another actor")
	}
	if t.State == StateClaimed {
		e := E(409, "task_claimed", "another claim is active")
		e.Extra = map[string]any{"epoch": t.Epoch}
		return nil, e
	}
	if t.State != StateReady {
		return nil, invalidTransition(t.State, "", "a %s task cannot be claimed", t.State)
	}
	if expectBrief != nil && *expectBrief != t.BriefRevision {
		e := E(412, "revision_conflict", "brief revision is %d", t.BriefRevision)
		e.Extra = map[string]any{"revision": t.BriefRevision}
		return nil, e
	}
	secret, err := newProofSecret()
	if err != nil {
		return nil, err
	}
	c := claimRow{ClaimView: ClaimView{ID: ids.New("clm"), TaskID: t.ID, ActorID: b.p.ActorID, ExecutionID: ids.New("exe"), Epoch: t.Epoch + 1,
		BriefRevision: t.BriefRevision, Status: "active", Revision: 1, LeaseSeconds: lease, AcquiredAt: b.now,
		ExpiresAt: b.now.Add(time.Duration(lease) * time.Second)}, BoardID: b.board.ID, CredentialID: b.p.CredentialID, Verifier: proofVerifier(secret)}
	if b.p.RunID != "" {
		run := b.p.RunID
		c.RunID = &run
	}
	if kr := b.s.ClaimKeys; kr != nil {
		ct, kid, err := kr.seal(secret, claimAAD(b.board.ID, t.ID, c.ID, c.ActorID, c.Epoch))
		if err != nil {
			return nil, err
		}
		c.Ciphertext, c.KeyID = ct, &kid
	}
	if _, err := b.Exec(b.ctx, `INSERT INTO claims(id, board_id, task_id, actor_id, credential_id, run_id, execution_id, epoch, brief_revision, lease_seconds,
		status, revision, acquired_at, expires_at, proof_verifier, proof_ciphertext, proof_key_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'active',1,$11,$12,$13,$14,$15)`,
		c.ID, c.BoardID, c.TaskID, c.ActorID, c.CredentialID, c.RunID, c.ExecutionID, c.Epoch, c.BriefRevision, c.LeaseSeconds,
		c.AcquiredAt, c.ExpiresAt, c.Verifier, c.Ciphertext, c.KeyID); err != nil {
		return nil, err
	}
	before := t.State
	t.State, t.Epoch, t.CurrentClaimID, t.ClaimExpiresAt = StateClaimed, c.Epoch, &c.ID, &c.ExpiresAt
	b.touch(t, true)
	if err := b.saveTask(t); err != nil {
		return nil, err
	}
	if err := b.emitTask(t, taskEvent{Type: "task.claimed", StateBefore: before, TVI: true, Extra: map[string]any{"claim": c.ClaimView}}); err != nil {
		return nil, err
	}
	tv, err := b.readerView(t)
	if err != nil {
		return nil, err
	}
	brief, err := briefSummary(b.ctx, b, t.ID, c.BriefRevision)
	if err != nil {
		return nil, err
	}
	proof := proofPrefix + c.ID + "." + secret
	return &ClaimResponse{Outcome: "claimed", Claim: c.ClaimView, Proof: &proof, Brief: brief, Task: tv}, nil
}

func briefSummary(ctx context.Context, q querier, taskID string, rev int) (BriefSummary, error) {
	bs := BriefSummary{Revision: rev}
	err := q.QueryRow(ctx, `SELECT text, redacted_at IS NOT NULL FROM task_briefs WHERE task_id=$1 AND revision=$2`, taskID, rev).Scan(&bs.Text, &bs.Redacted)
	return bs, err
}

// ClaimTask is L3 (POST …/tasks/{id}/claims).
func (s *Service) ClaimTask(ctx context.Context, p *Principal, boardID, taskID string, idem *Idem, req ClaimRequest) (*Result, error) {
	lease, err := s.leaseSeconds(req.LeaseSeconds)
	if err != nil {
		return nil, err
	}
	idem.Request = struct {
		LeaseSeconds        int  `json:"lease_seconds"`
		ExpectBriefRevision *int `json:"expect_brief_revision"`
	}{lease, req.ExpectBriefRevision}
	return s.boardMutation(ctx, p, boardID, idem, func(b *btx) (*Result, error) {
		t, err := b.lockTask(taskID)
		if err != nil {
			return nil, err
		}
		resp, err := b.claim(t, lease, req.ExpectBriefRevision)
		if err != nil {
			return nil, err
		}
		return &Result{Status: 201, ResourceType: "claim", ResourceID: resp.Claim.ID, Body: resp}, nil
	})
}

// ReplayClaim regenerates a claim response for an idempotent replay
// (CLAIM-041): the original claim, never a new one, no event, no lease
// change; the proof only if replay is enabled, the claim is active now, and
// the caller still holds read and claim.
func (s *Service) ReplayClaim(ctx context.Context, p *Principal, boardID, claimID string) (*ClaimResponse, error) {
	c, err := loadClaim(ctx, s.DB, claimID)
	if err != nil {
		return nil, err
	}
	v, row, err := s.readableTask(ctx, p, boardID, c.TaskID)
	if err != nil {
		return nil, err
	}
	now := s.Now()
	tv, err := buildTaskView(ctx, s.DB, row, v.chans, v.reader(), now, nil)
	if err != nil {
		return nil, err
	}
	brief, err := briefSummary(ctx, s.DB, c.TaskID, c.BriefRevision)
	if err != nil {
		return nil, err
	}
	resp := &ClaimResponse{Outcome: "claimed", Claim: c.effective(now), Brief: brief, Task: tv, ProofUnavailable: true}
	path := chanByID(v.chans, row.ChannelID).Path
	if s.ClaimKeys != nil && c.activeAt(now) && c.Ciphertext != nil && c.KeyID != nil &&
		v.policy.Has(path, authz.Read) && v.policy.Has(path, authz.Claim) {
		secret, err := s.ClaimKeys.open(c.Ciphertext, *c.KeyID, claimAAD(boardID, c.TaskID, c.ID, c.ActorID, c.Epoch))
		if err == nil {
			proof := proofPrefix + c.ID + "." + secret
			resp.Proof, resp.ProofUnavailable = &proof, false
		}
	}
	return resp, nil
}

// RenewClaim is L4 (CLAIM-023).
func (s *Service) RenewClaim(ctx context.Context, p *Principal, boardID, taskID, claimID string, proof *ClaimProof, idem *Idem, req RenewRequest) (*Result, error) {
	lease, err := s.leaseSeconds(req.LeaseSeconds)
	if err != nil {
		return nil, err
	}
	idem.Request = proofRequest{Body: map[string]any{"lease_seconds": lease, "claim_id": claimID}, ClaimID: proof.ClaimID}
	return s.boardMutation(ctx, p, boardID, idem, func(b *btx) (*Result, error) {
		t, err := b.lockTask(taskID)
		if err != nil {
			return nil, err
		}
		c, err := b.verifyClaimant(t, proof)
		if err != nil {
			return nil, err
		}
		if c.ID != claimID {
			return nil, claimLost(t)
		}
		c.ExpiresAt = b.now.Add(time.Duration(lease) * time.Second)
		c.Revision++
		c.LeaseSeconds = lease
		if _, err := b.Exec(b.ctx, `UPDATE claims SET expires_at=$2, revision=$3, lease_seconds=$4 WHERE id=$1`, c.ID, c.ExpiresAt, c.Revision, lease); err != nil {
			return nil, err
		}
		t.ClaimExpiresAt = &c.ExpiresAt
		if err := b.saveTask(t); err != nil {
			return nil, err
		}
		view, err := b.storedView(t, nil)
		if err != nil {
			return nil, err
		}
		stored, err := b.storedRootLabels(t.RootMessageID)
		if err != nil {
			return nil, err
		}
		stored[TaskStateLabel] = t.State
		if _, err := b.emit(PendingEvent{Type: "task.claim_renewed", ChannelID: t.ChannelID, ThreadID: t.ThreadID, ResourceType: "claim", ResourceID: c.ID,
			Revision: c.Revision, Payload: map[string]any{"task": view, "claim": c.ClaimView},
			Facets: []events.Facet{{ThreadScope: true, ChannelPath: b.chanOf(t).Path, ThreadID: t.ThreadID, RootLabels: stored}}}); err != nil {
			return nil, err
		}
		return &Result{Status: 200, ResourceType: "claim_renewal", ResourceID: c.ID, Body: c.ClaimView}, nil
	})
}

// ReleaseTask is L5.
func (s *Service) ReleaseTask(ctx context.Context, p *Principal, boardID, taskID string, proof *ClaimProof, idem *Idem, req ReasonRequest) (*Result, error) {
	reason, err := s.reasonText(req.Reason, false, "release")
	if err != nil {
		return nil, err
	}
	idem.Request = proofRequest{Body: ReasonRequest{Reason: reason}, ClaimID: proof.ClaimID}
	return s.taskCommand(ctx, p, boardID, taskID, idem, func(b *btx, t *taskRow) error {
		if _, err := b.verifyClaimant(t, proof); err != nil {
			return err
		}
		ended, err := b.endClaim(t, "released")
		if err != nil {
			return err
		}
		t.State = StateReady
		b.touch(t, true)
		if err := b.saveTask(t); err != nil {
			return err
		}
		return b.emitTask(t, taskEvent{Type: "task.released", StateBefore: StateClaimed, TVI: true, Extra: map[string]any{"claim": ended, "reason": reason}})
	})
}

// CompleteTask is L7 with synchronous dependent activation (L14, DEP-007).
func (s *Service) CompleteTask(ctx context.Context, p *Principal, boardID, taskID string, proof *ClaimProof, idem *Idem, req CompleteRequest) (*Result, error) {
	res := req.Result
	if res == nil {
		res = &TaskResult{}
	}
	if res.Summary != nil {
		if err := s.validateText(*res.Summary); err != nil {
			return nil, err
		}
	}
	ev, err := s.validateEvidence(res.Evidence, res.Data)
	if err != nil {
		return nil, err
	}
	res.Evidence = ev
	idem.Request = proofRequest{Body: CompleteRequest{Result: res}, ClaimID: proof.ClaimID}
	return s.taskCommand(ctx, p, boardID, taskID, idem, func(b *btx, t *taskRow) error {
		if _, err := b.verifyClaimant(t, proof); err != nil {
			return err
		}
		return b.settle(t, StateCompleted, nil, res.Summary, res.Evidence, res.Data)
	})
}

// FailTask is L8.
func (s *Service) FailTask(ctx context.Context, p *Principal, boardID, taskID string, proof *ClaimProof, idem *Idem, req FailRequest) (*Result, error) {
	reason, err := s.reasonText(req.Reason, true, "fail")
	if err != nil {
		return nil, err
	}
	ev, err := s.validateEvidence(req.Evidence, req.Data)
	if err != nil {
		return nil, err
	}
	req.Reason, req.Evidence = reason, ev
	idem.Request = proofRequest{Body: req, ClaimID: proof.ClaimID}
	return s.taskCommand(ctx, p, boardID, taskID, idem, func(b *btx, t *taskRow) error {
		if _, err := b.verifyClaimant(t, proof); err != nil {
			return err
		}
		return b.settle(t, StateFailed, reason, nil, req.Evidence, req.Data)
	})
}

// settle ends the active claim as completed or failed and records the
// outcome. Evidence is recorded as given; nothing is inferred (CLAIM-032).
func (b *btx) settle(t *taskRow, state string, reason, summary *string, ev []Evidence, data map[string]any) error {
	ended, err := b.endClaim(t, state)
	if err != nil {
		return err
	}
	out := Outcome{Kind: state, Reason: reason, Summary: summary, Evidence: ev, Data: data, ByActorID: b.p.ActorID,
		ClaimID: &ended.ID, ExecutionID: &ended.ExecutionID, At: b.now}
	t.Outcome = jsonOf(out)
	t.State = state
	b.touch(t, true)
	if err := b.saveTask(t); err != nil {
		return err
	}
	if err := b.emitTask(t, taskEvent{Type: "task." + state, StateBefore: StateClaimed, TVI: true, Extra: map[string]any{"claim": ended, "outcome": out}}); err != nil {
		return err
	}
	if state == StateCompleted {
		return b.activateDependents(t)
	}
	return nil
}

// activateDependents moves every waiting dependent whose prerequisites are
// now all completed to ready, in this transaction (DEP-007). Blocked
// dependents stay blocked.
func (b *btx) activateDependents(prereq *taskRow) error {
	rows, err := b.Query(b.ctx, `SELECT d.task_id FROM task_dependencies d JOIN tasks t ON t.id = d.task_id
		WHERE d.board_id=$1 AND d.depends_on_task_id=$2 AND t.state='waiting' ORDER BY t.created_position, t.id`, b.board.ID, prereq.ID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range ids {
		d, err := loadTaskRow(b.ctx, b, b.board.ID, id, true)
		if err != nil {
			return err
		}
		ok, err := b.prereqsDone(d.ID)
		if err != nil || !ok {
			return err
		}
		d.State = StateReady
		b.touch(&d, true)
		if err := b.saveTask(&d); err != nil {
			return err
		}
		if err := b.emitTask(&d, taskEvent{Type: "task.became_ready", StateBefore: StateWaiting, TVI: true, Extra: map[string]any{
			"cause": "dependencies_satisfied", "prerequisite_task_id": prereq.ID, "prerequisite_channel_id": prereq.ChannelID}}); err != nil {
			return err
		}
	}
	return nil
}

// BlockTask is L9/L10. With a proof it is a claimant block; without, the
// caller must be a manager (ADR-0016 §1).
func (s *Service) BlockTask(ctx context.Context, p *Principal, boardID, taskID string, proof *ClaimProof, ifMatch *int64, idem *Idem, req ReasonRequest) (*Result, error) {
	reason, err := s.reasonText(req.Reason, true, "block")
	if err != nil {
		return nil, err
	}
	if proof != nil {
		idem.Request = proofRequest{Body: ReasonRequest{Reason: reason}, ClaimID: proof.ClaimID}
	} else {
		idem.Request = ReasonRequest{Reason: reason}
	}
	return s.taskCommand(ctx, p, boardID, taskID, idem, func(b *btx, t *taskRow) error {
		cause := "manual"
		if proof != nil {
			if _, err := b.verifyClaimant(t, proof); err != nil {
				return err
			}
			cause = "claimant"
		} else {
			if err := b.requireManager(t); err != nil {
				return err
			}
			if err := checkIfMatch(t, ifMatch); err != nil {
				return err
			}
			if t.State != StateReady && t.State != StateWaiting && t.State != StateClaimed {
				return invalidTransition(t.State, "", "a %s task cannot be blocked", t.State)
			}
		}
		return b.block(t, *reason, cause, "task.blocked", nil)
	})
}

// block sets blocked, revoking any claim (TASK-034), and emits evType.
func (b *btx) block(t *taskRow, reason, cause, evType string, extra map[string]any) error {
	before := t.State
	revoked, err := b.endClaim(t, "revoked")
	if err != nil {
		return err
	}
	t.Block = jsonOf(BlockInfo{Reason: reason, Cause: cause, ByActorID: b.p.ActorID, At: b.now})
	t.State = StateBlocked
	b.touch(t, before != StateBlocked)
	if err := b.saveTask(t); err != nil {
		return err
	}
	if extra == nil {
		extra = map[string]any{"reason": reason, "cause": cause}
	}
	extra["revoked_claim"] = revoked
	return b.emitTask(t, taskEvent{Type: evType, StateBefore: before, TVI: true, Extra: extra})
}

// UnblockTask is L11.
func (s *Service) UnblockTask(ctx context.Context, p *Principal, boardID, taskID string, ifMatch *int64, idem *Idem, req ReasonRequest) (*Result, error) {
	reason, err := s.reasonText(req.Reason, false, "unblock")
	if err != nil {
		return nil, err
	}
	idem.Request = ReasonRequest{Reason: reason}
	return s.taskCommand(ctx, p, boardID, taskID, idem, func(b *btx, t *taskRow) error {
		if err := b.requireManager(t); err != nil {
			return err
		}
		if err := checkIfMatch(t, ifMatch); err != nil {
			return err
		}
		if t.State != StateBlocked {
			return invalidTransition(t.State, "", "only a blocked task can be unblocked")
		}
		ok, err := b.prereqsDone(t.ID)
		if err != nil {
			return err
		}
		t.State = StateWaiting
		if ok {
			t.State = StateReady
		}
		t.Block = nil
		b.touch(t, true)
		if err := b.saveTask(t); err != nil {
			return err
		}
		return b.emitTask(t, taskEvent{Type: "task.unblocked", StateBefore: StateBlocked, TVI: true, Extra: map[string]any{"reason": reason}})
	})
}

// CancelTask is L12.
func (s *Service) CancelTask(ctx context.Context, p *Principal, boardID, taskID string, ifMatch *int64, idem *Idem, req ReasonRequest) (*Result, error) {
	reason, err := s.reasonText(req.Reason, true, "cancel")
	if err != nil {
		return nil, err
	}
	idem.Request = ReasonRequest{Reason: reason}
	return s.taskCommand(ctx, p, boardID, taskID, idem, func(b *btx, t *taskRow) error {
		if err := b.requireManager(t); err != nil {
			return err
		}
		if err := checkIfMatch(t, ifMatch); err != nil {
			return err
		}
		if !nonterminal(t.State) {
			return invalidTransition(t.State, "", "a %s task cannot be canceled", t.State)
		}
		return b.cancel(t, reason, "manual")
	})
}

func (b *btx) cancel(t *taskRow, reason *string, cause string) error {
	before := t.State
	revoked, err := b.endClaim(t, "revoked")
	if err != nil {
		return err
	}
	out := Outcome{Kind: StateCanceled, Reason: reason, Evidence: []Evidence{}, ByActorID: b.p.ActorID, At: b.now}
	if revoked != nil {
		out.ClaimID, out.ExecutionID = &revoked.ID, &revoked.ExecutionID
	}
	t.Outcome = jsonOf(out)
	t.State = StateCanceled
	b.touch(t, true)
	if err := b.saveTask(t); err != nil {
		return err
	}
	return b.emitTask(t, taskEvent{Type: "task.canceled", StateBefore: before, TVI: true,
		Extra: map[string]any{"reason": reason, "cause": cause, "revoked_claim": revoked}})
}

// ReopenTask is L13 (TASK-036, BRIEF-022).
func (s *Service) ReopenTask(ctx context.Context, p *Principal, boardID, taskID string, ifMatch *int64, idem *Idem, req ReasonRequest) (*Result, error) {
	reason, err := s.reasonText(req.Reason, false, "reopen")
	if err != nil {
		return nil, err
	}
	idem.Request = ReasonRequest{Reason: reason}
	return s.taskCommand(ctx, p, boardID, taskID, idem, func(b *btx, t *taskRow) error {
		if err := b.requireManager(t); err != nil {
			return err
		}
		if err := checkIfMatch(t, ifMatch); err != nil {
			return err
		}
		if t.State != StateFailed && t.State != StateCanceled {
			hint := ""
			if t.State == StateCompleted {
				hint = "a completed task is final; create a follow-up task"
			}
			return invalidTransition(t.State, hint, "only a failed or canceled task can be reopened")
		}
		if t.RootRedactedAt != nil {
			return invalidTransition(t.State, "the root was redacted; create a new task", "a task whose root was redacted cannot be reopened")
		}
		ok, err := b.prereqsDone(t.ID)
		if err != nil {
			return err
		}
		before, prev := t.State, nullJSON(t.Outcome)
		t.State = StateWaiting
		if ok {
			t.State = StateReady
		}
		t.Outcome, t.Block = nil, nil
		t.RetryCount++
		b.touch(t, true)
		if err := b.saveTask(t); err != nil {
			return err
		}
		return b.emitTask(t, taskEvent{Type: "task.reopened", StateBefore: before, TVI: true, Extra: map[string]any{"reason": reason, "previous_outcome": prev}})
	})
}

// SetAssignee is L16 (TASK-050..054).
func (s *Service) SetAssignee(ctx context.Context, p *Principal, boardID, taskID string, ifMatch int64, idem *Idem, req AssigneeRequest) (*Result, error) {
	if req.AssigneeActorID != nil && *req.AssigneeActorID == "" {
		return nil, Validation("assignee_actor_id must be an actor ID or null")
	}
	if idem != nil {
		idem.Request = struct {
			A *string `json:"assignee_actor_id"`
			M int64   `json:"if_match"`
		}{req.AssigneeActorID, ifMatch}
	}
	return s.taskCommand(ctx, p, boardID, taskID, idem, func(b *btx, t *taskRow) error {
		if err := b.requireManager(t); err != nil {
			return err
		}
		if err := checkIfMatch(t, &ifMatch); err != nil {
			return err
		}
		if !nonterminal(t.State) {
			return invalidTransition(t.State, "reopen a failed or canceled task first", "a %s task cannot be reassigned", t.State)
		}
		if t.State == StateClaimed {
			return claimActive(t)
		}
		if err := b.checkAssignee(req.AssigneeActorID, b.chanOf(t).Path); err != nil {
			return err
		}
		before := t.Assignee
		t.Assignee = req.AssigneeActorID
		b.touch(t, false)
		if err := b.saveTask(t); err != nil {
			return err
		}
		return b.emitTask(t, taskEvent{Type: "task.assigned", StateBefore: t.State, Extra: map[string]any{"assignee_before": before, "assignee_after": t.Assignee}})
	})
}

func claimActive(t *taskRow) *Error {
	e := E(409, "task_claim_active", "a claim is active; block the task first to revoke it")
	e.Extra = map[string]any{"claim_expires_at": t.ClaimExpiresAt}
	return e
}

// SetDependencies is L15 (DEP-002..006).
func (s *Service) SetDependencies(ctx context.Context, p *Principal, boardID, taskID string, ifMatch int64, idem *Idem, req DependenciesRequest) (*Result, error) {
	if req.TaskIDs == nil {
		return nil, Validation("task_ids is required (use [] to remove every readable prerequisite)")
	}
	want, err := s.validateDeps(req.TaskIDs)
	if err != nil {
		return nil, err
	}
	if idem != nil {
		idem.Request = struct {
			T []string `json:"task_ids"`
			M int64    `json:"if_match"`
		}{want, ifMatch}
	}
	return s.taskCommand(ctx, p, boardID, taskID, idem, func(b *btx, t *taskRow) error {
		if err := b.requireManager(t); err != nil {
			return err
		}
		if err := checkIfMatch(t, &ifMatch); err != nil {
			return err
		}
		if !nonterminal(t.State) {
			return invalidTransition(t.State, "", "dependencies of a %s task cannot change", t.State)
		}
		if t.State == StateClaimed {
			return claimActive(t)
		}
		if slices.Contains(want, t.ID) {
			e := E(409, "dependency_cycle", "a task cannot depend on itself")
			e.Extra = map[string]any{"cycle": []string{t.ID, t.ID}}
			return e
		}
		added, err := b.resolvePrereqs(want)
		if err != nil {
			return err
		}
		rows, err := b.Query(b.ctx, `SELECT d.depends_on_task_id, p.channel_id FROM task_dependencies d JOIN tasks p ON p.id=d.depends_on_task_id WHERE d.task_id=$1`, t.ID)
		if err != nil {
			return err
		}
		cur := map[string]string{}
		for rows.Next() {
			var id, ch string
			if err := rows.Scan(&id, &ch); err != nil {
				rows.Close()
				return err
			}
			cur[id] = ch
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		// New set: requested readable IDs plus every unreadable existing edge
		// (DEP-005: what the caller cannot see is kept unchanged).
		next := map[string]string{}
		for _, d := range added {
			next[d.TaskID] = d.ChannelID
		}
		for id, ch := range cur {
			if !b.canReadChan(ch) {
				next[id] = ch
			}
		}
		if len(next) > s.Cfg.MaxDependenciesPerTask {
			return limitExceeded(s.Cfg.MaxDependenciesPerTask, "at most %d prerequisites per task", s.Cfg.MaxDependenciesPerTask)
		}
		if cyc, err := b.cycleThrough(t.ID, next); err != nil || cyc != nil {
			if err != nil {
				return err
			}
			var readable []string
			for _, id := range cyc {
				var ch string
				if err := b.QueryRow(b.ctx, `SELECT channel_id FROM tasks WHERE id=$1`, id).Scan(&ch); err == nil && b.canReadChan(ch) {
					readable = append(readable, id)
				}
			}
			e := E(409, "dependency_cycle", "the change would create a dependency cycle")
			e.Extra = map[string]any{"cycle": readable}
			return e
		}
		var addRefs, remRefs []DependencyView
		for id, ch := range next {
			if _, ok := cur[id]; !ok {
				addRefs = append(addRefs, DependencyView{TaskID: id, ChannelID: ch})
				if err := b.insertDeps(t.ID, []string{id}); err != nil {
					return err
				}
			}
		}
		for id, ch := range cur {
			if _, ok := next[id]; !ok {
				remRefs = append(remRefs, DependencyView{TaskID: id, ChannelID: ch})
				if _, err := b.Exec(b.ctx, `DELETE FROM task_dependencies WHERE task_id=$1 AND depends_on_task_id=$2`, t.ID, id); err != nil {
					return err
				}
			}
		}
		if len(addRefs) == 0 && len(remRefs) == 0 {
			return nil
		}
		sortDeps(addRefs)
		sortDeps(remRefs)
		before := t.State
		if t.State != StateBlocked {
			ok, err := b.prereqsDone(t.ID)
			if err != nil {
				return err
			}
			t.State = StateWaiting
			if ok {
				t.State = StateReady
			}
		}
		b.touch(t, before != t.State)
		if err := b.saveTask(t); err != nil {
			return err
		}
		return b.emitTask(t, taskEvent{Type: "task.dependencies_changed", StateBefore: before, TVI: true, Extra: map[string]any{
			"added": nonNilDeps(addRefs), "removed": nonNilDeps(remRefs), "state_before": before, "state_after": t.State}})
	})
}

func sortDeps(d []DependencyView) {
	slices.SortFunc(d, func(a, b DependencyView) int { return strings.Compare(a.TaskID, b.TaskID) })
}

func nonNilDeps(d []DependencyView) []DependencyView {
	if d == nil {
		return []DependencyView{}
	}
	return d
}

// cycleThrough checks the whole board graph, including unreadable tasks
// (DEP-002), with task's edges replaced by next. It returns a path
// task -> ... -> task, or nil. Runs under the board lock (ADR-0008).
func (b *btx) cycleThrough(task string, next map[string]string) ([]string, error) {
	rows, err := b.Query(b.ctx, `SELECT task_id, depends_on_task_id FROM task_dependencies WHERE board_id=$1`, b.board.ID)
	if err != nil {
		return nil, err
	}
	adj := map[string][]string{}
	for rows.Next() {
		var a, d string
		if err := rows.Scan(&a, &d); err != nil {
			rows.Close()
			return nil, err
		}
		if a != task {
			adj[a] = append(adj[a], d)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for id := range next {
		adj[task] = append(adj[task], id)
	}
	// DFS from task looking for task.
	parent := map[string]string{}
	seen := map[string]bool{}
	stack := []string{task}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, d := range adj[n] {
			if d == task {
				path := []string{task}
				for x := n; x != task; x = parent[x] {
					path = append(path, x)
				}
				slices.Reverse(path[1:])
				return append(path, task), nil
			}
			if !seen[d] {
				seen[d] = true
				parent[d] = n
				stack = append(stack, d)
			}
		}
	}
	return nil, nil
}

// PatchTask is L19: title (via the thread) and owner.
func (s *Service) PatchTask(ctx context.Context, p *Principal, boardID, taskID string, ifMatch int64, req PatchTaskRequest) (*TaskView, error) {
	if req.Title == nil && req.OwnerActorID == nil {
		return nil, Validation("at least one of title or owner_actor_id is required")
	}
	if req.Title != nil {
		if err := validName(*req.Title, s.Cfg.MaxTitleBytes); err != nil {
			return nil, Validation("title must be 1..%d bytes of UTF-8", s.Cfg.MaxTitleBytes)
		}
	}
	res, err := s.taskCommand(ctx, p, boardID, taskID, nil, func(b *btx, t *taskRow) error {
		if err := b.requireManager(t); err != nil {
			return err
		}
		if err := checkIfMatch(t, &ifMatch); err != nil {
			return err
		}
		if t.State == StateCompleted {
			return invalidTransition(t.State, "a completed task is final; create a follow-up task", "a completed task cannot be edited")
		}
		var changed []string
		if req.Title != nil && *req.Title != t.Title {
			th, err := loadThread(b.ctx, b, b.board.ID, t.ThreadID, b.chans, true)
			if err != nil {
				return err
			}
			th.Title, th.Revision, th.UpdatedAt = *req.Title, th.Revision+1, b.now
			if _, err := b.Exec(b.ctx, `UPDATE threads SET title=$2, revision=$3, updated_at=$4 WHERE id=$1`, th.ID, th.Title, th.Revision, b.now); err != nil {
				return err
			}
			if _, err := b.emit(PendingEvent{Type: "thread.updated", ChannelID: th.ChannelID, ThreadID: th.ID, ResourceType: "thread", ResourceID: th.ID, Revision: th.Revision,
				Payload: map[string]any{"thread": th}, Facets: []events.Facet{{ThreadScope: true, ChannelPath: th.ChannelPath, ThreadID: th.ID, RootLabels: th.Labels}}}); err != nil {
				return err
			}
			t.Title = *req.Title
			changed = append(changed, "title")
		}
		if req.OwnerActorID != nil && *req.OwnerActorID != t.Owner {
			ok, err := b.actorCanRead(*req.OwnerActorID, b.chanOf(t).Path)
			if err != nil {
				return err
			}
			if !ok {
				return Validation("owner_actor_id %q is not an actor who can read this channel", *req.OwnerActorID)
			}
			t.Owner = *req.OwnerActorID
			changed = append(changed, "owner_actor_id")
		}
		if len(changed) == 0 {
			return nil
		}
		b.touch(t, false)
		if err := b.saveTask(t); err != nil {
			return err
		}
		return b.emitTask(t, taskEvent{Type: "task.updated", StateBefore: t.State, Extra: map[string]any{"changed": changed, "title": t.Title, "owner_actor_id": t.Owner}})
	})
	if err != nil {
		return nil, err
	}
	v := res.Body.(TaskView)
	return &v, nil
}

// UpdateBrief is L17 (BRIEF-002..005).
func (s *Service) UpdateBrief(ctx context.Context, p *Principal, boardID, taskID string, ifMatch int64, idem *Idem, req BriefUpdateRequest) (*Result, error) {
	if strings.TrimSpace(req.Text) == "" {
		return nil, Validation("text must not be empty")
	}
	if err := s.validateText(req.Text); err != nil {
		return nil, err
	}
	reason, err := s.reasonText(req.Reason, false, "brief update")
	if err != nil {
		return nil, err
	}
	req.Reason = reason
	idem.Request = struct {
		R BriefUpdateRequest `json:"request"`
		M int64              `json:"if_match"`
	}{req, ifMatch}
	return s.boardMutation(ctx, p, boardID, idem, func(b *btx) (*Result, error) {
		t, err := b.lockTask(taskID)
		if err != nil {
			return nil, err
		}
		path := b.chanOf(t).Path
		if err := b.requireManager(t); err != nil {
			return nil, err
		}
		if !b.policy.Has(path, authz.Post) {
			return nil, forbiddenReason("missing_capability", "post required in %q", path)
		}
		if err := checkIfMatch(t, &ifMatch); err != nil {
			return nil, err
		}
		switch t.State {
		case StateCompleted:
			return nil, invalidTransition(t.State, "a completed task is final; create a follow-up task", "the brief of a completed task cannot change")
		case StateFailed, StateCanceled:
			return nil, invalidTransition(t.State, "reopen the task first", "the brief of a %s task cannot change", t.State)
		}
		rev := t.BriefRevision + 1
		// Notice reply: states the revision and reason, never the text
		// (BRIEF-003, spec §21 note 3).
		text := fmt.Sprintf("Brief updated to revision %d.", rev)
		if reason != nil {
			text += " Reason: " + *reason
		}
		if len(text) > s.Cfg.MaxTextBytes {
			text = text[:s.Cfg.MaxTextBytes]
			for !utf8.ValidString(text) {
				text = text[:len(text)-1]
			}
		}
		th, err := loadThread(b.ctx, b, b.board.ID, t.ThreadID, b.chans, true)
		if err != nil {
			return nil, err
		}
		var lastSeq int64
		if err := b.QueryRow(b.ctx, `SELECT last_seq FROM threads WHERE id=$1`, t.ThreadID).Scan(&lastSeq); err != nil {
			return nil, err
		}
		labels := map[string]string{BriefRevisionLabel: fmt.Sprint(rev)}
		m := MessageView{ID: ids.New("msg"), BoardID: b.board.ID, ThreadID: t.ThreadID, ChannelID: t.ChannelID, ChannelPath: path, Seq: lastSeq + 1,
			AuthorActorID: b.p.ActorID, Text: &text, Labels: labels, Mentions: []string{}, Revision: 1, CreatedAt: b.now}
		if err := b.insertMessage(&m, labels); err != nil {
			return nil, err
		}
		if _, err := b.Exec(b.ctx, `UPDATE threads SET last_seq=$2, last_message_id=$3, message_count=message_count+1, activity_position=$4, updated_at=$5 WHERE id=$1`,
			t.ThreadID, m.Seq, m.ID, b.head+1, b.now); err != nil {
			return nil, err
		}
		if _, err := b.emit(PendingEvent{Type: "message.created", ChannelID: t.ChannelID, ThreadID: t.ThreadID, ResourceType: "message", ResourceID: m.ID, Revision: 1,
			Payload: messageEventPayload(m, th, nil), Facets: []events.Facet{m.facet(th.Labels)}}); err != nil {
			return nil, err
		}
		if _, err := b.Exec(b.ctx, `INSERT INTO task_briefs(board_id, task_id, revision, text, source_kind, source_message_id, source_message_revision, reason, created_by, created_at)
			VALUES ($1,$2,$3,$4,'brief_update',$5,1,$6,$7,$8)`, b.board.ID, t.ID, rev, req.Text, m.ID, reason, b.p.ActorID, b.now); err != nil {
			return nil, err
		}
		stateBefore := t.State
		t.BriefRevision = rev
		blockReason := fmt.Sprintf("brief updated to revision %d", rev)
		if reason != nil {
			blockReason = *reason
		}
		if err := b.block(t, blockReason, "brief_updated", "task.brief_updated", map[string]any{
			"brief_revision": rev, "message_id": m.ID, "state_before": stateBefore}); err != nil {
			return nil, err
		}
		ref := t.ID + "/" + fmt.Sprint(rev)
		bv, err := scanBrief(b.QueryRow(b.ctx, `SELECT `+briefCols+` FROM task_briefs WHERE task_id=$1 AND revision=$2`, t.ID, rev))
		if err != nil {
			return nil, err
		}
		tv, err := b.readerView(t)
		if err != nil {
			return nil, err
		}
		bv.Task = &tv
		return &Result{Status: 201, ResourceType: "brief", ResourceID: ref, Body: bv}, nil
	})
}

// RedactRoot is L18 (BRIEF-020..022).
func (s *Service) RedactRoot(ctx context.Context, p *Principal, boardID, taskID string, ifMatch int64, idem *Idem, req ReasonRequest) (*Result, error) {
	reason, err := s.reasonText(req.Reason, false, "root redaction")
	if err != nil {
		return nil, err
	}
	idem.Request = struct {
		R *string `json:"reason"`
		M int64   `json:"if_match"`
	}{reason, ifMatch}
	return s.taskCommand(ctx, p, boardID, taskID, idem, func(b *btx, t *taskRow) error {
		path := b.chanOf(t).Path
		if err := b.requireManager(t); err != nil {
			return err
		}
		root, err := loadMessage(b.ctx, b, b.board.ID, t.RootMessageID, b.chans, true)
		if err != nil {
			return err
		}
		kind := "author"
		if root.AuthorActorID == p.ActorID {
			if !b.policy.Has(path, authz.Post) {
				return forbiddenReason("missing_capability", "post required to delete your own root")
			}
		} else {
			if !b.policy.IsBoardAdmin() {
				return forbiddenReason("missing_capability", "redacting another actor's root requires board_admin")
			}
			if reason == nil {
				return Validation("redaction requires a reason")
			}
			kind = "redaction"
		}
		if root.Deleted != nil && t.RootRedactedAt != nil {
			return nil // already redacted: the current task view
		}
		if root.Revision != ifMatch {
			return revisionConflict(root.Revision, "root message")
		}
		r := ""
		if reason != nil {
			r = *reason
		}
		if _, err := b.tombstoneMessage(root, kind, r); err != nil {
			return err
		}
		res, err := b.Query(b.ctx, `UPDATE task_briefs SET text=NULL, redacted_at=$2 WHERE task_id=$1 RETURNING revision`, t.ID, b.now)
		if err != nil {
			return err
		}
		revs, err := pgx.CollectRows(res, pgx.RowTo[int])
		if err != nil {
			return err
		}
		slices.Sort(revs)
		now := b.now
		t.RootRedactedAt = &now
		if nonterminal(t.State) {
			if err := b.cancel(t, reason, "root_redacted"); err != nil {
				return err
			}
		} else {
			b.touch(t, false)
			if err := b.saveTask(t); err != nil {
				return err
			}
			if err := b.emitTask(t, taskEvent{Type: "task.brief_redacted", StateBefore: t.State, Extra: map[string]any{"brief_revisions": revs}}); err != nil {
				return err
			}
		}
		// Open snapshot sessions that hold the task get the new view in this
		// transaction, as P1 does for deleted messages (ADR-0016 §5).
		view, err := b.storedView(t, nil)
		if err != nil {
			return err
		}
		_, err = b.Exec(b.ctx, `UPDATE snapshot_items SET payload=$2 WHERE resource_type='task' AND resource_id=$1`, t.ID, jsonOf(view))
		return err
	})
}

// ---- claim-next (§10, ADR-0016 §6) ----

var errNoWork = errors.New("no work")

// NormalizeClaimNext validates and applies defaults (fingerprint form).
func (s *Service) NormalizeClaimNext(req *ClaimNextRequest) error {
	for _, pth := range []string{req.Channel, req.Subtree} {
		if pth != "" {
			if err := authz.ValidatePath(pth); err != nil {
				return Validation("%s", err)
			}
		}
	}
	if req.Assignee == "" {
		req.Assignee = "eligible"
	}
	if !slices.Contains([]string{"eligible", "me", "unassigned"}, req.Assignee) {
		return Validation("assignee must be eligible, me, or unassigned")
	}
	lease, err := s.leaseSeconds(req.LeaseSeconds)
	if err != nil {
		return err
	}
	req.LeaseSeconds = &lease
	w := 0
	if req.WaitMs != nil {
		w = *req.WaitMs
	}
	if w < 0 || time.Duration(w)*time.Millisecond > s.Cfg.MaxClaimWait {
		return Validation("wait_ms must be 0..%d", s.Cfg.MaxClaimWait.Milliseconds())
	}
	req.WaitMs = &w
	if len(req.Labels) > 0 {
		norm := map[string][]string{}
		for k, vals := range req.Labels {
			k = strings.ToLower(k)
			if err := events.ValidateLabelKey(k, true); err != nil {
				return Validation("label %q: %s", k, err)
			}
			v := sortedUnique(append(norm[k], vals...))
			if v == nil {
				v = []string{}
			}
			norm[k] = v
		}
		req.Labels = norm
	} else {
		req.Labels = nil
	}
	return nil
}

// ClaimNext selects and claims the oldest eligible task atomically, or
// waits up to wait_ms without holding a transaction (SEL-001..008). token
// re-authenticates each attempt (AUTH-004). It returns *Result on a claim
// (or replay) and *NoWork otherwise.
func (s *Service) ClaimNext(ctx context.Context, p *Principal, token, boardID string, idem *Idem, req ClaimNextRequest) (*Result, *NoWork, error) {
	if err := s.NormalizeClaimNext(&req); err != nil {
		return nil, nil, err
	}
	idem.Request = req
	wait := time.Duration(*req.WaitMs) * time.Millisecond
	deadline := time.Now().Add(wait)
	notify, unsubscribe := s.Hub.Subscribe(boardID)
	defer unsubscribe()
	runID := p.RunID
	for iter := 0; ; iter++ {
		if iter > 0 {
			np, err := s.recheck(ctx, token)
			if err != nil {
				return nil, nil, err
			}
			np.RunID = runID
			p = np
		}
		res, err := s.claimNextOnce(ctx, p, boardID, idem, req)
		if err == nil {
			return res, nil, nil
		}
		if !errors.Is(err, errNoWork) {
			return nil, nil, err
		}
		if wait <= 0 {
			return nil, &NoWork{Outcome: "no_work"}, nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, &NoWork{Outcome: "no_work", TimedOut: true}, nil
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-notify:
		case <-time.After(min(remaining, 2*time.Second)):
		}
	}
}

func (s *Service) claimNextOnce(ctx context.Context, p *Principal, boardID string, idem *Idem, req ClaimNextRequest) (*Result, error) {
	return s.boardMutation(ctx, p, boardID, idem, func(b *btx) (*Result, error) {
		cf := events.Filter{Channel: req.Channel, Subtree: req.Subtree}
		var C []string
		for _, c := range b.chans {
			if b.policy.Has(c.Path, authz.Read) && b.policy.Has(c.Path, authz.Claim) && cf.MatchFacet(events.Facet{ChannelPath: c.Path, Invalidation: true}) {
				C = append(C, c.ID)
			}
		}
		if len(C) == 0 {
			return nil, errNoWork
		}
		assignee := map[string]string{
			"eligible":   `(tk.assignee_actor_id IS NULL OR tk.assignee_actor_id = $5)`,
			"me":         `tk.assignee_actor_id = $5`,
			"unassigned": `(tk.assignee_actor_id IS NULL AND $5::text IS NOT NULL)`,
		}[req.Assignee]
		lf := events.Filter{Labels: req.Labels}
		var cursor int64
		const window = 200
		for {
			// Fresh READ COMMITTED snapshot taken after the board lock: it
			// sees every commit of every earlier lock holder (ADR-0016 §6).
			rows, err := b.Query(b.ctx, `SELECT tk.id, tk.channel_id, tk.created_position,
				COALESCE((SELECT jsonb_object_agg(key, value) FROM message_labels WHERE message_id=th.root_message_id), '{}'::jsonb)
				FROM tasks tk JOIN threads th ON th.id = tk.thread_id
				WHERE tk.board_id=$1 AND tk.channel_id = ANY($2) AND tk.created_position > $3
				  AND (tk.state = 'ready' OR (tk.state = 'claimed' AND tk.claim_expires_at <= $4)) AND `+assignee+`
				ORDER BY tk.created_position, tk.id LIMIT $6`, b.board.ID, C, cursor, b.now, b.p.ActorID, window)
			if err != nil {
				return nil, err
			}
			type cand struct {
				id, ch string
				pos    int64
				labels map[string]string
			}
			var cands []cand
			for rows.Next() {
				var c cand
				if err := rows.Scan(&c.id, &c.ch, &c.pos, &c.labels); err != nil {
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
				cursor = c.pos
				c.labels[TaskStateLabel] = StateReady
				if !lf.MatchFacet(events.Facet{ThreadScope: true, ChannelPath: chanByID(b.chans, c.ch).Path, RootLabels: c.labels}) {
					continue
				}
				t, err := b.lockTask(c.id)
				if err != nil {
					return nil, err
				}
				resp, err := b.claim(t, *req.LeaseSeconds, nil)
				if err != nil {
					return nil, err
				}
				return &Result{Status: 201, ResourceType: "claim", ResourceID: resp.Claim.ID, Body: resp}, nil
			}
			if len(cands) < window {
				return nil, errNoWork
			}
		}
	})
}

// ---- sweeper (ADR-0016 §7) ----

// SweepClaimExpiry materializes lapsed leases, at most limit per board per
// call. It is housekeeping only: every read and operation already applies
// the effective view (CLAIM-050).
func (s *Service) SweepClaimExpiry(ctx context.Context, limit int) (int, error) {
	rows, err := s.DB.Query(ctx, `SELECT DISTINCT board_id FROM tasks WHERE state='claimed' AND claim_expires_at <= $1`, s.Now())
	if err != nil {
		return 0, err
	}
	boards, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	sys := &Principal{ActorID: "system", System: true}
	total := 0
	for _, bid := range boards {
		_, err := s.boardMutation(ctx, sys, bid, nil, func(b *btx) (*Result, error) {
			rows, err := b.Query(b.ctx, `SELECT id FROM tasks WHERE board_id=$1 AND state='claimed' AND claim_expires_at <= $2
				ORDER BY claim_expires_at LIMIT $3 FOR UPDATE`, bid, b.now, limit)
			if err != nil {
				return nil, err
			}
			ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return nil, err
			}
			for _, id := range ids {
				t, err := loadTaskRow(b.ctx, b, bid, id, true)
				if err != nil {
					return nil, err
				}
				if err := b.materialize(&t); err != nil {
					return nil, err
				}
				total++
			}
			return &Result{}, nil
		})
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
