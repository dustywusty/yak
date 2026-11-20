package main

// `abby run`: the reference managed runner (spec/p2-tasks.md §17,
// PAR-033). It claims one task, runs a subprocess while it renews the
// lease, and settles the claim from the subprocess outcome.
//
// State machine:
//
//	acquire ──no work──────────────────────────────▶ exit 7
//	   │  └─task_claimed / claim_lost / no proof───▶ exit 9
//	   ▼
//	confirm (only after a waiting claim-next: one renew to get a
//	   │     deadline from a known send time; failure ─▶ exit 9 / 10)
//	   ▼
//	running: child + renew loop (every lease/3 ± 10 %)
//	   ├─ child exit 0 ─────▶ stop renewals ─▶ complete ─▶ exit 0
//	   ├─ child exit ≠ 0 ───▶ stop renewals ─▶ fail ─────▶ exit 11
//	   │     (complete/fail rejected 400/413 ─▶ renew ─▶ release ─▶ exit 13;
//	   │      never settle without the result it was given)
//	   ├─ claim_lost/403/404/401 on renew ─▶ stop child ─▶ exit 9
//	   ├─ no renewal confirmed by deadline − margin
//	   │                    ─▶ stop child, no more writes ─▶ exit 10
//	   └─ SIGINT/SIGTERM ───▶ stop child (renewals continue)
//	                        ─▶ stop renewals ─▶ release ─▶ exit 12
//
// "Stop child" is SIGTERM to the child's process group, then SIGKILL after
// --grace (at once on a second runner signal). The local deadline is the
// monotonic send time of the first attempt of the request that produced
// the current expiry plus the granted lease (RUN-003); expires_at is never
// compared with the local clock. Every settlement retry reuses its key
// (RUN-006); each renewal has its own key (RUN-002).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/abby-protocol/abby/sdk/go/abby"
)

type runOptions struct {
	Board  string
	TaskID string // claim this task; empty = claim-next
	Next   abby.ClaimNextRequest
	Wait   time.Duration // total claim-next wait

	LeaseSeconds        int // 0 = server default
	ExpectBriefRevision *int

	Command []string
	Dir     string
	Grace   time.Duration

	Summary       string
	Evidence      []abby.Evidence
	ReleaseReason string

	Stdout, Stderr io.Writer
	Log            io.Writer

	// Signals delivers runner interrupts (SIGINT/SIGTERM).
	Signals <-chan os.Signal

	// Test hooks. unit is the length of one lease second (default 1 s);
	// jitter returns a value in [-1, 1] (default uniform random).
	unit   time.Duration
	jitter func() float64
}

type runReport struct {
	TaskID      string     `json:"task_id"`
	ClaimID     string     `json:"claim_id,omitempty"`
	ExecutionID string     `json:"execution_id,omitempty"`
	Epoch       int64      `json:"epoch,omitempty"`
	RunID       string     `json:"run_id,omitempty"`
	Outcome     string     `json:"outcome"` // completed | failed | released | result_rejected | claim_lost | lease_unconfirmed | no_work | interrupted
	ExitStatus  *int       `json:"exit_status,omitempty"`
	Renewals    int        `json:"renewals"`
	Task        *abby.Task `json:"task,omitempty"`
	Detail      string     `json:"detail,omitempty"`
	// EvidenceMapped lists result-file evidence whose kind the protocol
	// does not accept; each was sent as kind "other".
	EvidenceMapped []string `json:"evidence_mapped,omitempty"`
	// RejectedResult is the result the server refused (outcome
	// result_rejected), kept so it can be corrected and settled again.
	RejectedResult *rejectedResult `json:"rejected_result,omitempty"`
}

type rejectedResult struct {
	Settlement string          `json:"settlement"` // complete | fail
	Status     int             `json:"status"`
	Code       string          `json:"code"`
	Detail     string          `json:"detail,omitempty"`
	Summary    string          `json:"summary,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	Evidence   []abby.Evidence `json:"evidence,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
}

type runner struct {
	opt   runOptions
	acq   *abby.Tasks // SDK retries allowed (same key)
	ops   *abby.Tasks // no SDK retries; the runner retries with deadlines
	runID string
	rep   runReport

	taskID, claimID, proof string

	mu       sync.Mutex
	lease    time.Duration // granted lease, in local time
	lastSent time.Time     // send time behind the current expiry
	deadline time.Time     // lastSent + lease
	renewals int
}

var (
	errLeaseUnconfirmed = errors.New("renewal not confirmed before the local lease deadline")
)

type lostErr struct{ err error }

func (l *lostErr) Error() string { return "claim lost: " + l.err.Error() }
func (l *lostErr) Unwrap() error { return l.err }

func newRunner(c *abby.Client, opt runOptions) *runner {
	if opt.unit <= 0 {
		opt.unit = time.Second
	}
	if opt.jitter == nil {
		opt.jitter = func() float64 { return rand.Float64()*2 - 1 }
	}
	if opt.Grace <= 0 {
		opt.Grace = 10 * opt.unit
	}
	if opt.Log == nil {
		opt.Log = io.Discard
	}
	runID := c.RunID
	if runID == "" {
		runID = "run-" + strings.TrimPrefix(abby.NewIdempotencyKey(), "k-")[:16]
	}
	cc := *c
	cc.RunID = runID
	noRetry := cc
	noRetry.MaxRetries = 0
	return &runner{opt: opt, acq: cc.Tasks(opt.Board), ops: noRetry.Tasks(opt.Board), runID: runID}
}

func (r *runner) logf(format string, a ...any) {
	fmt.Fprintf(r.opt.Log, "abby run: "+format+"\n", a...)
}

func (r *runner) margin() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return max(5*r.opt.unit, r.lease/6)
}

func (r *runner) setDeadline(sent time.Time, leaseSeconds int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lease = time.Duration(leaseSeconds) * r.opt.unit
	r.lastSent = sent
	r.deadline = sent.Add(r.lease)
}

func (r *runner) snapshot() (lastSent, deadline time.Time, lease time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastSent, r.deadline, r.lease
}

func (r *runner) backoffBase() time.Duration { return r.opt.unit / 5 }

// sleep waits d or until ctx ends.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// run executes the whole runner and returns the report and an *exitErr
// (nil on success).
func (r *runner) run(ctx context.Context) (*runReport, error) {
	r.rep.RunID = r.runID
	sigs := r.opt.Signals

	// ---- acquire ----
	actx, acancel := context.WithCancel(ctx)
	acqDone := make(chan struct{})
	watchDone := make(chan struct{})
	var interruptSig os.Signal
	go func() {
		defer close(watchDone)
		select {
		case s := <-sigs:
			interruptSig = s
			acancel()
		case <-acqDone:
		}
	}()
	cr, sent, waited, err := r.acquire(actx)
	close(acqDone)
	<-watchDone
	acancel()
	if interruptSig != nil {
		r.rep.Outcome = "interrupted"
		if err == nil && cr.Claimed() && cr.ProofValue() != "" {
			// The signal raced with a successful claim: give it back.
			r.taskID, r.claimID, r.proof = cr.Claim.TaskID, cr.Claim.ID, cr.ProofValue()
			r.rep.TaskID, r.rep.ClaimID, r.rep.ExecutionID, r.rep.Epoch = r.taskID, r.claimID, cr.Claim.ExecutionID, cr.Claim.Epoch
			r.setDeadline(sent, cr.Claim.LeaseSeconds)
			if t, rerr := r.settle(ctx, "release", func(c context.Context, key string) (*abby.Task, error) {
				return r.ops.Release(c, r.taskID, r.proof, fmt.Sprintf("runner interrupted by %v before work started", interruptSig), key)
			}); rerr == nil {
				r.rep.Outcome, r.rep.Task = "released", t
			} else {
				r.logf("release failed: %v", rerr)
			}
		}
		// A claim that committed after we left lapses at expiry (SEL-008).
		return &r.rep, &exitErr{exitInterrupted, fmt.Errorf("interrupted by %v while acquiring a claim", interruptSig)}
	}
	if err != nil {
		if abby.IsTaskClaimed(err) || abby.IsClaimLost(err) {
			r.rep.Outcome = "claim_lost"
		}
		return &r.rep, err
	}
	if !cr.Claimed() {
		r.rep.Outcome = "no_work"
		return &r.rep, &exitErr{exitTimeout, errors.New("no work: no eligible task before the wait ended")}
	}
	r.taskID, r.claimID, r.proof = cr.Claim.TaskID, cr.Claim.ID, cr.ProofValue()
	r.rep.TaskID, r.rep.ClaimID, r.rep.ExecutionID, r.rep.Epoch = r.taskID, r.claimID, cr.Claim.ExecutionID, cr.Claim.Epoch
	r.rep.Task = cr.Task
	if r.proof == "" {
		// Replayed claim without proof material (CLAIM-041): we cannot act
		// as the claimant. The claim lapses at expiry.
		r.rep.Outcome = "claim_lost"
		return &r.rep, &exitErr{exitClaimLost, fmt.Errorf("claim %s returned without a proof (proof_unavailable); it will lapse at expiry", r.claimID)}
	}
	r.setDeadline(sent, cr.Claim.LeaseSeconds)
	r.logf("claimed task %s (claim %s, execution %s, epoch %d, lease %ds)", r.taskID, r.claimID, cr.Claim.ExecutionID, cr.Claim.Epoch, cr.Claim.LeaseSeconds)

	if waited {
		// The claim may have committed long after the request was sent
		// (server-side wait), so its send time says little about expiry.
		// Renew once to get a deadline from a known send time before any
		// work starts.
		_, _, lease := r.snapshot()
		if err := r.renewUntil(ctx, time.Now().Add(max(5*r.opt.unit, lease/6))); err != nil {
			return r.renewFailed(err)
		}
	}

	// ---- run the child ----
	tmp, err := os.MkdirTemp("", "abby-run-")
	if err != nil {
		return &r.rep, err
	}
	defer os.RemoveAll(tmp)
	resultFile := filepath.Join(tmp, "result.json")
	briefFile := ""
	if cr.Brief != nil && cr.Brief.Text != nil {
		briefFile = filepath.Join(tmp, "brief.md")
		if err := os.WriteFile(briefFile, []byte(*cr.Brief.Text), 0o600); err != nil {
			return &r.rep, err
		}
	}
	child, err := r.startChild(cr, resultFile, briefFile)
	if err != nil {
		// Nothing ran: give the task back.
		r.logf("cannot start command: %v; releasing", err)
		_, rerr := r.settle(ctx, "release", func(c context.Context, key string) (*abby.Task, error) {
			return r.ops.Release(c, r.taskID, r.proof, "runner could not start the command: "+err.Error(), key)
		})
		if rerr != nil {
			r.logf("release failed: %v", rerr)
		}
		return &r.rep, &exitErr{exitError, fmt.Errorf("start command: %w", err)}
	}
	started := time.Now()

	rctx, rcancel := context.WithCancel(ctx)
	renewEv := make(chan error, 1)
	renewDone := make(chan struct{})
	go func() {
		defer close(renewDone)
		if err := r.renewLoop(rctx); err != nil && rctx.Err() == nil {
			renewEv <- err
		}
	}()
	stopRenewals := func() {
		rcancel()
		<-renewDone
	}

	select {
	case <-child.done:
		stopRenewals()
		select {
		case err := <-renewEv: // the loop failed just before the child ended
			return r.renewFailed(err)
		default:
		}
		return r.settleExit(ctx, child, resultFile, time.Since(started))

	case err := <-renewEv:
		r.logf("%v; stopping the command", err)
		r.stopChild(child, sigs)
		stopRenewals()
		return r.renewFailed(err)

	case sig := <-sigs:
		r.logf("received %v; stopping the command, then releasing", sig)
		r.stopChild(child, sigs)
		stopRenewals()
		select {
		case err := <-renewEv:
			return r.renewFailed(err)
		default:
		}
		reason := r.opt.ReleaseReason
		if reason == "" {
			reason = fmt.Sprintf("runner interrupted by %v", sig)
		}
		t, err := r.settle(ctx, "release", func(c context.Context, key string) (*abby.Task, error) {
			return r.ops.Release(c, r.taskID, r.proof, reason, key)
		})
		if err != nil {
			return r.settleFailed(err)
		}
		r.rep.Task = t
		r.rep.Outcome = "released"
		return &r.rep, &exitErr{exitInterrupted, fmt.Errorf("interrupted by %v; claim released", sig)}
	}
}

func (r *runner) acquire(ctx context.Context) (cr *abby.ClaimResponse, sent time.Time, waited bool, err error) {
	key := abby.NewIdempotencyKey()
	sent = time.Now()
	if r.opt.TaskID != "" {
		cr, err = r.acq.Claim(ctx, r.opt.TaskID, abby.ClaimRequest{LeaseSeconds: r.opt.LeaseSeconds, ExpectBriefRevision: r.opt.ExpectBriefRevision}, key)
		return cr, sent, false, err
	}
	req := r.opt.Next
	req.LeaseSeconds = r.opt.LeaseSeconds
	cr, err = r.acq.ClaimNextWait(ctx, req, r.opt.Wait, 0, key)
	return cr, sent, r.opt.Wait > 0, err
}

// renewLoop renews at about a third of the lease until ctx ends or a
// renewal cannot be confirmed.
func (r *runner) renewLoop(ctx context.Context) error {
	for {
		last, deadline, lease := r.snapshot()
		interval := time.Duration(float64(lease/3) * (1 + 0.1*r.opt.jitter()))
		stopAt := deadline.Add(-r.margin())
		next := last.Add(interval)
		if next.After(stopAt) {
			next = stopAt
		}
		if !sleep(ctx, time.Until(next)) {
			return nil
		}
		if err := r.renewUntil(ctx, stopAt); err != nil {
			return err
		}
	}
}

// renewUntil makes one renewal (one key, retried) and gives up at stopAt.
func (r *runner) renewUntil(ctx context.Context, stopAt time.Time) error {
	key := abby.NewIdempotencyKey()
	first := time.Time{}
	backoff := r.backoffBase()
	for {
		if !time.Now().Before(stopAt) {
			return errLeaseUnconfirmed
		}
		if first.IsZero() {
			first = time.Now()
		}
		actx, cancel := context.WithDeadline(ctx, stopAt)
		cl, err := r.ops.Renew(actx, r.taskID, r.claimID, r.proof, r.opt.LeaseSeconds, key)
		cancel()
		if err == nil {
			// A retry of the same key may be a replay of an earlier
			// attempt, so the first send time bounds the new expiry.
			r.setDeadline(first, cl.LeaseSeconds)
			r.mu.Lock()
			r.renewals++
			r.mu.Unlock()
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var p *abby.Problem
		switch {
		case abby.IsOwnershipLost(err), errors.As(err, &p) && p.Status == 401:
			return &lostErr{err}
		case abby.IsTransient(err), errors.Is(err, context.DeadlineExceeded):
			r.logf("renew not confirmed (%v); retrying", err)
		default:
			return fmt.Errorf("%w: %v", errLeaseUnconfirmed, err)
		}
		if !sleep(ctx, min(backoff, time.Until(stopAt))) {
			return ctx.Err()
		}
		backoff = min(backoff*2, 5*r.opt.unit)
	}
}

func (r *runner) renewFailed(err error) (*runReport, error) {
	r.rep.Renewals = r.renewals
	var le *lostErr
	if errors.As(err, &le) {
		r.rep.Outcome = "claim_lost"
		r.rep.Detail = le.err.Error()
		return &r.rep, &exitErr{exitClaimLost, err}
	}
	r.rep.Outcome = "lease_unconfirmed"
	r.rep.Detail = err.Error()
	return &r.rep, &exitErr{exitLeaseUnconfirmed, fmt.Errorf("%w; command stopped, no further writes; the claim lapses at expiry unless a renew is confirmed (read the task before any new attempt)", err)}
}

func (r *runner) settleFailed(err error) (*runReport, error) {
	r.rep.Renewals = r.renewals
	var p *abby.Problem
	if abby.IsOwnershipLost(err) || (errors.As(err, &p) && p.Status == 401) {
		r.rep.Outcome = "claim_lost"
		r.rep.Detail = err.Error()
		return &r.rep, &exitErr{exitClaimLost, &lostErr{err}}
	}
	if errors.Is(err, errLeaseUnconfirmed) {
		r.rep.Outcome = "lease_unconfirmed"
		r.rep.Detail = err.Error()
		return &r.rep, &exitErr{exitLeaseUnconfirmed, fmt.Errorf("settlement outcome unknown: %w; run `abby task show %s`", err, r.taskID)}
	}
	return &r.rep, err
}

// settle runs one settlement with one key, retrying transient failures
// until the local lease deadline (RUN-006).
func (r *runner) settle(ctx context.Context, what string, op func(context.Context, string) (*abby.Task, error)) (*abby.Task, error) {
	key := abby.NewIdempotencyKey()
	backoff := r.backoffBase()
	for {
		_, deadline, _ := r.snapshot()
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("%s: %w", what, errLeaseUnconfirmed)
		}
		actx, cancel := context.WithDeadline(ctx, deadline)
		t, err := op(actx, key)
		cancel()
		if err == nil {
			return t, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !abby.IsTransient(err) && !errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		r.logf("%s not confirmed (%v); retrying with the same key", what, err)
		if !sleep(ctx, min(backoff, time.Until(deadline))) {
			return nil, ctx.Err()
		}
		backoff = min(backoff*2, 5*r.opt.unit)
	}
}

// ---- child process ----

type childProc struct {
	cmd    *exec.Cmd
	done   chan struct{}
	err    error
	status int // exit status, or -1 if killed by a signal
	signal string
}

func (r *runner) childEnv(cr *abby.ClaimResponse, resultFile, briefFile string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "ABBY_CLAIM_PROOF", "ABBY_TASK_ID", "ABBY_CLAIM_ID", "ABBY_EXECUTION_ID", "ABBY_CLAIM_EPOCH", "ABBY_RUN_ID",
			"ABBY_BRIEF_REVISION", "ABBY_BRIEF_FILE", "ABBY_RESULT_FILE":
			continue // never pass a proof; never inherit stale claim values
		}
		env = append(env, kv)
	}
	env = append(env,
		"ABBY_TASK_ID="+cr.Claim.TaskID,
		"ABBY_CLAIM_ID="+cr.Claim.ID,
		"ABBY_EXECUTION_ID="+cr.Claim.ExecutionID,
		"ABBY_CLAIM_EPOCH="+strconv.FormatInt(cr.Claim.Epoch, 10),
		"ABBY_RUN_ID="+r.runID,
		"ABBY_BOARD="+r.opt.Board,
		"ABBY_BRIEF_REVISION="+strconv.Itoa(cr.Claim.BriefRevision),
		"ABBY_RESULT_FILE="+resultFile,
	)
	if briefFile != "" {
		env = append(env, "ABBY_BRIEF_FILE="+briefFile)
	}
	return env
}

func (r *runner) startChild(cr *abby.ClaimResponse, resultFile, briefFile string) (*childProc, error) {
	if len(r.opt.Command) == 0 {
		return nil, errors.New("no command")
	}
	cmd := exec.Command(r.opt.Command[0], r.opt.Command[1:]...)
	cmd.Env = r.childEnv(cr, resultFile, briefFile)
	cmd.Dir = r.opt.Dir
	cmd.Stdout = r.opt.Stdout
	cmd.Stderr = r.opt.Stderr
	// Own process group, so a stop reaches the whole tree the command
	// starts, and terminal Ctrl-C goes to the runner only.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = r.opt.Grace
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	ch := &childProc{cmd: cmd, done: make(chan struct{})}
	go func() {
		ch.err = cmd.Wait()
		ch.status = 0
		if ps := cmd.ProcessState; ps != nil {
			ch.status = ps.ExitCode()
			if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				ch.signal = ws.Signal().String()
			}
		} else if ch.err != nil {
			ch.status = -1
		}
		close(ch.done)
	}()
	return ch, nil
}

// stopChild sends SIGTERM to the process group, then SIGKILL after the
// grace period, or at once on another runner signal.
func (r *runner) stopChild(ch *childProc, sigs <-chan os.Signal) {
	pgid := ch.cmd.Process.Pid
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	t := time.NewTimer(r.opt.Grace)
	defer t.Stop()
	select {
	case <-ch.done:
		_ = syscall.Kill(-pgid, syscall.SIGKILL) // stragglers in the group
		return
	case <-t.C:
		r.logf("command did not stop within %s; killing", r.opt.Grace)
	case <-sigs:
		r.logf("second signal; killing the command")
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	<-ch.done
}

// childResult is what the command may write to $ABBY_RESULT_FILE.
type childResult struct {
	Summary  string          `json:"summary"`
	Reason   string          `json:"reason"`
	Evidence []abby.Evidence `json:"evidence"`
	Data     json.RawMessage `json:"data"`
}

func readChildResult(path string) (*childResult, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return nil, err
	}
	var cr childResult
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cr); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &cr, nil
}

// buildData merges the command's data object with the runner's fields.
func buildData(fromChild json.RawMessage, status int, signal string, elapsed time.Duration) (json.RawMessage, error) {
	m := map[string]any{}
	if len(fromChild) > 0 && string(fromChild) != "null" {
		if err := json.Unmarshal(fromChild, &m); err != nil {
			return nil, fmt.Errorf("result data must be a JSON object: %w", err)
		}
	}
	m["exit_status"] = status
	if signal != "" {
		m["signal"] = signal
	}
	m["duration_ms"] = elapsed.Milliseconds()
	return json.Marshal(m)
}

func (r *runner) settleExit(ctx context.Context, ch *childProc, resultFile string, elapsed time.Duration) (*runReport, error) {
	status := ch.status
	r.rep.ExitStatus = &status
	cres, rerr := readChildResult(resultFile)
	if rerr != nil {
		r.logf("ignoring the command's result file: %v", rerr)
		cres = nil
	}
	baseData, _ := buildData(nil, status, ch.signal, elapsed)
	evidence := append([]abby.Evidence(nil), r.opt.Evidence...)
	summary := r.opt.Summary
	data := baseData
	if cres != nil {
		evidence = append(evidence, r.normalizeEvidence(cres.Evidence)...)
		if cres.Summary != "" {
			summary = cres.Summary
		}
		if d, err := buildData(cres.Data, status, ch.signal, elapsed); err == nil {
			data = d
		} else {
			r.logf("ignoring the command's result data: %v", err)
		}
	}

	if status == 0 && ch.signal == "" {
		res := abby.Result{Summary: summary, Evidence: evidence, Data: data}
		t, err := r.settle(ctx, "complete", func(c context.Context, key string) (*abby.Task, error) {
			return r.ops.Complete(c, r.taskID, r.proof, res, key)
		})
		if isRejectedResult(err) {
			return r.resultRejected(ctx, err, &rejectedResult{Settlement: "complete", Summary: summary, Evidence: evidence, Data: data})
		}
		if err != nil {
			return r.settleFailed(err)
		}
		r.rep.Renewals = r.renewals
		r.rep.Outcome = "completed"
		r.rep.Task = t
		r.logf("task %s completed", r.taskID)
		return &r.rep, nil
	}

	reason := fmt.Sprintf("command exited with status %d", status)
	if ch.signal != "" {
		reason = "command killed by signal " + ch.signal
	}
	if cres != nil && cres.Reason != "" {
		reason = cres.Reason + " (" + reason + ")"
	}
	fr := abby.FailRequest{Reason: reason, Evidence: evidence, Data: data}
	t, err := r.settle(ctx, "fail", func(c context.Context, key string) (*abby.Task, error) {
		return r.ops.Fail(c, r.taskID, r.proof, fr, key)
	})
	if isRejectedResult(err) {
		return r.resultRejected(ctx, err, &rejectedResult{Settlement: "fail", Reason: reason, Evidence: evidence, Data: data})
	}
	if err != nil {
		return r.settleFailed(err)
	}
	r.rep.Renewals = r.renewals
	r.rep.Outcome = "failed"
	r.rep.Task = t
	r.logf("task %s failed: %s", r.taskID, reason)
	return &r.rep, &exitErr{exitCommandFailed, errors.New("task failed: " + reason)}
}

func isRejectedResult(err error) bool {
	var p *abby.Problem
	return err != nil && errors.As(err, &p) && (p.Status == 400 || p.Status == 413)
}

// normalizeEvidence maps evidence kinds the protocol does not accept
// (CLAIM-031) to "other" and keeps the original kind at the start of the
// note, so nothing the command reported is lost or rejected for its kind.
func (r *runner) normalizeEvidence(in []abby.Evidence) []abby.Evidence {
	out := make([]abby.Evidence, 0, len(in))
	for _, ev := range in {
		if !validEvidenceKind(ev.Kind) {
			orig := ev.Kind
			ev.Kind = "other"
			switch {
			case orig == "":
				// No kind to keep.
			case ev.Note == "":
				ev.Note = orig
			default:
				ev.Note = orig + ": " + ev.Note
			}
			r.rep.EvidenceMapped = append(r.rep.EvidenceMapped, fmt.Sprintf("%q -> other (ref %s)", orig, ev.Ref))
			r.logf("result evidence kind %q is not one of %s; recording it as other (ref %s)", orig, evidenceKindList, ev.Ref)
		}
		out = append(out, ev)
	}
	return out
}

// resultRejected handles a complete or fail that the server refused with
// 400 or 413. The runner never settles again without the result it was
// given: it keeps the claim alive with a renewal, reports the refused
// result, and releases the claim with a reason that names the rejection,
// so the task is ready for a corrected settlement (exit 13).
func (r *runner) resultRejected(ctx context.Context, err error, rr *rejectedResult) (*runReport, error) {
	var p *abby.Problem
	errors.As(err, &p)
	rr.Status, rr.Code, rr.Detail = p.Status, p.Code, p.Detail
	r.rep.RejectedResult = rr
	r.rep.Outcome = "result_rejected"
	r.rep.Detail = fmt.Sprintf("%s rejected: %d %s: %s", rr.Settlement, p.Status, p.Code, p.Detail)
	r.logf("server rejected the %s result (%d %s: %s); NOT settling without it. Evidence it carried:", rr.Settlement, p.Status, p.Code, p.Detail)
	for _, ev := range rr.Evidence {
		r.logf("  %s %s %s", ev.Kind, ev.Ref, ev.Note)
	}

	// Keep the claim alive until the release is confirmed.
	_, deadline, _ := r.snapshot()
	if rerr := r.renewUntil(ctx, deadline.Add(-r.margin())); rerr != nil {
		rep, ee := r.renewFailed(rerr)
		rep.RejectedResult = rr
		return rep, ee
	}

	detail := p.Detail
	if len(detail) > 500 {
		detail = detail[:500] + "…"
	}
	reason := fmt.Sprintf("abby run: server rejected the %s result (%d %s: %s); claim released so the work can be settled again with a corrected result, not without its evidence", rr.Settlement, p.Status, p.Code, detail)
	t, rerr := r.settle(ctx, "release", func(c context.Context, key string) (*abby.Task, error) {
		return r.ops.Release(c, r.taskID, r.proof, reason, key)
	})
	if rerr != nil {
		rep, ee := r.settleFailed(rerr)
		rep.RejectedResult = rr
		return rep, ee
	}
	r.rep.Renewals = r.renewals
	r.rep.Task = t
	r.logf("claim %s released; task %s is ready for a corrected settlement", r.claimID, r.taskID)
	return &r.rep, &exitErr{exitResultRejected, fmt.Errorf("server rejected the %s result (%d %s: %s); claim released, evidence kept in the report", rr.Settlement, p.Status, p.Code, p.Detail)}
}
