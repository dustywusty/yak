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
	"unicode/utf8"

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
	// settleDetail is the problem detail for other settle statuses.
	settleDetail string
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
		// Like the real server (CLAIM-031): unknown evidence kinds are a
		// 400 and reserve no key.
		evs, _ := body["evidence"].([]any)
		if res, ok := body["result"].(map[string]any); ok {
			evs, _ = res["evidence"].([]any)
		}
		for _, e := range evs {
			if k, _ := e.(map[string]any)["kind"].(string); !validEvidenceKind(k) {
				problem(w, 400, "validation_failed")
				return
			}
		}
		if reason, ok := body["reason"].(string); ok && !utf8.ValidString(reason) {
			problem(w, 400, "validation_failed") // "reason must be valid UTF-8"
			return
		}
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
			if f.settleDetail != "" {
				writeJSON(w, st, map[string]any{"status": st, "code": "limit_exceeded", "detail": f.settleDetail, "request_id": "req"})
				return
			}
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

// BOOT-03 regression: a result file with kinds branch/check was rejected
// and the runner completed without any evidence. Unknown kinds now go out
// as "other" with the original kind in the note, and the server accepts
// them on the first attempt.
func TestRunMapsUnknownEvidenceKinds(t *testing.T) {
	f := newFakeServer(t, 30)
	script := `cat > "$ABBY_RESULT_FILE" <<'EOF'
{"summary":"fixed","evidence":[
 {"kind":"commit","ref":"0123456789abcdef0123456789abcdef01234567"},
 {"kind":"branch","ref":"fix/runner-evidence"},
 {"kind":"check","ref":"go test ./...","note":"ok"}]}
EOF`
	r := testRunner(t, f, sh(script), nil)
	rep, err := r.run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.Outcome != "completed" {
		t.Fatalf("outcome = %s", rep.Outcome)
	}
	cs := f.requests("/complete")
	if len(cs) != 1 {
		t.Fatalf("complete attempts = %d, want 1", len(cs))
	}
	got, _ := json.Marshal(cs[0].Body["result"].(map[string]any)["evidence"])
	want := `[{"kind":"commit","ref":"0123456789abcdef0123456789abcdef01234567"},` +
		`{"kind":"other","note":"branch","ref":"fix/runner-evidence"},` +
		`{"kind":"other","note":"check: ok","ref":"go test ./..."}]`
	if string(got) != want {
		t.Errorf("evidence sent:\n got %s\nwant %s", got, want)
	}
	if len(rep.EvidenceMapped) != 2 {
		t.Errorf("evidence_mapped = %v, want 2 entries", rep.EvidenceMapped)
	}
}

// When the server still rejects the result, the runner must not settle
// without it: it keeps the claim (renews), releases with a reason that
// names the rejection, reports the refused result, and exits 13.
func TestRunResultRejectedReleasesWithoutSettling(t *testing.T) {
	for _, tc := range []struct {
		name, op, script string
		status           int
	}{
		{"complete-413", "complete", "exit 0", 413},
		{"fail-400", "fail", "exit 3", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeServer(t, 30)
			f.settle = func(op string, n int) int {
				if op == tc.op {
					return tc.status
				}
				return 200
			}
			script := `printf '{"summary":"s","evidence":[{"kind":"commit","ref":"abc123"},{"kind":"branch","ref":"fix/x"}]}' > "$ABBY_RESULT_FILE"; ` + tc.script
			r := testRunner(t, f, sh(script), func(o *runOptions) {
				o.Evidence = []abby.Evidence{{Kind: "url", Ref: "https://example.test/pr/1"}}
			})
			rep, err := r.run(context.Background())
			if code(err) != exitResultRejected {
				t.Fatalf("exit = %d (%v), want %d", code(err), err, exitResultRejected)
			}
			if rep.Outcome != "result_rejected" {
				t.Errorf("outcome = %s", rep.Outcome)
			}
			// Exactly one settlement attempt, and never one without the evidence.
			ss := f.requests("/" + tc.op)
			if len(ss) != 1 {
				t.Fatalf("%s attempts = %d, want 1 (no re-settle without the result)", tc.op, len(ss))
			}
			for _, other := range []string{"/complete", "/fail"} {
				if other != "/"+tc.op && len(f.requests(other)) != 0 {
					t.Errorf("%s called after the rejection", other)
				}
			}
			// The claim was kept alive after the rejection, then released.
			rel := f.requests("/release")
			if len(rel) != 1 || rel[0].Proof != testProof {
				t.Fatalf("release calls = %+v", rel)
			}
			reason := fmt.Sprint(rel[0].Body["reason"])
			if !strings.Contains(reason, "rejected") || !strings.Contains(reason, strconv.Itoa(tc.status)) || !strings.Contains(reason, tc.op) {
				t.Errorf("release reason = %q", reason)
			}
			renewedBetween := false
			for _, q := range f.requests("/renew") {
				if q.At.After(ss[0].At) && q.At.Before(rel[0].At) {
					renewedBetween = true
				}
			}
			if !renewedBetween {
				t.Error("no renewal between the rejection and the release")
			}
			if rep.Task == nil || rep.Task.State != "ready" {
				t.Errorf("task after release = %+v", rep.Task)
			}
			// The refused result is in the report, evidence intact.
			rr := rep.RejectedResult
			if rr == nil || rr.Settlement != tc.op || rr.Status != tc.status {
				t.Fatalf("rejected_result = %+v", rr)
			}
			ev := fmt.Sprint(rr.Evidence)
			for _, s := range []string{"abc123", "fix/x", "example.test"} {
				if !strings.Contains(ev, s) {
					t.Errorf("rejected_result evidence %s lacks %s", ev, s)
				}
			}
		})
	}
}

// Review F1 of 7d760f6: a result file that exists but cannot be used was
// ignored, and the runner completed with no evidence. Now the runner
// never settles: it renews, releases with a reason that names the
// problem, keeps the raw file in the report, and exits 13.
func TestRunUnusableResultFileReleasesWithoutSettling(t *testing.T) {
	oversize := `{"evidence":[{"kind":"commit","ref":"abc"}],"summary":"` + strings.Repeat("a", maxResultFileBytes) + `"}`
	for _, tc := range []struct {
		name, file, exit, wantDetail string
	}{
		{"unknown-evidence-field", `{"evidence":[{"kind":"commit","ref":"abc","sha":"abc"}]}`, "0", `unknown field "sha"`},
		{"unknown-top-level-key", `{"summary":"s","branch":"fix/x","evidence":[{"kind":"commit","ref":"abc"}]}`, "0", `unknown field "branch"`},
		{"trailing-comma", `{"evidence":[{"kind":"commit","ref":"abc"},]}`, "0", "invalid character"},
		{"content-after-object", `{"evidence":[{"kind":"commit","ref":"abc"}]} {"x":1}`, "0", "content after the result object"},
		{"oversize", oversize, "0", "larger than"},
		{"non-object-data", `{"evidence":[{"kind":"commit","ref":"abc"}],"data":[1,2]}`, "0", "must be a JSON object"},
		{"fail-path-trailing-comma", `{"reason":"r","evidence":[{"kind":"commit","ref":"abc"},]}`, "3", "invalid character"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeServer(t, 30)
			fixture := filepath.Join(t.TempDir(), "result.json")
			if err := os.WriteFile(fixture, []byte(tc.file), 0o600); err != nil {
				t.Fatal(err)
			}
			r := testRunner(t, f, sh(`cp `+fixture+` "$ABBY_RESULT_FILE"; exit `+tc.exit), nil)
			rep, err := r.run(context.Background())
			if code(err) != exitResultRejected {
				t.Fatalf("exit = %d (%v), want %d", code(err), err, exitResultRejected)
			}
			if rep.Outcome != "result_rejected" {
				t.Errorf("outcome = %s", rep.Outcome)
			}
			for _, op := range []string{"/complete", "/fail"} {
				if n := len(f.requests(op)); n != 0 {
					t.Errorf("%s called %d times; must not settle without the result file", op, n)
				}
			}
			rel := f.requests("/release")
			if len(rel) != 1 || rel[0].Proof != testProof {
				t.Fatalf("release calls = %+v", rel)
			}
			reason := fmt.Sprint(rel[0].Body["reason"])
			if !strings.Contains(reason, "result file") || !strings.Contains(reason, "result_file_unusable") || !utf8.ValidString(reason) {
				t.Errorf("release reason = %q", reason)
			}
			rs := f.requests("/renew")
			if len(rs) == 0 || !rs[len(rs)-1].At.Before(rel[0].At) {
				t.Error("no renewal before the release")
			}
			if rep.Task == nil || rep.Task.State != "ready" {
				t.Errorf("task after release = %+v", rep.Task)
			}
			rr := rep.RejectedResult
			if rr == nil || rr.Code != "result_file_unusable" || rr.Status != 0 || !strings.Contains(rr.Detail, tc.wantDetail) {
				t.Fatalf("rejected_result = %+v", rr)
			}
			wantSettlement := map[string]string{"0": "complete", "3": "fail"}[tc.exit]
			if rr.Settlement != wantSettlement {
				t.Errorf("settlement = %s, want %s", rr.Settlement, wantSettlement)
			}
			if !strings.Contains(rr.Raw, `"ref":"abc"`) || len(rr.Raw) > maxReportRawBytes+len("…") {
				t.Errorf("raw in report: %d bytes, has ref: %v", len(rr.Raw), strings.Contains(rr.Raw, `"ref":"abc"`))
			}
		})
	}
}

// Review R1: the release reason is cut on a rune boundary, so a
// multi-byte problem detail cannot make the release fail.
func TestRunRejectedReleaseReasonIsValidUTF8(t *testing.T) {
	f := newFakeServer(t, 30)
	f.settle = func(op string, n int) int {
		if op == "complete" {
			return 413
		}
		return 200
	}
	f.settleDetail = "x" + strings.Repeat("é", 600) // 1201 bytes, odd offsets split runes
	r := testRunner(t, f, sh("exit 0"), nil)
	rep, err := r.run(context.Background())
	if code(err) != exitResultRejected {
		t.Fatalf("exit = %d (%v), want %d", code(err), err, exitResultRejected)
	}
	if rep.ReleaseError != "" || rep.Task == nil || rep.Task.State != "ready" {
		t.Fatalf("release did not succeed: release_error=%q task=%+v", rep.ReleaseError, rep.Task)
	}
	rel := f.requests("/release")
	reason := fmt.Sprint(rel[0].Body["reason"])
	if !utf8.ValidString(reason) || !strings.Contains(reason, "…") || len(reason) > 800 {
		t.Errorf("reason (%d bytes) = %q", len(reason), reason)
	}
}

func TestTruncateUTF8(t *testing.T) {
	s := "x" + strings.Repeat("é", 10) // é is 2 bytes: offsets 2, 4, … split a rune
	for n := 0; n <= len(s)+1; n++ {
		got := truncateUTF8(s, n)
		if !utf8.ValidString(got) {
			t.Fatalf("n=%d: invalid UTF-8 %q", n, got)
		}
		if n >= len(s) && got != s {
			t.Fatalf("n=%d: changed a short string: %q", n, got)
		}
		if n < len(s) && len(strings.TrimSuffix(got, "…")) > n {
			t.Fatalf("n=%d: kept %d bytes", n, len(strings.TrimSuffix(got, "…")))
		}
	}
}

// Review R2: if the release after a rejection fails, the runner still
// exits 13 (nothing was settled) and reports the release error.
func TestRunRejectedReleaseFailureStillExits13(t *testing.T) {
	f := newFakeServer(t, 30)
	f.settle = func(op string, n int) int {
		switch op {
		case "complete":
			return 413
		case "release":
			return 400
		}
		return 200
	}
	script := `printf '{"evidence":[{"kind":"commit","ref":"abc123"}]}' > "$ABBY_RESULT_FILE"`
	r := testRunner(t, f, sh(script), nil)
	rep, err := r.run(context.Background())
	if code(err) != exitResultRejected {
		t.Fatalf("exit = %d (%v), want %d", code(err), err, exitResultRejected)
	}
	if rep.Outcome != "result_rejected" || rep.ReleaseError == "" {
		t.Errorf("outcome = %s, release_error = %q", rep.Outcome, rep.ReleaseError)
	}
	if !strings.Contains(err.Error(), "release failed") {
		t.Errorf("error = %v", err)
	}
	if rep.RejectedResult == nil || !strings.Contains(fmt.Sprint(rep.RejectedResult.Evidence), "abc123") {
		t.Errorf("rejected_result = %+v", rep.RejectedResult)
	}
	if n := len(f.requests("/complete")); n != 1 {
		t.Errorf("complete attempts = %d, want 1", n)
	}
	if n := len(f.requests("/release")); n != 1 {
		t.Errorf("release attempts = %d, want 1 (a 400 is final)", n)
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
	for _, bad := range []string{"bogus:x", "branch:fix/x", "check:go test", "commit::note", "commit: "} {
		if _, err := parseEvidence([]string{bad}); err == nil || !isUsageErr(err) {
			t.Errorf("%q: err = %v, want a usage error", bad, err)
		}
	}
	if _, err := commitEvidence([]string{"abc", ""}); err == nil || !isUsageErr(err) {
		t.Errorf("empty --commit: err = %v, want a usage error", err)
	}
}

func TestLabelFilter(t *testing.T) {
	m := labelFilter([]string{"area:server", "area:cli", "urgent"})
	b, _ := json.Marshal(m)
	if string(b) != `{"area":["server","cli"],"urgent":[]}` {
		t.Fatalf("got %s", b)
	}
}
