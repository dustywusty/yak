package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/abby-protocol/abby/sdk/go/abby"
)

// Time scale for runner tests: one lease "second" is 10 ms, so a 30 s
// lease lasts 300 ms and renewals come every ~100 ms.
const testUnit = 10 * time.Millisecond

const testProof = "acp1.claim_1.secretsecretsecretsecret"

type rec struct {
	Method, Path, Key, Proof, RunID string
	Body                            map[string]any
	At                              time.Time
}

// fakeServer is a minimal task server for one task and one claim.
type fakeServer struct {
	t     *testing.T
	srv   *httptest.Server
	lease int

	mu        sync.Mutex
	reqs      []rec
	renews    int
	committed map[string]string // idempotency key -> op already committed
	// renew decides the response to the n-th renewal (1-based): 200, 409
	// (claim_lost), 503, or 0 (hang until the client gives up).
	renew func(n int) int
	// settle decides the response to the n-th attempt of op: 200, 503 after
	// committing (lost response), 409, or 0 (hang).
	settle   func(op string, n int) int
	attempts map[string]int
	// claimNext answers the n-th claim-next: "claimed" or "no_work".
	claimNext func(n int) string
	nexts     int
	claimCode string // if set, the claim route fails with this 409 code
}

func newFakeServer(t *testing.T, lease int) *fakeServer {
	f := &fakeServer{t: t, lease: lease, committed: map[string]string{}, attempts: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) claimView(status string) map[string]any {
	return map[string]any{
		"id": "claim_1", "task_id": "task_1", "actor_id": "actor_w", "execution_id": "exec_1", "run_id": "run_x",
		"epoch": 2, "brief_revision": 1, "status": status, "revision": 1, "lease_seconds": f.lease,
		"acquired_at": time.Now().UTC().Format(time.RFC3339Nano), "expires_at": time.Now().Add(time.Duration(f.lease) * time.Second).UTC().Format(time.RFC3339Nano),
		"ended_at": nil, "end_recorded_at": nil,
	}
}

func taskView(state string) map[string]any {
	return map[string]any{"id": "task_1", "board_id": "b", "thread_id": "th_1", "channel_id": "ch", "channel_path": "server",
		"title": "T", "owner_actor_id": "actor_lead", "assignee_actor_id": nil, "created_by_actor_id": "actor_lead",
		"origin": "new_thread", "state": state, "revision": 4, "brief_revision": 1, "epoch": 2, "retry_count": 0,
		"dependencies": []any{}, "unreadable_dependencies_pending": false, "current_claim": nil, "last_claim": nil,
		"block": nil, "outcome": nil, "root_redacted": false,
		"created_at": "2026-11-19T20:00:00Z", "updated_at": "2026-11-19T20:00:00Z", "state_changed_at": "2026-11-19T20:00:00Z"}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func problem(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{"status": status, "code": code, "detail": code, "request_id": "req"})
}

func (f *fakeServer) handle(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	rc := rec{Method: r.Method, Path: r.URL.Path, Key: r.Header.Get("Idempotency-Key"), Proof: r.Header.Get(abby.HeaderClaimProof),
		RunID: r.Header.Get("Abby-Run-Id"), Body: body, At: time.Now()}
	f.mu.Lock()
	f.reqs = append(f.reqs, rc)
	f.mu.Unlock()

	hang := func() { <-r.Context().Done() }
	p := strings.TrimPrefix(r.URL.Path, "/v1/boards/b/tasks")
	switch {
	case p == "/claim-next":
		f.mu.Lock()
		f.nexts++
		n := f.nexts
		f.mu.Unlock()
		out := "claimed"
		if f.claimNext != nil {
			out = f.claimNext(n)
		}
		if out == "no_work" {
			writeJSON(w, 200, map[string]any{"outcome": "no_work", "timed_out": true})
			return
		}
		f.writeClaim(w)
	case p == "/task_1/claims":
		if f.claimCode != "" {
			problem(w, 409, f.claimCode)
			return
		}
		f.writeClaim(w)
	case strings.HasSuffix(p, "/renew"):
		if rc.Proof != testProof {
			problem(w, 428, "precondition_required")
			return
		}
		f.mu.Lock()
		f.renews++
		n := f.renews
		f.mu.Unlock()
		st := 200
		if f.renew != nil {
			st = f.renew(n)
		}
		switch st {
		case 0:
			hang()
		case 200:
			writeJSON(w, 200, f.claimView("active"))
		case 409:
			problem(w, 409, "claim_lost")
		default:
			problem(w, st, "temporarily_unavailable")
		}
	case p == "/task_1/complete", p == "/task_1/fail", p == "/task_1/release":
		op := strings.TrimPrefix(p, "/task_1/")
		if rc.Proof != testProof {
			problem(w, 428, "precondition_required")
			return
		}
		f.mu.Lock()
		f.attempts[op]++
		n := f.attempts[op]
		prev, replay := f.committed[rc.Key]
		f.mu.Unlock()
		state := map[string]string{"complete": "completed", "fail": "failed", "release": "ready"}[op]
		if replay && prev == op {
			w.Header().Set("Idempotent-Replayed", "true")
			writeJSON(w, 200, taskView(state))
			return
		}
		st := 200
		if f.settle != nil {
			st = f.settle(op, n)
		}
		switch st {
		case 0:
			hang()
		case 200:
			f.mu.Lock()
			f.committed[rc.Key] = op
			f.mu.Unlock()
			writeJSON(w, 200, taskView(state))
		case 409:
			problem(w, 409, "claim_lost")
		case 503:
			// Commit, then lose the response.
			f.mu.Lock()
			f.committed[rc.Key] = op
			f.mu.Unlock()
			problem(w, 503, "temporarily_unavailable")
		default:
			problem(w, st, "error")
		}
	default:
		problem(w, 404, "not_found")
	}
}

func (f *fakeServer) writeClaim(w http.ResponseWriter) {
	writeJSON(w, 201, map[string]any{
		"outcome": "claimed", "claim": f.claimView("active"), "proof": testProof, "proof_unavailable": false,
		"brief": map[string]any{"revision": 1, "text": "Do the thing.", "redacted": false}, "task": taskView("claimed"),
	})
}

func (f *fakeServer) requests(suffix string) []rec {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []rec
	for _, r := range f.reqs {
		if strings.HasSuffix(r.Path, suffix) {
			out = append(out, r)
		}
	}
	return out
}

type tlog struct{ t *testing.T }

func (l tlog) Write(p []byte) (int, error) {
	l.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func testRunner(t *testing.T, f *fakeServer, command []string, mod func(*runOptions)) *runner {
	t.Helper()
	opt := runOptions{
		Board: "b", TaskID: "task_1", Command: command, Grace: 300 * time.Millisecond,
		Log: tlog{t}, unit: testUnit, jitter: func() float64 { return 0 },
	}
	if mod != nil {
		mod(&opt)
	}
	return newRunner(abby.New(f.srv.URL, "tok"), opt)
}

func sh(script string) []string { return []string{"/bin/sh", "-c", script} }

func code(err error) int {
	if err == nil {
		return exitOK
	}
	return exitCode(err)
}

// pidAlive reports whether a process exists.
func pidAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func readPid(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) > 0 {
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err == nil {
				return pid
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("child never wrote %s", path)
	return 0
}

func TestRunSuccessCompletes(t *testing.T) {
	t.Setenv("ABBY_CLAIM_PROOF", "must-not-leak")
	f := newFakeServer(t, 30)
	script := `printf '{"summary":"all good","evidence":[{"kind":"commit","ref":"abc123"}],"data":{"task":"%s","claim":"%s","exec":"%s","epoch":"%s","run":"%s","proof":"%s","brief":"%s"}}' ` +
		`"$ABBY_TASK_ID" "$ABBY_CLAIM_ID" "$ABBY_EXECUTION_ID" "$ABBY_CLAIM_EPOCH" "$ABBY_RUN_ID" "${ABBY_CLAIM_PROOF:-}" "$(cat "$ABBY_BRIEF_FILE")" > "$ABBY_RESULT_FILE"`
	r := testRunner(t, f, sh(script), func(o *runOptions) {
		o.Evidence = []abby.Evidence{{Kind: "url", Ref: "https://example.test/pr/1"}}
	})
	rep, err := r.run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.Outcome != "completed" || rep.ExitStatus == nil || *rep.ExitStatus != 0 {
		t.Fatalf("report %+v", rep)
	}
	cs := f.requests("/complete")
	if len(cs) != 1 {
		t.Fatalf("complete calls = %d", len(cs))
	}
	c := cs[0]
	if c.Proof != testProof {
		t.Errorf("complete proof header = %q", c.Proof)
	}
	if c.Key == "" {
		t.Error("complete without Idempotency-Key")
	}
	if strings.Contains(fmt.Sprint(c.Body), testProof) {
		t.Error("proof leaked into a body")
	}
	res := c.Body["result"].(map[string]any)
	if res["summary"] != "all good" {
		t.Errorf("summary = %v", res["summary"])
	}
	ev := fmt.Sprint(res["evidence"])
	if !strings.Contains(ev, "abc123") || !strings.Contains(ev, "example.test") {
		t.Errorf("evidence = %v", ev)
	}
	data := res["data"].(map[string]any)
	want := map[string]any{"task": "task_1", "claim": "claim_1", "exec": "exec_1", "epoch": "2", "run": r.runID, "proof": "", "brief": "Do the thing.", "exit_status": float64(0)}
	for k, v := range want {
		if data[k] != v {
			t.Errorf("data[%s] = %v, want %v", k, data[k], v)
		}
	}
	for _, q := range f.requests("/claims") {
		if q.Proof != "" {
			t.Error("claim request carried a proof header")
		}
		if q.RunID != r.runID {
			t.Errorf("claim Abby-Run-Id = %q, want %q", q.RunID, r.runID)
		}
	}
}

func TestRunNonZeroExitFails(t *testing.T) {
	f := newFakeServer(t, 30)
	r := testRunner(t, f, sh("exit 3"), nil)
	rep, err := r.run(context.Background())
	if code(err) != exitCommandFailed {
		t.Fatalf("exit = %d (%v), want %d", code(err), err, exitCommandFailed)
	}
	if rep.Outcome != "failed" {
		t.Fatalf("outcome = %s", rep.Outcome)
	}
	fs := f.requests("/fail")
	if len(fs) != 1 || fs[0].Proof != testProof {
		t.Fatalf("fail calls = %+v", fs)
	}
	if !strings.Contains(fs[0].Body["reason"].(string), "status 3") {
		t.Errorf("reason = %v", fs[0].Body["reason"])
	}
	if d := fs[0].Body["data"].(map[string]any); d["exit_status"] != float64(3) {
		t.Errorf("data = %v", d)
	}
	if len(f.requests("/complete")) != 0 {
		t.Error("complete called on failure")
	}
}

func TestRunRenewalCadence(t *testing.T) {
	f := newFakeServer(t, 30) // 300 ms lease -> renew every ~100 ms
	r := testRunner(t, f, sh("sleep 1"), nil)
	start := time.Now()
	if _, err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	rs := f.requests("/renew")
	if len(rs) < 6 || len(rs) > 12 {
		t.Fatalf("renewals = %d in %s, want about 9", len(rs), time.Since(start))
	}
	keys := map[string]bool{}
	prev := f.requests("/claims")[0].At
	for i, q := range rs {
		if q.Proof != testProof {
			t.Errorf("renew %d proof header = %q", i, q.Proof)
		}
		if keys[q.Key] {
			t.Errorf("renew %d reused key %s", i, q.Key)
		}
		keys[q.Key] = true
		if gap := q.At.Sub(prev); gap < 80*time.Millisecond || gap > 200*time.Millisecond {
			t.Errorf("renew %d gap %s, want ~100ms", i, gap)
		}
		prev = q.At
	}
}

func TestRunJitterBounds(t *testing.T) {
	for _, j := range []float64{-1, 1} {
		f := newFakeServer(t, 30)
		r := testRunner(t, f, sh("sleep 0.5"), func(o *runOptions) { o.jitter = func() float64 { return j } })
		if _, err := r.run(context.Background()); err != nil {
			t.Fatal(err)
		}
		rs := f.requests("/renew")
		if len(rs) < 2 {
			t.Fatalf("jitter %v: renewals = %d", j, len(rs))
		}
		gap := rs[1].At.Sub(rs[0].At)
		want := time.Duration(float64(100*time.Millisecond) * (1 + 0.1*j))
		if gap < want-15*time.Millisecond || gap > want+60*time.Millisecond {
			t.Errorf("jitter %v: gap %s, want ~%s", j, gap, want)
		}
	}
}

func TestRunClaimLostStopsChild(t *testing.T) {
	f := newFakeServer(t, 30)
	f.renew = func(n int) int {
		if n >= 2 {
			return 409
		}
		return 200
	}
	pidFile := filepath.Join(t.TempDir(), "pid")
	r := testRunner(t, f, sh(`echo $$ > `+pidFile+`; exec sleep 30`), nil)
	start := time.Now()
	rep, err := r.run(context.Background())
	if code(err) != exitClaimLost {
		t.Fatalf("exit = %d (%v), want %d", code(err), err, exitClaimLost)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("runner took %s; child not stopped promptly", el)
	}
	if rep.Outcome != "claim_lost" {
		t.Errorf("outcome = %s", rep.Outcome)
	}
	if pid := readPid(t, pidFile); pidAlive(pid) {
		t.Errorf("child %d still alive", pid)
	}
	for _, op := range []string{"/complete", "/fail", "/release"} {
		if n := len(f.requests(op)); n != 0 {
			t.Errorf("%s called %d times after claim_lost", op, n)
		}
	}
}

func TestRunRenewalOutageStopsBeforeLeaseExpiry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{{"503", 503}, {"hang", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeServer(t, 50) // 500 ms lease, margin max(50 ms, 83 ms)
			f.renew = func(n int) int {
				if n >= 2 {
					return tc.status
				}
				return 200
			}
			pidFile := filepath.Join(t.TempDir(), "pid")
			r := testRunner(t, f, sh(`echo $$ > `+pidFile+`; exec sleep 30`), nil)
			rep, err := r.run(context.Background())
			returned := time.Now()
			if code(err) != exitLeaseUnconfirmed {
				t.Fatalf("exit = %d (%v), want %d", code(err), err, exitLeaseUnconfirmed)
			}
			if rep.Outcome != "lease_unconfirmed" {
				t.Errorf("outcome = %s", rep.Outcome)
			}
			// The last confirmed renewal was received after it was sent, so
			// its receipt time + lease is later than the true local deadline.
			ok := f.requests("/renew")[0]
			expiry := ok.At.Add(50 * testUnit)
			if !returned.Before(expiry) {
				t.Errorf("child stopped %s after the lease could expire", returned.Sub(expiry))
			}
			if pid := readPid(t, pidFile); pidAlive(pid) {
				t.Errorf("child %d still alive", pid)
			}
			if len(f.requests("/renew")) < 3 && tc.status == 503 {
				t.Errorf("renewal was not retried: %d attempts", len(f.requests("/renew")))
			}
			// Retries of one renewal reuse its key.
			rs := f.requests("/renew")[1:]
			for _, q := range rs {
				if q.Key != rs[0].Key {
					t.Errorf("retry of one renewal used a new key")
					break
				}
			}
			for _, op := range []string{"/complete", "/fail", "/release"} {
				if n := len(f.requests(op)); n != 0 {
					t.Errorf("%s called %d times during an outage", op, n)
				}
			}
		})
	}
}

func TestRunSignalStopsChildAndReleases(t *testing.T) {
	f := newFakeServer(t, 30)
	sigs := make(chan os.Signal, 2)
	pidFile := filepath.Join(t.TempDir(), "pid")
	r := testRunner(t, f, sh(`echo $$ > `+pidFile+`; exec sleep 30`), func(o *runOptions) { o.Signals = sigs })
	type res struct {
		rep *runReport
		err error
	}
	done := make(chan res, 1)
	go func() {
		rep, err := r.run(context.Background())
		done <- res{rep, err}
	}()
	pid := readPid(t, pidFile)
	sigs <- syscall.SIGTERM
	var out res
	select {
	case out = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runner did not stop after SIGTERM")
	}
	if code(out.err) != exitInterrupted {
		t.Fatalf("exit = %d (%v), want %d", code(out.err), out.err, exitInterrupted)
	}
	if out.rep.Outcome != "released" {
		t.Errorf("outcome = %s", out.rep.Outcome)
	}
	if pidAlive(pid) {
		t.Errorf("child %d still alive", pid)
	}
	rel := f.requests("/release")
	if len(rel) != 1 || rel[0].Proof != testProof || !strings.Contains(fmt.Sprint(rel[0].Body["reason"]), "terminated") {
		t.Fatalf("release calls = %+v", rel)
	}
}

func TestRunSecondSignalKillsAtOnce(t *testing.T) {
	f := newFakeServer(t, 30)
	sigs := make(chan os.Signal, 2)
	pidFile := filepath.Join(t.TempDir(), "pid")
	// The child ignores SIGTERM; only SIGKILL stops it.
	r := testRunner(t, f, sh(`trap '' TERM; echo $$ > `+pidFile+`; while :; do sleep 0.01; done`), func(o *runOptions) {
		o.Signals = sigs
		o.Grace = 20 * time.Second
	})
	done := make(chan error, 1)
	go func() { _, err := r.run(context.Background()); done <- err }()
	readPid(t, pidFile)
	sigs <- syscall.SIGINT
	time.Sleep(50 * time.Millisecond)
	sigs <- syscall.SIGINT
	select {
	case err := <-done:
		if code(err) != exitInterrupted {
			t.Fatalf("exit = %d (%v)", code(err), err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second signal did not kill the child")
	}
}

func TestRunSettlementRetryReusesKey(t *testing.T) {
	f := newFakeServer(t, 30)
	f.settle = func(op string, n int) int {
		if n == 1 {
			return 503 // commits, response lost
		}
		return 200
	}
	r := testRunner(t, f, sh("exit 0"), nil)
	rep, err := r.run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.Outcome != "completed" {
		t.Fatalf("outcome = %s", rep.Outcome)
	}
	cs := f.requests("/complete")
	if len(cs) != 2 {
		t.Fatalf("complete attempts = %d, want 2", len(cs))
	}
	if cs[0].Key == "" || cs[0].Key != cs[1].Key {
		t.Fatalf("retry changed the key: %q then %q", cs[0].Key, cs[1].Key)
	}
	if fmt.Sprint(cs[0].Body) != fmt.Sprint(cs[1].Body) {
		t.Errorf("retry changed the body")
	}
}

func TestRunClaimLostOnSettlement(t *testing.T) {
	f := newFakeServer(t, 30)
	f.settle = func(string, int) int { return 409 }
	r := testRunner(t, f, sh("exit 0"), nil)
	_, err := r.run(context.Background())
	if code(err) != exitClaimLost {
		t.Fatalf("exit = %d (%v), want %d", code(err), err, exitClaimLost)
	}
	if n := len(f.requests("/complete")); n != 1 {
		t.Errorf("complete attempts = %d, want 1 (claim_lost is final)", n)
	}
}

func TestRunTaskClaimedByAnother(t *testing.T) {
	f := newFakeServer(t, 30)
	f.claimCode = "task_claimed"
	r := testRunner(t, f, sh("exit 0"), nil)
	_, err := r.run(context.Background())
	if code(err) != exitClaimLost {
		t.Fatalf("exit = %d (%v), want %d", code(err), err, exitClaimLost)
	}
}

func TestRunClaimNextNoWork(t *testing.T) {
	f := newFakeServer(t, 30)
	f.claimNext = func(int) string { return "no_work" }
	r := testRunner(t, f, sh("exit 0"), func(o *runOptions) { o.TaskID = "" })
	rep, err := r.run(context.Background())
	if code(err) != exitTimeout || rep.Outcome != "no_work" {
		t.Fatalf("exit = %d (%v), outcome %s", code(err), err, rep.Outcome)
	}
}

func TestRunClaimNextWaitConfirmsBeforeStart(t *testing.T) {
	f := newFakeServer(t, 30)
	f.claimNext = func(n int) string {
		if n < 3 {
			return "no_work"
		}
		return "claimed"
	}
	marker := filepath.Join(t.TempDir(), "started")
	r := testRunner(t, f, sh(`date > `+marker), func(o *runOptions) {
		o.TaskID = ""
		o.Wait = 2 * time.Second
		o.Next = abby.ClaimNextRequest{Subtree: "server", Assignee: "eligible"}
	})
	if _, err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	ns := f.requests("/claim-next")
	if len(ns) != 3 {
		t.Fatalf("claim-next calls = %d", len(ns))
	}
	for _, q := range ns {
		if q.Key != ns[0].Key {
			t.Error("claim-next attempts must share one key (SEL-008 recovery)")
		}
		if q.Body["subtree"] != "server" {
			t.Errorf("body = %v", q.Body)
		}
	}
	rs := f.requests("/renew")
	if len(rs) == 0 || rs[0].At.Before(ns[2].At) {
		t.Fatal("no confirming renew after the waiting claim-next")
	}
	st, err := os.Stat(marker)
	if err != nil {
		t.Fatal(err)
	}
	_ = st
	if len(f.requests("/complete")) != 1 {
		t.Error("not completed")
	}
}

func TestRunProofUnavailable(t *testing.T) {
	f := newFakeServer(t, 30)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := map[string]any{"outcome": "claimed", "claim": f.claimView("active"), "proof": nil, "proof_unavailable": true, "task": taskView("claimed")}
		w.Header().Set("Idempotent-Replayed", "true")
		writeJSON(w, 201, v)
	}))
	defer srv.Close()
	r := newRunner(abby.New(srv.URL, "tok"), runOptions{Board: "b", TaskID: "task_1", Command: sh("exit 0"), unit: testUnit, Log: tlog{t}})
	_, err := r.run(context.Background())
	if code(err) != exitClaimLost {
		t.Fatalf("exit = %d (%v)", code(err), err)
	}
}

func TestExitCodeMapping(t *testing.T) {
	for _, tc := range []struct {
		p    *abby.Problem
		want int
	}{
		{&abby.Problem{Status: 409, Code: "claim_lost"}, exitClaimLost},
		{&abby.Problem{Status: 409, Code: "task_claimed"}, exitClaimLost},
		{&abby.Problem{Status: 409, Code: "invalid_transition"}, exitConflict},
		{&abby.Problem{Status: 428, Code: "precondition_required"}, exitConflict},
	} {
		if got := exitCode(tc.p); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.p.Code, got, tc.want)
		}
	}
	if exitCode(&exitErr{exitLeaseUnconfirmed, errors.New("x")}) != 10 {
		t.Error("exitErr code not kept")
	}
}

func TestParseEvidence(t *testing.T) {
	ev, err := parseEvidence([]string{"commit:abc:the fix", "url:https://x.test/a:b", "other:note"})
	if err != nil {
		t.Fatal(err)
	}
	if ev[0] != (abby.Evidence{Kind: "commit", Ref: "abc", Note: "the fix"}) || ev[1].Ref != "https://x.test/a:b" || ev[2].Ref != "note" {
		t.Fatalf("got %+v", ev)
	}
	if _, err := parseEvidence([]string{"bogus:x"}); err == nil {
		t.Error("bad kind accepted")
	}
}

func TestLabelFilter(t *testing.T) {
	m := labelFilter([]string{"area:server", "area:cli", "urgent"})
	b, _ := json.Marshal(m)
	if string(b) != `{"area":["server","cli"],"urgent":[]}` {
		t.Fatalf("got %s", b)
	}
}
