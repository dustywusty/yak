package abby

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type seen struct {
	Method, Path, Query, Key, Proof, IfMatch string
	Body                                     map[string]any
}

type recorder struct {
	mu   sync.Mutex
	reqs []seen
}

func (r *recorder) add(req *http.Request) seen {
	var body map[string]any
	_ = json.NewDecoder(req.Body).Decode(&body)
	s := seen{Method: req.Method, Path: req.URL.Path, Query: req.URL.RawQuery, Key: req.Header.Get("Idempotency-Key"),
		Proof: req.Header.Get(HeaderClaimProof), IfMatch: req.Header.Get("If-Match"), Body: body}
	r.mu.Lock()
	r.reqs = append(r.reqs, s)
	r.mu.Unlock()
	return s
}

func (r *recorder) all() []seen {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]seen(nil), r.reqs...)
}

func jsonOK(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func task(id, state string) map[string]any {
	return map[string]any{"id": id, "state": state, "revision": 3, "epoch": 1, "dependencies": []any{}}
}

func newTasks(t *testing.T, h http.HandlerFunc) *Tasks {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(srv.URL, "tok")
	return c.Tasks("b1")
}

func TestProofHeaderOnlyOnClaimantOps(t *testing.T) {
	rec := &recorder{}
	tc := newTasks(t, func(w http.ResponseWriter, r *http.Request) {
		s := rec.add(r)
		switch {
		case strings.HasSuffix(s.Path, "/renew"):
			jsonOK(w, 200, map[string]any{"id": "c1", "lease_seconds": 60})
		case strings.HasSuffix(s.Path, "/claims"):
			jsonOK(w, 201, map[string]any{"outcome": "claimed", "claim": map[string]any{"id": "c1"}, "proof": "P"})
		default:
			jsonOK(w, 200, task("t1", "ready"))
		}
	})
	ctx := context.Background()
	const proof = "acp1.c1.secret"
	if _, err := tc.Claim(ctx, "t1", ClaimRequest{LeaseSeconds: 60}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := tc.Renew(ctx, "t1", "c1", proof, 60, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := tc.Release(ctx, "t1", proof, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := tc.Complete(ctx, "t1", proof, Result{Summary: "s", Evidence: []Evidence{{Kind: "commit", Ref: "abc"}}}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := tc.Fail(ctx, "t1", proof, FailRequest{Reason: "r"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := tc.Block(ctx, "t1", proof, "claimant block", 0, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := tc.Block(ctx, "t1", "", "manager block", 3, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := tc.Cancel(ctx, "t1", "no longer needed", 0, ""); err != nil {
		t.Fatal(err)
	}
	reqs := rec.all()
	wantProof := []bool{false, true, true, true, true, true, false, false}
	for i, s := range reqs {
		if (s.Proof == proof) != wantProof[i] {
			t.Errorf("%s %s: proof header %q, want sent=%v", s.Method, s.Path, s.Proof, wantProof[i])
		}
		if s.Key == "" {
			t.Errorf("%s %s: no Idempotency-Key", s.Method, s.Path)
		}
		if strings.Contains(fmt.Sprint(s.Body), proof) || strings.Contains(s.Query, "secret") {
			t.Errorf("%s %s: proof leaked into body or query", s.Method, s.Path)
		}
	}
	if reqs[1].Path != "/v1/boards/b1/tasks/t1/claims/c1/renew" || reqs[1].Body["lease_seconds"] != float64(60) {
		t.Errorf("renew = %+v", reqs[1])
	}
	if reqs[3].Body["result"].(map[string]any)["summary"] != "s" {
		t.Errorf("complete body = %v", reqs[3].Body)
	}
	if reqs[6].IfMatch != `"3"` || reqs[7].IfMatch != "" {
		t.Errorf("If-Match: manager block %q, cancel %q", reqs[6].IfMatch, reqs[7].IfMatch)
	}
	// Claimant operations refuse to run without a proof.
	if _, err := tc.Complete(ctx, "t1", "", Result{}, ""); err == nil {
		t.Error("complete without proof was sent")
	}
}

func TestKeyedRetryReusesKey(t *testing.T) {
	rec := &recorder{}
	n := 0
	tc := newTasks(t, func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		n++
		if n == 1 {
			jsonOK(w, 503, map[string]any{"status": 503, "code": "temporarily_unavailable"})
			return
		}
		w.Header().Set("Idempotent-Replayed", "true")
		jsonOK(w, 200, task("t1", "completed"))
	})
	tk, err := tc.Complete(context.Background(), "t1", "P", Result{Summary: "done"}, "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if tk.State != StateCompleted {
		t.Errorf("state %s", tk.State)
	}
	reqs := rec.all()
	if len(reqs) != 2 || reqs[0].Key != "key-1" || reqs[1].Key != "key-1" || reqs[1].Proof != "P" {
		t.Fatalf("requests = %+v", reqs)
	}
}

func TestClaimNextWaitLoopsWithOneKey(t *testing.T) {
	rec := &recorder{}
	n := 0
	tc := newTasks(t, func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		n++
		if n < 3 {
			jsonOK(w, 200, map[string]any{"outcome": "no_work", "timed_out": true})
			return
		}
		jsonOK(w, 201, map[string]any{"outcome": "claimed", "claim": map[string]any{"id": "c1", "task_id": "t1"}, "proof": "P", "task": task("t1", "claimed")})
	})
	cr, err := tc.ClaimNextWait(context.Background(), ClaimNextRequest{Channel: "server", Labels: map[string][]string{"area": {}}}, 90*time.Second, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if !cr.Claimed() || cr.ProofValue() != "P" {
		t.Fatalf("response %+v", cr)
	}
	reqs := rec.all()
	if len(reqs) != 3 {
		t.Fatalf("calls = %d", len(reqs))
	}
	for _, s := range reqs {
		if s.Key != reqs[0].Key {
			t.Error("claim-next attempts used different keys")
		}
		if w := s.Body["wait_ms"].(float64); w <= 0 || w > 30000 {
			t.Errorf("wait_ms = %v", w)
		}
		if l, ok := s.Body["labels"].(map[string]any)["area"].([]any); !ok || len(l) != 0 {
			t.Errorf("labels = %v (want key present as [])", s.Body["labels"])
		}
	}
}

func TestClaimNextNoWork(t *testing.T) {
	tc := newTasks(t, func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, 200, map[string]any{"outcome": "no_work", "timed_out": false})
	})
	cr, err := tc.ClaimNextWait(context.Background(), ClaimNextRequest{}, 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if cr.Claimed() || cr.Outcome != "no_work" {
		t.Fatalf("got %+v", cr)
	}
}

func TestManagerConditionsAndList(t *testing.T) {
	rec := &recorder{}
	tc := newTasks(t, func(w http.ResponseWriter, r *http.Request) {
		s := rec.add(r)
		if s.Method == "GET" && s.Path == "/v1/boards/b1/tasks" {
			if !strings.Contains(s.Query, "page_token=p2") {
				next := "p2"
				jsonOK(w, 200, map[string]any{"items": []any{task("t1", "ready")}, "next_page_token": next})
				return
			}
			jsonOK(w, 200, map[string]any{"items": []any{task("t2", "mystery")}, "next_page_token": nil})
			return
		}
		jsonOK(w, 200, task("t1", "ready"))
	})
	ctx := context.Background()
	a := "actor_a"
	if _, err := tc.SetAssignee(ctx, "t1", 7, &a, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := tc.SetAssignee(ctx, "t1", 8, nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := tc.SetDependencies(ctx, "t1", 9, nil, ""); err != nil {
		t.Fatal(err)
	}
	title := "New"
	if _, err := tc.Patch(ctx, "t1", 10, PatchTaskRequest{Title: &title}); err != nil {
		t.Fatal(err)
	}
	var got []Task
	if err := tc.ListAll(ctx, TaskFilter{States: []string{"ready", "waiting"}, Assignee: "none", Labels: []string{"area:server"}}, func(tk Task) error {
		got = append(got, tk)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reqs := rec.all()
	if reqs[0].Method != "PUT" || reqs[0].IfMatch != `"7"` || reqs[0].Body["assignee_actor_id"] != "actor_a" {
		t.Errorf("assign = %+v", reqs[0])
	}
	if v, ok := reqs[1].Body["assignee_actor_id"]; !ok || v != nil {
		t.Errorf("clear assignee body = %v", reqs[1].Body)
	}
	if d, ok := reqs[2].Body["task_ids"].([]any); !ok || len(d) != 0 {
		t.Errorf("deps body = %v", reqs[2].Body)
	}
	if reqs[3].Method != "PATCH" || reqs[3].IfMatch != `"10"` || reqs[3].Body["title"] != "New" || reqs[3].Body["owner_actor_id"] != nil {
		t.Errorf("patch = %+v", reqs[3])
	}
	if q := reqs[4].Query; !strings.Contains(q, "state=ready") || !strings.Contains(q, "state=waiting") || !strings.Contains(q, "assignee=none") || !strings.Contains(q, "label=area%3Aserver") {
		t.Errorf("list query = %s", q)
	}
	if len(got) != 2 {
		t.Fatalf("listed %d", len(got))
	}
	if got[0].CheckState() != nil || got[1].CheckState() == nil {
		t.Error("unknown state must be an error (TASK-020)")
	}
}

func TestErrorClassifiers(t *testing.T) {
	lost := &Problem{Status: 409, Code: "claim_lost"}
	if !IsClaimLost(lost) || !IsOwnershipLost(lost) || IsTransient(lost) {
		t.Error("claim_lost")
	}
	if !IsOwnershipLost(&Problem{Status: 403, Code: "forbidden"}) || !IsOwnershipLost(&Problem{Status: 404, Code: "not_found"}) {
		t.Error("403/404 must stop a claimant")
	}
	if IsOwnershipLost(&Problem{Status: 409, Code: "invalid_transition"}) {
		t.Error("invalid_transition is not ownership loss")
	}
	if !IsTransient(&Problem{Status: 503}) || !IsTransient(&Problem{Status: 429}) || IsTransient(&Problem{Status: 400}) {
		t.Error("status classification")
	}
	if IsTransient(context.DeadlineExceeded) || IsTransient(fmt.Errorf("x: %w", context.Canceled)) {
		t.Error("own deadline is not transient")
	}
	if !IsTaskClaimed(&Problem{Status: 409, Code: "task_claimed"}) {
		t.Error("task_claimed")
	}
	if IsTransient(errors.New("plain")) {
		t.Error("plain error is not transient")
	}
}

func items(n int, dep func(i int) []BatchDep) []BatchItem {
	var out []BatchItem
	for i := 0; i < n; i++ {
		out = append(out, BatchItem{Ref: fmt.Sprintf("r%d", i), Thread: NewThread{Channel: "roadmap", Title: fmt.Sprintf("T%d", i), Text: "x"}, DependsOn: dep(i)})
	}
	return out
}

func TestPlanBatchesTopological(t *testing.T) {
	// r0 depends on r2; file order otherwise kept.
	its := items(4, func(i int) []BatchDep {
		if i == 0 {
			return []BatchDep{{Ref: "r2"}}
		}
		return nil
	})
	bs, err := PlanBatches(its, 2)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, b := range bs {
		for _, it := range b {
			order = append(order, it.Ref)
		}
	}
	if strings.Join(order, ",") != "r1,r2,r0,r3" || len(bs) != 2 {
		t.Fatalf("order %v in %d batches", order, len(bs))
	}
	cyc := items(2, func(i int) []BatchDep { return []BatchDep{{Ref: fmt.Sprintf("r%d", 1-i)}} })
	if _, err := PlanBatches(cyc, 100); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle not detected: %v", err)
	}
	if _, err := PlanBatches(items(1, func(int) []BatchDep { return []BatchDep{{Ref: "nope"}} }), 100); err == nil {
		t.Fatal("unknown ref accepted")
	}
}

// planServer accepts batches and label lookups, like a server would.
type planServer struct {
	mu      sync.Mutex
	batches []CreateTaskBatchRequest
	keys    []string
	byKey   map[string]map[string]any // replay
	labels  map[string]string         // "plan.id:X|plan.ref:R" -> task ID
	next    int
	fail    int // fail the batch with this 1-based call number
	calls   int
}

func (p *planServer) handle(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if r.Method == "GET" {
		q := r.URL.Query()["label"]
		var planID string
		var refs []string
		for _, l := range q {
			k, v, _ := strings.Cut(l, ":")
			if k == PlanLabelID {
				planID = v
			} else if k == PlanLabelRef {
				refs = append(refs, v)
			}
		}
		var out []any
		for _, ref := range refs {
			if id, ok := p.labels[planID+"|"+ref]; ok {
				out = append(out, task(id, "ready"))
			}
		}
		jsonOK(w, 200, map[string]any{"items": out, "next_page_token": nil})
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if v, ok := p.byKey[key]; ok {
		w.Header().Set("Idempotent-Replayed", "true")
		jsonOK(w, 201, v)
		return
	}
	p.calls++
	if p.calls == p.fail {
		jsonOK(w, 503, map[string]any{"status": 503, "code": "temporarily_unavailable"})
		return
	}
	var req CreateTaskBatchRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	p.batches = append(p.batches, req)
	p.keys = append(p.keys, key)
	var mapped []any
	refIDs := map[string]string{}
	for _, it := range req.Tasks {
		p.next++
		id := fmt.Sprintf("task_%d", p.next)
		refIDs[it.Ref] = id
		p.labels[it.Thread.Labels[PlanLabelID]+"|"+it.Thread.Labels[PlanLabelRef]] = id
		mapped = append(mapped, map[string]any{"ref": it.Ref, "task": task(id, "ready")})
	}
	resp := map[string]any{"batch_id": fmt.Sprintf("batch_%d", len(p.batches)), "tasks": mapped}
	p.byKey[key] = resp
	jsonOK(w, 201, resp)
}

func TestSubmitPlanResumable(t *testing.T) {
	ps := &planServer{byKey: map[string]map[string]any{}, labels: map[string]string{}, fail: 3}
	srv := httptest.NewServer(http.HandlerFunc(ps.handle))
	defer srv.Close()
	c := New(srv.URL, "tok")
	c.MaxRetries = 0
	tc := c.Tasks("b1")

	// A chain of 250 tasks: each depends on the previous one.
	plan := items(250, func(i int) []BatchDep {
		if i == 0 {
			return []BatchDep{{TaskID: "task_existing"}}
		}
		return []BatchDep{{Ref: fmt.Sprintf("r%d", i-1)}}
	})
	var saved *PlanProgress
	save := func(p *PlanProgress) error {
		b, _ := json.Marshal(p)
		saved = &PlanProgress{}
		return json.Unmarshal(b, saved)
	}
	// First run: batch 3 fails (calls: batch1, batch2, batch3 -> 503).
	_, err := tc.SubmitPlan(context.Background(), plan, PlanOptions{PlanID: "p1", Save: save})
	if err == nil {
		t.Fatal("expected the third batch to fail")
	}
	if saved == nil || len(saved.BatchesDone) != 2 || len(saved.Tasks) != 200 {
		t.Fatalf("progress after failure = %+v", saved)
	}
	// Rerun from the progress file: only batch 3 is sent.
	prog, err := tc.SubmitPlan(context.Background(), plan, PlanOptions{PlanID: "p1", Progress: saved, Save: save})
	if err != nil {
		t.Fatal(err)
	}
	if len(prog.Tasks) != 250 || len(ps.batches) != 3 {
		t.Fatalf("tasks %d, batches %d", len(prog.Tasks), len(ps.batches))
	}
	if strings.Join(ps.keys, ",") != "p1:1,p1:2,p1:3" {
		t.Fatalf("keys = %v", ps.keys)
	}
	// Cross-batch dependencies are task IDs; in-batch ones stay refs.
	first := ps.batches[1].Tasks[0]
	if len(first.DependsOn) != 1 || first.DependsOn[0].TaskID != prog.Tasks["r99"] {
		t.Errorf("cross-batch dep = %+v", first.DependsOn)
	}
	if d := ps.batches[1].Tasks[1].DependsOn; len(d) != 1 || d[0].Ref != "r100" {
		t.Errorf("in-batch dep = %+v", d)
	}
	if d := ps.batches[0].Tasks[0].DependsOn; d[0].TaskID != "task_existing" {
		t.Errorf("existing dep = %+v", d)
	}
	if l := ps.batches[0].Tasks[5].Thread.Labels; l[PlanLabelID] != "p1" || l[PlanLabelRef] != "r5" {
		t.Errorf("plan labels = %v", l)
	}
	// Rerun with no progress and expired receipts: every batch is found by
	// its plan labels and adopted; nothing is created again.
	ps.byKey = map[string]map[string]any{}
	prog2, err := tc.SubmitPlan(context.Background(), plan, PlanOptions{PlanID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ps.batches) != 3 {
		t.Fatalf("rerun created %d more batches", len(ps.batches)-3)
	}
	for ref, id := range prog.Tasks {
		if prog2.Tasks[ref] != id {
			t.Fatalf("rerun mapped %s to %s, want %s", ref, prog2.Tasks[ref], id)
		}
	}
}

func TestSubmitPlanSingleAtomicBatch(t *testing.T) {
	ps := &planServer{byKey: map[string]map[string]any{}, labels: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(ps.handle))
	defer srv.Close()
	tc := New(srv.URL, "tok").Tasks("b1")
	plan := items(100, func(int) []BatchDep { return nil })
	prog, err := tc.SubmitPlan(context.Background(), plan, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ps.batches) != 1 || len(ps.batches[0].Tasks) != 100 || len(prog.Tasks) != 100 {
		t.Fatalf("batches %d", len(ps.batches))
	}
	if !strings.HasPrefix(ps.keys[0], PlanID(plan)+":1") {
		t.Errorf("key %s", ps.keys[0])
	}
}
