package conformance

// P2 (abby.p2.tasks) black-box conformance. Requirement IDs refer to
// spec/p2-tasks.md; scenario IDs to its §20 and design §14.1. Every test
// skips unless GET /v1/server lists the abby.p2.tasks profile.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestP2Discovery(t *testing.T) { // TASK-001, TASK-002, TASK-003
	d := requireP2(t)
	for _, k := range []string{"claim_lease_default_seconds", "claim_lease_min_seconds", "claim_lease_max_seconds",
		"max_batch_tasks", "max_dependencies_per_task", "max_task_result_bytes", "max_evidence_items", "max_claim_wait_ms"} {
		if d.lim(t, k) <= 0 {
			t.Fatalf("limits.%s must be positive", k)
		}
	}
	mn, df, mx := d.lim(t, "claim_lease_min_seconds"), d.lim(t, "claim_lease_default_seconds"), d.lim(t, "claim_lease_max_seconds")
	if !(mn <= df && df <= mx) {
		t.Fatalf("lease bounds min %d default %d max %d", mn, df, mx)
	}
	if d.capsMap {
		if _, ok := d.raw["capabilities"].(J)["claim_proof_replay"].(bool); !ok {
			t.Fatal("capabilities.claim_proof_replay must be a boolean (TASK-003)")
		}
	}
}

// ---------------------------------------------------------------------------

func TestCT007ClaimRace(t *testing.T) { // CLAIM-001, CLAIM-002, CLAIM-003, SEL-001, SEL-005
	f := newP2Board(t)
	m := f.manager(t)
	ws := make([]*actorClient, 8)
	for i := range ws {
		ws[i] = f.worker(t, fmt.Sprint("race", i))
	}
	cur := f.head(t, m)
	id := str(f.createTask(t, m, "race", "race", "one winner", nil), "id")

	res := make([]resp, len(ws))
	var fns []func()
	for i, w := range ws {
		i, w := i, w
		fns = append(fns, func() { res[i] = f.claimResp(w, id, nil) })
	}
	race(fns...)
	win := -1
	for i, r := range res {
		switch {
		case r.Status == 201:
			if win >= 0 {
				t.Fatalf("two claims committed (claimers %d and %d)", win, i)
			}
			win = i
		case r.Status == 409 && r.code() == "task_claimed":
			if _, ok := r.Body["epoch"].(float64); !ok {
				t.Fatalf("task_claimed without epoch: %s", r.Raw)
			}
		default:
			t.Fatalf("claimer %d: unexpected %d %s", i, r.Status, r.Raw)
		}
	}
	if win < 0 {
		t.Fatal("no claim committed")
	}
	c1 := obj(res[win].Body, "claim")
	if num(c1, "epoch") != 1 || c1["actor_id"] != ws[win].id || c1["status"] != "active" || str(c1, "execution_id") == "" {
		t.Fatalf("winning claim: %v", c1)
	}
	v := f.getTask(t, m, id)
	if v["state"] != "claimed" || obj(v, "current_claim")["id"] != c1["id"] || num(v, "epoch") != 1 {
		t.Fatalf("task after race: %v", v)
	}
	evs, _ := f.since(t, m, cur)
	if n := countType(evs, "task.claimed"); n != 1 {
		t.Fatalf("want one task.claimed, got %d", n)
	}

	// Epochs strictly increase and each claim is a new execution, also for
	// re-acquisition by the same actor (CLAIM-003).
	seen := map[string]bool{str(c1, "id"): true, str(c1, "execution_id"): true}
	holder, prev := ws[win], res[win].Body
	for i, next := range []*actorClient{ws[(win+1)%len(ws)], ws[win]} {
		f.mustCmd(t, holder, id, "release", nil, proof(str(prev, "proof")))
		prev = f.claim(t, next, id, nil)
		c := obj(prev, "claim")
		if num(c, "epoch") != float64(i+2) || seen[str(c, "id")] || seen[str(c, "execution_id")] {
			t.Fatalf("re-claim %d: epoch %v (want %d), claim/execution must be new: %v", i, c["epoch"], i+2, c)
		}
		seen[str(c, "id")], seen[str(c, "execution_id")] = true, true
		holder = next
	}
	hist := m.must(200, m.do("GET", f.tp(id, "/claims"), nil))
	var epochs []float64
	for _, it := range arr(hist.Body, "items") {
		epochs = append(epochs, num(it.(J), "epoch"))
	}
	if fmt.Sprint(epochs) != "[3 2 1]" {
		t.Fatalf("claim history epochs %v, want newest first [3 2 1]", epochs)
	}
	if strings.Contains(string(hist.Raw), str(prev, "proof")) {
		t.Fatal("claim history contains a proof")
	}

	// Claim-next variant: N claimers, 3 tasks; each task claimed once.
	var ids []string
	for i := 0; i < 3; i++ {
		ids = append(ids, str(f.createTask(t, m, "race-next", fmt.Sprint("n", i), "x", nil), "id"))
	}
	fns = nil
	for i, w := range ws {
		i, w := i, w
		fns = append(fns, func() { res[i] = f.claimNext(w, J{"channel": "race-next"}) })
	}
	race(fns...)
	got := map[string]int{}
	for i, r := range res {
		switch {
		case r.Status == 201 && r.Body["outcome"] == "claimed":
			got[str(obj(r.Body, "claim"), "task_id")]++
		case r.Status == 200 && r.Body["outcome"] == "no_work":
		default:
			t.Fatalf("claim-next %d: %d %s", i, r.Status, r.Raw)
		}
	}
	for _, x := range ids {
		if got[x] != 1 {
			t.Fatalf("claim-next race: task %s claimed %d times (%v)", x, got[x], got)
		}
	}
	if len(got) != 3 {
		t.Fatalf("claim-next race claimed unexpected tasks: %v", got)
	}
}

func TestCT008ExpiredProofRejected(t *testing.T) { // CLAIM-014, CLAIM-015, CLAIM-020, CLAIM-021, CLAIM-022, CLAIM-050..053, TASK-023, TASK-031
	f := newP2Board(t)
	mn, mx, df := f.d.lim(t, "claim_lease_min_seconds"), f.d.lim(t, "claim_lease_max_seconds"), f.d.lim(t, "claim_lease_default_seconds")
	m := f.manager(t)
	a, b := f.worker(t, "a"), f.worker(t, "b")

	// Lease bounds: no silent clamping; default applied (CLAIM-020, CLAIM-021).
	bt := str(f.createTask(t, m, "bounds", "b", "x", nil), "id")
	a.wantProblem(400, "validation_failed", f.claimResp(a, bt, J{"lease_seconds": mn - 1}))
	a.wantProblem(400, "validation_failed", f.claimResp(a, bt, J{"lease_seconds": mx + 1}))
	dc := f.claim(t, a, bt, nil)
	dcl := obj(dc, "claim")
	if num(dcl, "lease_seconds") != float64(df) || !ts(t, dcl["expires_at"]).Equal(ts(t, dcl["acquired_at"]).Add(time.Duration(df)*time.Second)) {
		t.Fatalf("default lease: %v", dcl)
	}
	a.wantProblem(400, "validation_failed", f.renew(a, bt, str(dcl, "id"), str(dc, "proof"), J{"lease_seconds": mx + 1}))
	f.mustCmd(t, a, bt, "release", nil, proof(str(dc, "proof")))

	id := str(f.createTask(t, m, "lapse", "l", "x", nil), "id")
	cur := f.head(t, m)
	ca := f.claim(t, a, id, J{"lease_seconds": mn})
	clA, pa := obj(ca, "claim"), str(ca, "proof")
	cidA := str(clA, "id")
	revClaimed := num(obj(ca, "task"), "revision")
	if !ts(t, clA["expires_at"]).Equal(ts(t, clA["acquired_at"]).Add(time.Duration(mn) * time.Second)) {
		t.Fatalf("expires_at != acquired_at + lease: %v", clA)
	}

	v := f.waitLapsed(t, m, id, cidA, mn)
	lc := obj(v, "last_claim")
	if v["state"] != "ready" || v["current_claim"] != nil || lc["id"] != cidA || lc["status"] != "expired" ||
		!sameInstant(t, lc["ended_at"], clA["expires_at"]) || num(v, "revision") != revClaimed+1 ||
		!sameInstant(t, v["updated_at"], clA["expires_at"]) || !sameInstant(t, v["state_changed_at"], clA["expires_at"]) {
		t.Fatalf("effective view after lapse (CLAIM-051): %v", v)
	}
	if er := lc["end_recorded_at"]; er != nil && ts(t, er).Before(ts(t, clA["expires_at"])) {
		t.Fatalf("end_recorded_at before expires_at: %v", lc)
	}

	stale := func(state string, epoch float64) {
		t.Helper()
		for _, c := range []struct {
			verb string
			body J
		}{{"release", nil}, {"complete", complete("late")}, {"fail", reason("late")}, {"block", reason("late")}} {
			wantClaimLost(t, a, state, epoch, f.cmd(a, id, c.verb, c.body, proof(pa)))
		}
		wantClaimLost(t, a, state, epoch, f.renew(a, id, cidA, pa, nil))
	}
	// Expired (maybe not yet recorded): rejected, nothing committed.
	stale("ready", 1)
	if v2 := f.getTask(t, m, id); v2["state"] != "ready" || num(v2, "revision") != num(v, "revision") {
		t.Fatalf("rejected stale operations changed the task: %v", v2)
	}

	// Another client acquires at once; old proof cannot touch the new work.
	cb := f.claim(t, b, id, nil)
	clB, pb := obj(cb, "claim"), str(cb, "proof")
	if num(clB, "epoch") != 2 {
		t.Fatalf("new claim epoch %v", clB["epoch"])
	}
	stale("claimed", 2)
	wantClaimLost(t, b, "claimed", 2, f.renew(b, id, cidA, pb, nil)) // CLAIM-015
	if cc := obj(f.getTask(t, m, id), "current_claim"); cc["id"] != clB["id"] {
		t.Fatalf("stale proof disturbed the new claim: %v", cc)
	}
	b.must(200, f.renew(b, id, str(clB, "id"), pb, nil))
	if done := f.mustCmd(t, b, id, "complete", complete("ok"), proof(pb)); done["state"] != "completed" {
		t.Fatalf("complete: %v", done)
	}
	stale("completed", 2)

	evs, _ := f.since(t, m, cur)
	n, pos := 0, -1
	for i, e := range evs {
		if e["type"] == "task.claim_expired" {
			n++
			pos = i
			cl := obj(obj(e, "payload"), "claim")
			if !sameInstant(t, e["effective_at"], clA["expires_at"]) || num(obj(e, "resource"), "revision") != revClaimed+1 || cl["status"] != "expired" || cl["id"] != cidA {
				t.Fatalf("task.claim_expired (CLAIM-052): %v", e)
			}
			if nx := evs[i+1]; nx["type"] != "thread.view_invalidated" {
				t.Fatalf("claim_expired not followed by view invalidation: %v", types(evs))
			}
		}
	}
	if n != 1 {
		t.Fatalf("lapse must be materialized exactly once, got %d: %v", n, types(evs))
	}
	for i, e := range evs {
		if e["type"] == "task.claimed" && obj(obj(e, "payload"), "claim")["id"] == clB["id"] && i < pos {
			t.Fatalf("new claim committed before the lapse event: %v", types(evs))
		}
	}
	checkTaskEvents(t, evs)
}

func TestCT009LostResponses(t *testing.T) { // CLAIM-040, CLAIM-041, CLAIM-042, CLAIM-044, CLAIM-045, TASK-070, TASK-071
	f := newP2Board(t)
	mn := f.d.lim(t, "claim_lease_min_seconds")
	m := f.manager(t)
	w, v := f.worker(t, "w"), f.worker(t, "v")

	// Lost claim response.
	id := str(f.createTask(t, m, "lost", "l", "x", nil), "id")
	k := idem("claim-" + rnd())
	r1 := w.must(201, f.claimResp(w, id, nil, k))
	c1, p1 := obj(r1.Body, "claim"), str(r1.Body, "proof")
	cur := f.head(t, m)
	r2 := w.must(201, f.claimResp(w, id, nil, k))
	c2 := obj(r2.Body, "claim")
	if r2.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatal("claim replay must be marked")
	}
	for _, fld := range []string{"id", "execution_id", "epoch", "expires_at", "revision"} {
		if fmt.Sprint(c1[fld]) != fmt.Sprint(c2[fld]) {
			t.Fatalf("claim replay changed %s: %v vs %v", fld, c1[fld], c2[fld])
		}
	}
	if f.d.proofReplay {
		if r2.Body["proof"] != p1 || r2.Body["proof_unavailable"] != false {
			t.Fatalf("claim_proof_replay=true, active claim: replay must return the proof: %s", r2.Raw)
		}
	} else if r2.Body["proof"] != nil || r2.Body["proof_unavailable"] != true {
		t.Fatalf("claim_proof_replay=false: replay must have proof null, proof_unavailable true: %s", r2.Raw)
	}
	if evs, _ := f.since(t, m, cur); len(evs) != 0 {
		t.Fatalf("claim replay emitted events: %v", types(evs))
	}
	w.wantProblem(409, "idempotency_conflict", f.claimResp(w, id, J{"lease_seconds": mn}, k))

	// Renew replay returns the current claim view and never extends it.
	kr := idem("renew-" + rnd())
	rn := w.must(200, f.renew(w, id, str(c1, "id"), p1, nil, kr)).Body
	time.Sleep(1100 * time.Millisecond)
	rn2 := w.must(200, f.renew(w, id, str(c1, "id"), p1, nil, kr))
	if rn2.Header.Get("Idempotent-Replayed") != "true" || !sameInstant(t, rn2.Body["expires_at"], rn["expires_at"]) || rn2.Body["revision"] != rn["revision"] {
		t.Fatalf("renew replay changed the claim: %v vs %v", rn2.Body, rn)
	}

	// Lost completion response.
	kc := idem("complete-" + rnd())
	d1 := w.must(200, f.cmd(w, id, "complete", complete("done"), proof(p1), kc))
	d2 := w.must(200, f.cmd(w, id, "complete", complete("done"), proof(p1), kc))
	if d1.Body["state"] != "completed" || d2.Body["state"] != "completed" || d2.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("completion replay: %s", d2.Raw)
	}
	w.wantProblem(409, "idempotency_conflict", f.cmd(w, id, "complete", complete("different"), proof(p1), kc))
	r3 := w.must(201, f.claimResp(w, id, nil, k)) // replay after the claim ended
	if obj(r3.Body, "claim")["id"] != c1["id"] || r3.Body["proof"] != nil || r3.Body["proof_unavailable"] != true {
		t.Fatalf("claim replay after completion: %s", r3.Raw)
	}
	evs, _ := f.since(t, m, cur)
	if countType(evs, "task.completed") != 1 || countType(evs, "task.claimed") != 0 || countType(evs, "task.claim_renewed") != 1 {
		t.Fatalf("replays must not emit events: %v", types(evs))
	}

	// Replay after a lapse: the claim stays lapsed (CLAIM-042, CT-009).
	id2 := str(f.createTask(t, m, "lost", "l2", "x", nil), "id")
	k2, kr2 := idem("claim2-"+rnd()), idem("renew2-"+rnd())
	lr := w.must(201, f.claimResp(w, id2, J{"lease_seconds": mn}, k2)).Body
	lc, lp := obj(lr, "claim"), str(lr, "proof")
	ren := w.must(200, f.renew(w, id2, str(lc, "id"), lp, J{"lease_seconds": mn}, kr2)).Body
	f.waitLapsed(t, m, id2, str(lc, "id"), mn+1)
	cur2 := f.head(t, m)
	rr := w.must(201, f.claimResp(w, id2, J{"lease_seconds": mn}, k2))
	rc := obj(rr.Body, "claim")
	if rr.Header.Get("Idempotent-Replayed") != "true" || rc["id"] != lc["id"] || rc["status"] != "expired" ||
		rr.Body["proof"] != nil || rr.Body["proof_unavailable"] != true || !sameInstant(t, rc["expires_at"], ren["expires_at"]) {
		t.Fatalf("claim replay after lapse: %s", rr.Raw)
	}
	rr2 := w.must(200, f.renew(w, id2, str(lc, "id"), lp, J{"lease_seconds": mn}, kr2))
	if rr2.Body["status"] != "expired" || !sameInstant(t, rr2.Body["expires_at"], ren["expires_at"]) {
		t.Fatalf("renew replay after lapse must not restore the lease: %s", rr2.Raw)
	}
	if tv := f.getTask(t, m, id2); tv["state"] != "ready" || tv["current_claim"] != nil {
		t.Fatalf("task after replays: %v", tv)
	}
	evs, _ = f.since(t, m, cur2)
	if countType(evs, "task.claimed")+countType(evs, "task.claim_renewed") != 0 {
		t.Fatalf("replay after lapse emitted claim events: %v", types(evs))
	}
	if c := obj(f.claim(t, v, id2, nil), "claim"); num(c, "epoch") != 2 {
		t.Fatalf("claim after lapsed replay: %v", c)
	}

	// Rejected claims and no_work do not reserve keys (CLAIM-045).
	id3 := str(f.createTask(t, m, "lost", "l3", "x", nil), "id")
	h := f.claim(t, w, id3, nil)
	kv := idem("rej-" + rnd())
	v.wantProblem(409, "task_claimed", f.claimResp(v, id3, nil, kv))
	f.mustCmd(t, w, id3, "release", nil, proof(str(h, "proof")))
	if r := v.must(201, f.claimResp(v, id3, nil, kv)); r.Header.Get("Idempotent-Replayed") == "true" {
		t.Fatal("a rejected claim reserved its key")
	}
	ch := "empty-" + rnd()
	kn := idem("next-" + rnd())
	if r := v.must(200, f.claimNext(v, J{"channel": ch}, kn)); r.Body["outcome"] != "no_work" {
		t.Fatalf("claim-next on empty channel: %s", r.Raw)
	}
	nt := str(f.createTask(t, m, ch, "n", "x", nil), "id")
	if r := v.must(201, f.claimNext(v, J{"channel": ch}, kn)); obj(r.Body, "claim")["task_id"] != nt || r.Header.Get("Idempotent-Replayed") == "true" {
		t.Fatalf("no_work reserved its key: %s", r.Raw)
	}
}

func TestCT010RenewCancelCompleteRace(t *testing.T) { // CLAIM-030, CLAIM-033, TASK-032, TASK-034
	f := newP2Board(t)
	m := f.manager(t)
	w := f.worker(t, "w")
	for round := 0; round < 6; round++ {
		id := str(f.createTask(t, m, "race", fmt.Sprint("r", round), "x", nil), "id")
		c := f.claim(t, w, id, nil)
		cid, p := str(obj(c, "claim"), "id"), str(c, "proof")
		cur := f.head(t, m)
		var rRenew, rDone, rCancel resp
		race(
			func() { rRenew = f.renew(w, id, cid, p, nil) },
			func() { rDone = f.cmd(w, id, "complete", complete("race"), proof(p)) },
			func() { rCancel = f.cmd(m, id, "cancel", reason("race")) },
		)
		v := f.getTask(t, m, id)
		final := str(v, "state")
		switch {
		case rDone.Status == 200 && rCancel.Status == 200:
			t.Fatalf("round %d: both complete and cancel committed", round)
		case rDone.Status == 200:
			wantInvalid(t, m, "completed", rCancel)
			if final != "completed" || obj(v, "last_claim")["status"] != "completed" {
				t.Fatalf("round %d: complete won but task %v", round, v)
			}
		case rCancel.Status == 200:
			wantClaimLost(t, w, "canceled", 1, rDone)
			if final != "canceled" || obj(v, "last_claim")["status"] != "revoked" {
				t.Fatalf("round %d: cancel won but task %v", round, v)
			}
		default:
			t.Fatalf("round %d: neither settled: complete %d %s / cancel %d %s", round, rDone.Status, rDone.Raw, rCancel.Status, rCancel.Raw)
		}
		renewed := 0
		if rRenew.Status == 200 {
			renewed = 1
		} else {
			wantClaimLost(t, w, final, 1, rRenew)
		}
		wantClaimLost(t, w, final, 1, f.renew(w, id, cid, p, nil))
		wantClaimLost(t, w, final, 1, f.cmd(w, id, "release", nil, proof(p)))
		wantClaimLost(t, w, final, 1, f.cmd(w, id, "fail", reason("x"), proof(p)))
		evs, _ := f.since(t, m, cur)
		if countType(evs, "task.completed")+countType(evs, "task.canceled") != 1 || countType(evs, "task.claim_renewed") != renewed {
			t.Fatalf("round %d: events %v", round, types(evs))
		}
		for _, e := range evs {
			if e["type"] == "task.canceled" && obj(obj(e, "payload"), "revoked_claim")["id"] != cid {
				t.Fatalf("task.canceled must name the revoked claim: %v", e)
			}
		}
		checkTaskEvents(t, evs)
	}
}

func TestCT011PrerequisiteFailReopenComplete(t *testing.T) { // DEP-004, DEP-007, DEP-008, TASK-015, TASK-035, TASK-036
	f := newP2Board(t)
	m := f.manager(t)
	w := f.worker(t, "w")
	pid := str(f.createTask(t, m, "dep", "prereq", "x", nil), "id")
	d := f.createTask(t, m, "dep", "dependent", "x", J{"depends_on": []string{pid}})
	did := str(d, "id")
	if d["state"] != "waiting" || fmt.Sprint(arr(d, "dependencies")) != fmt.Sprint([]any{J{"task_id": pid, "state": "ready"}}) || d["unreadable_dependencies_pending"] != false {
		t.Fatalf("dependent at creation: %v", d)
	}
	d2id := str(f.createTask(t, m, "dep", "blocked dependent", "x", J{"depends_on": []string{pid}}), "id")
	f.mustCmd(t, m, d2id, "block", reason("hold"))
	wantInvalid(t, w, "waiting", f.claimResp(w, did, nil))

	c := f.claim(t, w, pid, nil)
	f.mustCmd(t, w, pid, "fail", reason("broken"), proof(str(c, "proof")))
	if dv := f.getTask(t, m, did); dv["state"] != "waiting" || fmt.Sprint(arr(dv, "dependencies")) != fmt.Sprint([]any{J{"task_id": pid, "state": "failed"}}) {
		t.Fatalf("dependent after prerequisite failed: %v", dv)
	}
	pv := f.mustCmd(t, m, pid, "reopen", nil)
	if pv["state"] != "ready" || num(pv, "retry_count") != 1 || pv["outcome"] != nil || obj(pv, "last_claim")["status"] != "failed" {
		t.Fatalf("reopened prerequisite: %v", pv)
	}
	if dv := f.getTask(t, m, did); dv["state"] != "waiting" || len(arr(dv, "dependencies")) != 1 {
		t.Fatalf("dependent after reopen (links kept): %v", dv)
	}

	// A canceled prerequisite also stays unsatisfied; nothing cascades.
	qid := str(f.createTask(t, m, "dep", "q", "x", nil), "id")
	d3id := str(f.createTask(t, m, "dep", "d3", "x", J{"depends_on": []string{qid}}), "id")
	f.mustCmd(t, m, qid, "cancel", reason("drop"))
	if dv := f.getTask(t, m, d3id); dv["state"] != "waiting" {
		t.Fatalf("dependent of canceled prerequisite: %v", dv)
	}

	c2 := f.claim(t, w, pid, nil)
	if num(obj(c2, "claim"), "epoch") != 2 {
		t.Fatalf("re-claim after reopen: %v", c2["claim"])
	}
	cur := f.head(t, m)
	f.mustCmd(t, w, pid, "complete", complete("ok"), proof(str(c2, "proof")))
	evs, _ := f.since(t, m, cur)
	wantTypes(t, "complete with dependents", evs, "task.completed", "thread.view_invalidated", "task.became_ready", "thread.view_invalidated")
	checkTaskEvents(t, evs)
	br := obj(evs[2], "payload")
	if obj(br, "task")["id"] != did || br["cause"] != "dependencies_satisfied" || br["prerequisite_task_id"] != pid {
		t.Fatalf("task.became_ready payload: %v", br)
	}
	if dv := f.getTask(t, m, did); dv["state"] != "ready" {
		t.Fatalf("dependent after completion: %v", dv)
	}
	if dv := f.getTask(t, m, d2id); dv["state"] != "blocked" {
		t.Fatalf("blocked dependent must stay blocked: %v", dv)
	}
	if dv := f.mustCmd(t, m, d2id, "unblock", nil); dv["state"] != "ready" {
		t.Fatalf("unblock evaluates prerequisites (TASK-035): %v", dv)
	}
	f.mustCmd(t, m, d3id, "block", reason("x"))
	if dv := f.mustCmd(t, m, d3id, "unblock", nil); dv["state"] != "waiting" {
		t.Fatalf("unblock with an unsatisfied prerequisite: %v", dv)
	}
	// Activation is synchronous: claim-next sees the dependent at once.
	if r := w.must(201, f.claimNext(w, J{"channel": "dep"})); obj(r.Body, "claim")["task_id"] != did {
		t.Fatalf("claim-next after activation: %s", r.Raw)
	}
}

func TestCT012AtomicBatchRejection(t *testing.T) { // BATCH-001..005, DEP-001, DEP-002, DEP-003
	f := newP2Board(t)
	m := f.manager(t)
	maxBatch := f.d.lim(t, "max_batch_tasks")
	hider := newActor(t, "hid")
	f.grant(t, hider, "vault", "allow", "read", "post", "task_manage", "channel_manage")
	f.channel(t, hider, "vault", "private")
	hid := str(f.createTask(t, hider, "vault", "hidden", "x", nil), "id")
	f.grant(t, m, "nomanage", "deny", "task_manage")
	f2 := &tf{fixture: newBoard(t), d: f.d}
	f2.grant(t, m, "*", "allow", "read", "post", "task_manage", "channel_manage")
	xid := str(f2.createTask(t, m, "x", "other board", "x", nil), "id")
	cur := f.head(t, m)

	item := func(ref, ch string, deps ...J) J {
		it := J{"ref": ref, "thread": J{"channel": ch, "title": "item " + ref, "text": "batch " + ref}}
		if len(deps) > 0 {
			it["depends_on"] = deps
		}
		return it
	}
	batch := func(items ...J) resp { return m.do("POST", f.p("/tasks/batch"), J{"tasks": items}, newKey()) }
	nothingCreated := func(name string) {
		t.Helper()
		if n := len(arr(m.must(200, m.do("GET", f.p("/tasks"), nil)).Body, "items")); n != 0 {
			t.Fatalf("%s: %d tasks exist after a rejected batch", name, n)
		}
		if n := len(arr(m.must(200, m.do("GET", f.p("/threads"), nil)).Body, "threads")); n != 0 {
			t.Fatalf("%s: %d threads exist after a rejected batch", name, n)
		}
		if evs, _ := f.since(t, m, cur); len(evs) != 0 {
			t.Fatalf("%s: rejected batch emitted %v", name, types(evs))
		}
	}
	rejected := func(name string, r resp, wantCode string) {
		t.Helper()
		if r.Status < 400 || r.Status >= 500 {
			t.Fatalf("%s: want rejection, got %d %s", name, r.Status, r.Raw)
		}
		errs := arr(r.Body, "errors")
		if len(errs) == 0 {
			t.Fatalf("%s: problem must list errors (BATCH-003): %s", name, r.Raw)
		}
		found := r.code() == wantCode
		for _, e := range errs {
			ej := e.(J)
			if _, ok := ej["index"].(float64); !ok || str(ej, "code") == "" {
				t.Fatalf("%s: error entry %v needs index and code", name, ej)
			}
			if ej["code"] == wantCode {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: want code %s in problem or errors: %s", name, wantCode, r.Raw)
		}
		// Echoing the caller's own input reveals nothing; what must not happen
		// is a hidden ID being answered differently from an unknown one.
		if name == "hidden prerequisite" || name == "cross-board prerequisite" {
			norm := func(raw []byte, id string) string {
				var m J
				_ = json.Unmarshal(raw, &m)
				delete(m, "request_id")
				b, _ := json.Marshal(m)
				return strings.ReplaceAll(string(b), id, "<ID>")
			}
			missing := "task_missing_" + rnd()
			u := batch(item("ok", "plan"), item("a", "plan", J{"task_id": missing}))
			sent := hid
			if name == "cross-board prerequisite" {
				sent = xid
				u = batch(item("a", "plan", J{"task_id": missing}))
			}
			if norm(r.Raw, sent) != norm(u.Raw, missing) {
				t.Fatalf("%s: answered differently from an unknown ID:\n%s\n%s", name, r.Raw, u.Raw)
			}
		}
		nothingCreated(name)
	}
	rejected("cycle", batch(item("ok", "plan"), item("a", "plan", J{"ref": "b"}), item("b", "plan", J{"ref": "a"})), "dependency_cycle")
	rejected("self", batch(item("a", "plan", J{"ref": "a"})), "dependency_cycle")
	rejected("hidden prerequisite", batch(item("ok", "plan"), item("a", "plan", J{"task_id": hid})), "not_found")
	rejected("cross-board prerequisite", batch(item("a", "plan", J{"task_id": xid})), "not_found")
	rejected("unknown prerequisite", batch(item("a", "plan", J{"task_id": "task_missing_" + rnd()})), "not_found")
	rejected("unauthorized channel", batch(item("ok", "plan"), item("a", "nomanage")), "forbidden")
	rejected("hidden channel", batch(item("ok", "plan"), item("a", "vault")), "not_found")
	if r := batch(item("a", "plan"), item("a", "plan")); r.Status != 400 {
		t.Fatalf("duplicate ref: %d %s", r.Status, r.Raw)
	}
	if r := batch(item("a", "plan", J{"ref": "zz"})); r.Status < 400 || r.Status >= 500 {
		t.Fatalf("unknown ref accepted: %d %s", r.Status, r.Raw)
	}
	big := []J{}
	for i := 0; i <= maxBatch; i++ {
		big = append(big, item(fmt.Sprint("i", i), "plan"))
	}
	m.wantProblem(413, "limit_exceeded", batch(big...))
	m.wantProblem(400, "validation_failed", m.do("POST", f.p("/tasks/batch"), J{"tasks": []J{item("a", "plan")}})) // no key
	nothingCreated("after all rejections")

	// Success: request order, ref mapping, replay.
	k := idem("batch-" + rnd())
	body := J{"tasks": []J{item("a", "plan"), item("b", "plan/sub"), item("c", "plan", J{"ref": "a"}, J{"ref": "b"})}}
	r := m.must(201, m.do("POST", f.p("/tasks/batch"), body, k))
	bid := str(r.Body, "batch_id")
	got := arr(r.Body, "tasks")
	if bid == "" || len(got) != 3 {
		t.Fatalf("batch response: %s", r.Raw)
	}
	ids := map[string]string{}
	var order []string
	for i, x := range got {
		xj := x.(J)
		tk := obj(xj, "task")
		if xj["ref"] != []string{"a", "b", "c"}[i] || tk["origin"] != "batch" {
			t.Fatalf("batch item %d: %v", i, xj)
		}
		ids[str(xj, "ref")] = str(tk, "id")
		order = append(order, str(tk, "id"))
	}
	if obj(got[2].(J), "task")["state"] != "waiting" || obj(got[0].(J), "task")["state"] != "ready" {
		t.Fatalf("batch states: %s", r.Raw)
	}
	c := f.getTask(t, m, ids["c"])
	var deps []string
	for _, d := range arr(c, "dependencies") {
		deps = append(deps, str(d.(J), "task_id"))
	}
	sort.Strings(deps)
	want := []string{ids["a"], ids["b"]}
	sort.Strings(want)
	if fmt.Sprint(deps) != fmt.Sprint(want) {
		t.Fatalf("batch refs resolved to %v, want %v", deps, want)
	}
	evs, _ := f.since(t, m, cur)
	// Channels a batch creates on first post are not part of BATCH-004's
	// pair ordering; drop them before checking the pairs.
	kept := evs[:0]
	for _, e := range evs {
		if str(e, "type") != "channel.created" {
			kept = append(kept, e)
		}
	}
	evs = kept
	wantTypes(t, "batch events", evs, "thread.created", "task.created", "thread.created", "task.created", "thread.created", "task.created")
	for i := 0; i < 3; i++ {
		pl := obj(evs[2*i+1], "payload")
		if obj(pl, "task")["id"] != order[i] || pl["batch_id"] != bid || pl["origin"] != "batch" {
			t.Fatalf("task.created %d out of request order or wrong payload: %v", i, pl)
		}
	}
	r2 := m.must(201, m.do("POST", f.p("/tasks/batch"), body, k))
	if r2.Header.Get("Idempotent-Replayed") != "true" || r2.Body["batch_id"] != bid {
		t.Fatalf("batch replay: %s", r2.Raw)
	}
	for i, x := range arr(r2.Body, "tasks") {
		if obj(x.(J), "task")["id"] != order[i] {
			t.Fatalf("batch replay mapping changed: %s", r2.Raw)
		}
	}
}

func TestCT025ConversionAuthority(t *testing.T) { // TASK-011, TASK-012, TASK-013, TASK-014, TASK-016, BRIEF-001, BRIEF-010
	f := newP2Board(t)
	author := newActor(t, "author")
	f.grant(t, author, "*", "allow", "read", "post", "channel_manage")
	part := newActor(t, "part")
	f.grant(t, part, "*", "allow", "read", "post", "task_manage")
	tadmin := newActor(t, "tadmin")
	f.grant(t, tadmin, "*", "allow", "read", "post", "task_admin")
	authorMgr := newActor(t, "amgr")
	f.grant(t, authorMgr, "*", "allow", "read", "post", "task_manage", "channel_manage")
	conv := func(by *actorClient, thread string, opts ...opt) resp {
		return by.do("POST", f.p("/tasks"), J{"thread_id": thread}, append([]opt{newKey()}, opts...)...)
	}

	th, root := f.post(t, author, "conv", "Plan", "please do X")
	tid, rid := str(th, "id"), str(root, "id")
	cur := f.head(t, author)
	part.wantProblem(403, "forbidden", conv(part, tid))
	author.wantProblem(403, "forbidden", conv(author, tid)) // author without task_manage
	tadmin.wantProblem(400, "validation_failed", tadmin.do("POST", f.p("/tasks"), J{"thread_id": tid}))
	tv := author.must(200, author.do("GET", f.p("/threads/"+tid), nil)).Body["thread"].(J)
	if tv["task_id"] != nil {
		t.Fatalf("refused conversion changed the thread: %v", tv)
	}
	if evs, _ := f.since(t, author, cur); len(evs) != 0 {
		t.Fatalf("refused conversion emitted %v", types(evs))
	}
	// Author controls stay intact after a refused conversion.
	author.must(200, author.do("PATCH", f.p("/messages/"+rid), J{"text": "please do X, edited"}, ifMatch(1)))
	author.must(200, author.do("PATCH", f.p("/threads/"+tid), J{"title": "Plan v2"}, ifMatch(tv["revision"])))

	cur = f.head(t, author)
	tk := tadmin.must(201, conv(tadmin, tid)).Body
	taskID := str(tk, "id")
	if tk["owner_actor_id"] != tadmin.id || tk["created_by_actor_id"] != tadmin.id || tk["origin"] != "converted" ||
		tk["state"] != "ready" || num(tk, "brief_revision") != 1 || tk["thread_id"] != tid || tk["title"] != "Plan v2" {
		t.Fatalf("converted task: %v", tk)
	}
	evs, _ := f.since(t, author, cur)
	wantTypes(t, "conversion", evs, "task.created", "thread.view_invalidated")
	checkTaskEvents(t, evs)
	rm := author.must(200, author.do("GET", f.p("/messages/"+rid), nil)).Body
	if rm["author_actor_id"] != author.id || rm["text"] != "please do X, edited" || num(rm, "revision") != 2 || num(rm, "seq") != 1 {
		t.Fatalf("conversion changed the root message: %v", rm)
	}
	if v := author.must(200, author.do("GET", f.p("/threads/"+tid), nil)).Body["thread"].(J); v["task_id"] != taskID {
		t.Fatalf("thread.task_id = %v, want %s (TASK-024)", v["task_id"], taskID)
	}
	b1 := tadmin.must(200, tadmin.do("GET", f.tp(taskID, "/briefs/1"), nil)).Body
	src := obj(b1, "source")
	if b1["text"] != "please do X, edited" || src["kind"] != "root" || src["message_id"] != rid || num(src, "message_revision") != 2 {
		t.Fatalf("brief revision 1 (TASK-014): %v", b1)
	}
	// Root author gains no task rights; P1 root edits are now protected.
	author.wantProblem(403, "forbidden", f.cmd(author, taskID, "cancel", reason("mine")))
	r := author.do("PATCH", f.p("/messages/"+rid), J{"text": "sneaky"}, ifMatch(2))
	author.wantProblem(409, "task_root_protected", r)
	if r.Body["task_id"] != taskID {
		t.Fatalf("task_root_protected must name the task: %s", r.Raw)
	}
	r = conv(tadmin, tid)
	tadmin.wantProblem(409, "already_exists", r)
	if r.Body["task_id"] != taskID {
		t.Fatalf("already_exists must name the readable task: %s", r.Raw)
	}
	// The root author with task_manage may convert its own thread.
	th2, _ := f.post(t, authorMgr, "conv", "Mine", "own work")
	if own := authorMgr.must(201, conv(authorMgr, str(th2, "id"))).Body; own["owner_actor_id"] != authorMgr.id {
		t.Fatalf("own conversion: %v", own)
	}
	// Tombstoned root cannot be converted.
	th3, root3 := f.post(t, author, "conv", "Gone", "deleted soon")
	author.must(200, author.do("DELETE", f.p("/messages/"+str(root3, "id")), nil, ifMatch(1)))
	tadmin.wantProblem(409, "invalid_transition", conv(tadmin, str(th3, "id")))
}

func TestCT026BriefUpdateVsTerminal(t *testing.T) { // BRIEF-001..005, CLAIM-005, TASK-034, TASK-072, TASK-022, TASK-084
	f := newP2Board(t)
	m := f.manager(t)
	w := f.worker(t, "w")
	tk := f.createTask(t, m, "brief", "B", "ORIGINAL-BRIEF", nil)
	id := str(tk, "id")
	c := f.claim(t, w, id, nil)
	cid, p := str(obj(c, "claim"), "id"), str(c, "proof")
	rev := num(obj(c, "task"), "revision")
	cur := f.head(t, m)
	newText := "NEW-BRIEF-" + rnd()
	m.wantProblem(428, "precondition_required", f.postBrief(m, id, newText, "scope"))
	m.wantProblem(412, "revision_conflict", f.postBrief(m, id, newText, "scope", ifMatch(rev-1)))
	b := m.must(201, f.postBrief(m, id, newText, "scope change", ifMatch(rev))).Body
	bt := obj(b, "task")
	if num(b, "revision") != 2 || b["text"] != newText || obj(b, "source")["kind"] != "brief_update" ||
		bt["state"] != "blocked" || obj(bt, "block")["cause"] != "brief_updated" || bt["current_claim"] != nil ||
		obj(bt, "last_claim")["status"] != "revoked" || num(bt, "brief_revision") != 2 {
		t.Fatalf("brief update: %v", b)
	}
	evs, _ := f.since(t, m, cur)
	wantTypes(t, "brief update", evs, "message.created", "task.brief_updated", "thread.view_invalidated")
	checkTaskEvents(t, evs)
	notice := obj(obj(evs[0], "payload"), "message")
	bu := obj(evs[1], "payload")
	if notice["author_actor_id"] != m.id || obj(notice, "labels")["abby.task.brief_revision"] != "2" || strings.Contains(str(notice, "text"), newText) {
		t.Fatalf("brief notice message (BRIEF-003): %v", notice)
	}
	if num(bu, "brief_revision") != 2 || bu["message_id"] != notice["id"] || bu["state_before"] != "claimed" || obj(bu, "revoked_claim")["id"] != cid {
		t.Fatalf("task.brief_updated payload: %v", bu)
	}
	if strings.Contains(eventsJSON(evs), newText) || strings.Contains(string(m.do("GET", f.tp(id), nil).Raw), newText) {
		t.Fatal("brief text leaked into events or task view")
	}
	wantClaimLost(t, w, "blocked", 1, f.cmd(w, id, "complete", complete("x"), proof(p)))
	bl := arr(m.must(200, m.do("GET", f.tp(id, "/briefs"), nil)).Body, "items")
	b1 := m.must(200, m.do("GET", f.tp(id, "/briefs/1"), nil)).Body
	b2 := m.must(200, m.do("GET", f.tp(id, "/briefs/2"), nil)).Body
	if len(bl) != 2 || b1["text"] != "ORIGINAL-BRIEF" || obj(b1, "source")["kind"] != "root" || obj(b2, "source")["message_id"] != notice["id"] || b2["reason"] != "scope change" {
		t.Fatalf("brief versions: %v / %v / %v", bl, b1, b2)
	}
	f.mustCmd(t, m, id, "unblock", nil)
	w.wantProblem(412, "revision_conflict", f.claimResp(w, id, J{"expect_brief_revision": 1}))
	c2 := f.claim(t, w, id, J{"expect_brief_revision": 2})
	if num(obj(c2, "claim"), "brief_revision") != 2 || num(obj(c2, "brief"), "revision") != 2 || obj(c2, "brief")["text"] != newText {
		t.Fatalf("claim of updated brief: %v", c2)
	}

	// An update blocks even an unclaimed ready task (spec §21 note 7).
	t2 := f.createTask(t, m, "brief", "B2", "x", nil)
	if bt := obj(m.must(201, f.postBrief(m, str(t2, "id"), "y", "r", ifMatch(t2["revision"]))).Body, "task"); bt["state"] != "blocked" {
		t.Fatalf("brief update on ready task: %v", bt)
	}
	// Terminal guard.
	done := f.mustCmd(t, w, id, "complete", complete("ok"), proof(str(c2, "proof")))
	wantInvalid(t, m, "completed", f.postBrief(m, id, "z", "r", ifMatch(done["revision"])))
	for _, end := range []string{"cancel", "fail"} {
		x := f.createTask(t, m, "brief", end, "x", nil)
		xid := str(x, "id")
		var v J
		if end == "cancel" {
			v = f.mustCmd(t, m, xid, "cancel", reason("no"))
		} else {
			xc := f.claim(t, w, xid, nil)
			v = f.mustCmd(t, w, xid, "fail", reason("no"), proof(str(xc, "proof")))
		}
		r := f.postBrief(m, xid, "z", "r", ifMatch(v["revision"]))
		wantInvalid(t, m, str(v, "state"), r)
		if str(r.Body, "hint") == "" {
			t.Fatalf("brief update on %s needs a reopen hint (BRIEF-004): %s", v["state"], r.Raw)
		}
	}

	// Races: brief update vs completion.
	for i := 0; i < 4; i++ {
		x := str(f.createTask(t, m, "brief-race", fmt.Sprint("c", i), "x", nil), "id")
		xc := f.claim(t, w, x, nil)
		xrev := num(obj(xc, "task"), "revision")
		var rDone, rBrief resp
		race(
			func() { rDone = f.cmd(w, x, "complete", complete("race"), proof(str(xc, "proof"))) },
			func() { rBrief = f.postBrief(m, x, "RACE", "r", ifMatch(xrev)) },
		)
		v := f.getTask(t, m, x)
		switch {
		case rDone.Status == 200 && rBrief.Status == 201:
			t.Fatalf("race %d: both brief update and completion committed", i)
		case rDone.Status == 200:
			if c := rBrief.code(); !(rBrief.Status == 409 && c == "invalid_transition") && !(rBrief.Status == 412 && c == "revision_conflict") {
				t.Fatalf("race %d: losing brief update: %d %s", i, rBrief.Status, rBrief.Raw)
			}
			if v["state"] != "completed" || num(v, "brief_revision") != 1 {
				t.Fatalf("race %d: %v", i, v)
			}
		case rBrief.Status == 201:
			wantClaimLost(t, w, "blocked", 1, rDone)
			if v["state"] != "blocked" || num(v, "brief_revision") != 2 {
				t.Fatalf("race %d: %v", i, v)
			}
		default:
			t.Fatalf("race %d: neither committed: %s / %s", i, rDone.Raw, rBrief.Raw)
		}
	}
	// Races: brief update vs cancel (cancel is valid from blocked too).
	for i := 0; i < 4; i++ {
		x := f.createTask(t, m, "brief-race", fmt.Sprint("x", i), "x", nil)
		xid := str(x, "id")
		cur := f.head(t, m)
		var rCancel, rBrief resp
		race(
			func() { rCancel = f.cmd(m, xid, "cancel", reason("race")) },
			func() { rBrief = f.postBrief(m, xid, "RACE", "r", ifMatch(x["revision"])) },
		)
		m.must(200, rCancel)
		v := f.getTask(t, m, xid)
		if v["state"] != "canceled" {
			t.Fatalf("cancel race %d: %v", i, v)
		}
		evs, _ := f.since(t, m, cur)
		tys := strings.Join(types(evs), ",")
		if rBrief.Status == 201 {
			if num(v, "brief_revision") != 2 || strings.Index(tys, "task.brief_updated") > strings.Index(tys, "task.canceled") {
				t.Fatalf("cancel race %d: brief committed but order/state wrong: %v %s", i, v, tys)
			}
		} else if c := rBrief.code(); !(rBrief.Status == 409 && c == "invalid_transition") && !(rBrief.Status == 412 && c == "revision_conflict") {
			t.Fatalf("cancel race %d: losing brief update: %d %s", i, rBrief.Status, rBrief.Raw)
		} else if num(v, "brief_revision") != 1 || strings.Contains(tys, "task.brief_updated") {
			t.Fatalf("cancel race %d: failed brief update left traces: %v %s", i, v, tys)
		}
	}
}

func TestCT027AssignmentVsClaim(t *testing.T) { // TASK-044, TASK-050..054
	f := newP2Board(t)
	m := f.manager(t)
	f.grant(t, m, "*", "allow", "claim")
	w1, w2 := f.worker(t, "w1"), f.worker(t, "w2")
	stranger := newActor(t, "str")
	f.grant(t, stranger, "elsewhere", "allow", "read")

	tk := f.createTask(t, m, "assign", "a", "x", nil)
	id := str(tk, "id")
	c1 := f.claim(t, w1, id, nil)
	p1, cid1 := str(c1, "proof"), str(obj(c1, "claim"), "id")
	r := f.putAssignee(m, id, w2.id, ifMatch(obj(c1, "task")["revision"]))
	m.wantProblem(409, "task_claim_active", r)
	if r.Body["claim_expires_at"] == nil {
		t.Fatalf("task_claim_active needs claim_expires_at: %s", r.Raw)
	}
	if v := f.getTask(t, m, id); v["assignee_actor_id"] != nil || obj(v, "current_claim")["id"] != cid1 {
		t.Fatalf("refused reassignment changed the task: %v", v)
	}
	w1.must(200, f.renew(w1, id, cid1, p1, nil))

	// Documented path: block (revokes), reassign, unblock.
	bl := f.mustCmd(t, m, id, "block", reason("reassign"))
	m.wantProblem(428, "precondition_required", f.putAssignee(m, id, w2.id))
	m.wantProblem(412, "revision_conflict", f.putAssignee(m, id, w2.id, ifMatch(num(bl, "revision")-1)))
	m.wantProblem(400, "validation_failed", f.putAssignee(m, id, stranger.id, ifMatch(bl["revision"])))
	w2.wantProblem(403, "forbidden", f.putAssignee(w2, id, w2.id, ifMatch(bl["revision"]))) // assignee-to-be is no manager
	cur := f.head(t, m)
	as := m.must(200, f.putAssignee(m, id, w2.id, ifMatch(bl["revision"]))).Body
	if as["assignee_actor_id"] != w2.id || as["state"] != "blocked" {
		t.Fatalf("reassigned: %v", as)
	}
	evs, _ := f.since(t, m, cur)
	wantTypes(t, "assign", evs, "task.assigned")
	if pl := obj(evs[0], "payload"); pl["assignee_before"] != nil || pl["assignee_after"] != w2.id {
		t.Fatalf("task.assigned payload: %v", pl)
	}
	f.mustCmd(t, m, id, "unblock", nil)
	wantClaimLost(t, w1, "ready", 1, f.cmd(w1, id, "complete", complete("x"), proof(p1)))
	for _, who := range []*actorClient{w1, m} { // m is owner and holds claim, but is not the assignee
		r := f.claimResp(who, id, nil)
		who.wantProblem(403, "forbidden", r)
		if r.Body["reason"] != "not_assignee" {
			t.Fatalf("not_assignee reason missing: %s", r.Raw)
		}
	}
	if r := m.must(200, f.claimNext(m, J{"channel": "assign"})); r.Body["outcome"] != "no_work" {
		t.Fatalf("claim-next returned a task assigned to someone else: %s", r.Raw)
	}
	w2.wantProblem(403, "forbidden", f.cmd(w2, id, "cancel", reason("assignee is not a manager")))
	c2 := f.claim(t, w2, id, nil)
	done := f.mustCmd(t, w2, id, "complete", complete("ok"), proof(str(c2, "proof")))
	wantInvalid(t, m, "completed", f.putAssignee(m, id, nil, ifMatch(done["revision"])))

	// Race: assignment vs claim; exactly one wins, no stranded claimant.
	for i := 0; i < 6; i++ {
		x := f.createTask(t, m, "assign-race", fmt.Sprint("r", i), "x", nil)
		xid := str(x, "id")
		var rc, ra resp
		race(
			func() { rc = f.claimResp(w1, xid, nil) },
			func() { ra = f.putAssignee(m, xid, w2.id, ifMatch(x["revision"])) },
		)
		v := f.getTask(t, m, xid)
		switch {
		case rc.Status == 201 && ra.Status == 200:
			t.Fatalf("race %d: both claim and reassignment committed: %v", i, v)
		case rc.Status == 201:
			if !(ra.Status == 409 && ra.code() == "task_claim_active") && !(ra.Status == 412 && ra.code() == "revision_conflict") {
				t.Fatalf("race %d: losing reassignment %d %s", i, ra.Status, ra.Raw)
			}
			if v["assignee_actor_id"] != nil || obj(v, "current_claim")["actor_id"] != w1.id {
				t.Fatalf("race %d: %v", i, v)
			}
		case ra.Status == 200:
			if rc.Status != 403 || rc.Body["reason"] != "not_assignee" {
				t.Fatalf("race %d: losing claim %d %s", i, rc.Status, rc.Raw)
			}
			if v["assignee_actor_id"] != w2.id || v["current_claim"] != nil {
				t.Fatalf("race %d: stranded claimant: %v", i, v)
			}
		default:
			t.Fatalf("race %d: neither committed: %s / %s", i, rc.Raw, ra.Raw)
		}
	}
}

func TestCT029LifecycleWalk(t *testing.T) { // TASK-020..026, TASK-030..038, TASK-040, TASK-041, TASK-080..085, CLAIM-023, CLAIM-030, CLAIM-031
	f := newP2Board(t)
	m := f.manager(t)
	w := f.worker(t, "w")
	out := newActor(t, "out")
	f.grant(t, out, "elsewhere", "allow", "read")
	ss := func(v J) string { return str(v, "state") }
	// Create the channel first so L1 emits exactly thread.created and
	// task.created (a first post into a new channel adds channel.created).
	f.channel(t, m, "walk", "inherited")
	cur := f.head(t, m)

	var tk J
	evs := f.step(t, m, &cur, "L1 create", func() { tk = f.createTask(t, m, "walk", "Walk", "walk the table", nil) }, "thread.created", "task.created")
	id := str(tk, "id")
	for _, k := range []string{"id", "board_id", "thread_id", "channel_id", "channel_path", "title", "owner_actor_id", "assignee_actor_id",
		"created_by_actor_id", "origin", "state", "revision", "brief_revision", "epoch", "retry_count", "dependencies",
		"unreadable_dependencies_pending", "current_claim", "last_claim", "block", "outcome", "root_redacted", "created_at", "updated_at", "state_changed_at"} {
		if _, ok := tk[k]; !ok {
			t.Fatalf("task view lacks %q (TASK-021): %v", k, tk)
		}
	}
	if ss(tk) != "ready" || tk["owner_actor_id"] != m.id || tk["origin"] != "new_thread" || num(tk, "epoch") != 0 || tk["channel_path"] != "walk" || tk["title"] != "Walk" {
		t.Fatalf("new task: %v", tk)
	}
	if lb := obj(obj(obj(evs[0], "payload"), "thread"), "labels"); lb["abby.task.state"] != "ready" {
		t.Fatalf("thread.created must already show abby.task.state (TASK-085): %v", lb)
	}
	if pl := obj(evs[1], "payload"); pl["origin"] != "new_thread" || pl["batch_id"] != nil {
		t.Fatalf("task.created payload: %v", pl)
	}
	g := m.must(200, m.do("GET", f.tp(id), nil))
	if g.Header.Get("ETag") != fmt.Sprintf("%q", fmt.Sprint(g.Body["revision"])) {
		t.Fatalf("ETag %q for revision %v", g.Header.Get("ETag"), g.Body["revision"])
	}
	if th := m.must(200, m.do("GET", f.p("/threads/"+str(tk, "thread_id")), nil)).Body["thread"].(J); th["task_id"] != id {
		t.Fatalf("thread task_id: %v", th)
	}

	// L3 claim, L4 renew, L5 release.
	var c J
	evs = f.step(t, m, &cur, "L3 claim", func() { c = f.claim(t, w, id, nil, header("Abby-Run-Id", "run-walk")) }, "task.claimed", "thread.view_invalidated")
	cl, p := obj(c, "claim"), str(c, "proof")
	if cl["run_id"] != "run-walk" || cl["actor_id"] != w.id || num(cl, "revision") != 1 || obj(c, "brief")["text"] != "walk the table" {
		t.Fatalf("claim (CLAIM-004): %v", c)
	}
	revBefore := num(f.getTask(t, m, id), "revision")
	f.step(t, m, &cur, "L4 renew", func() {
		rn := w.must(200, f.renew(w, id, str(cl, "id"), p, J{"lease_seconds": f.d.lim(t, "claim_lease_min_seconds")})).Body
		if num(rn, "revision") != 2 || rn["id"] != cl["id"] || rn["status"] != "active" {
			t.Fatalf("renew: %v", rn)
		}
	}, "task.claim_renewed")
	if v := f.getTask(t, m, id); num(v, "revision") != revBefore || num(v, "epoch") != 1 || ss(v) != "claimed" {
		t.Fatalf("renew changed the task (CLAIM-023): %v", v)
	}
	f.step(t, m, &cur, "L5 release", func() {
		if v := f.mustCmd(t, w, id, "release", nil, proof(p)); ss(v) != "ready" || obj(v, "last_claim")["status"] != "released" {
			t.Fatalf("release: %v", v)
		}
	}, "task.released", "thread.view_invalidated")

	// L8 fail.
	c = f.claim(t, w, id, nil)
	p = str(c, "proof")
	cur = f.head(t, m)
	w.wantProblem(400, "validation_failed", f.cmd(w, id, "fail", J{}, proof(p)))
	f.step(t, m, &cur, "L8 fail", func() {
		v := f.mustCmd(t, w, id, "fail", J{"reason": "broke", "evidence": []J{{"kind": "url", "ref": "https://example.invalid/log", "note": "log"}}}, proof(p))
		if ss(v) != "failed" || obj(v, "outcome")["kind"] != "failed" || obj(v, "outcome")["reason"] != "broke" {
			t.Fatalf("fail: %v", v)
		}
	}, "task.failed", "thread.view_invalidated")

	// Unlisted transitions commit nothing (TASK-030).
	invalidFrom := func(state string, extra bool) {
		t.Helper()
		v := f.getTask(t, m, id)
		rev := v["revision"]
		wantInvalid(t, w, state, f.claimResp(w, id, nil))
		wantInvalid(t, m, state, f.cmd(m, id, "unblock", nil))
		wantInvalid(t, m, state, f.cmd(m, id, "block", reason("x")))
		wantInvalid(t, m, state, f.postBrief(m, id, "x", "y", ifMatch(rev)))
		wantInvalid(t, m, state, f.putAssignee(m, id, w.id, ifMatch(rev)))
		wantInvalid(t, m, state, f.putDeps(m, id, nil, ifMatch(rev)))
		if extra { // completed only
			wantInvalid(t, m, state, f.cmd(m, id, "reopen", nil))
			wantInvalid(t, m, state, f.cmd(m, id, "cancel", reason("x")))
			wantInvalid(t, m, state, m.do("PATCH", f.tp(id), J{"title": "nope"}, ifMatch(rev)))
		}
		if v2 := f.getTask(t, m, id); v2["revision"] != rev || ss(v2) != state {
			t.Fatalf("rejected operations changed the task: %v", v2)
		}
		if evs, next := f.since(t, m, cur); len(evs) != 0 {
			t.Fatalf("rejected operations emitted %v", types(evs))
		} else {
			cur = next
		}
	}
	invalidFrom("failed", false)
	wantClaimLost(t, w, "failed", 2, f.cmd(w, id, "complete", complete("x"), proof(p)))

	// L13 reopen.
	evs = f.step(t, m, &cur, "L13 reopen", func() {
		v := f.mustCmd(t, m, id, "reopen", nil)
		if ss(v) != "ready" || num(v, "retry_count") != 1 || v["outcome"] != nil || num(v, "epoch") != 2 || v["current_claim"] != nil {
			t.Fatalf("reopen (TASK-036): %v", v)
		}
	}, "task.reopened", "thread.view_invalidated")
	if po := obj(obj(evs[0], "payload"), "previous_outcome"); po["kind"] != "failed" {
		t.Fatalf("task.reopened previous_outcome: %v", po)
	}

	// L16 assign, L19 title/owner.
	v := f.getTask(t, m, id)
	f.step(t, m, &cur, "L16 assign", func() { m.must(200, f.putAssignee(m, id, w.id, ifMatch(v["revision"]))) }, "task.assigned")
	v = f.getTask(t, m, id)
	f.step(t, m, &cur, "L16 unassign", func() { m.must(200, f.putAssignee(m, id, nil, ifMatch(v["revision"]))) }, "task.assigned")
	v = f.getTask(t, m, id)
	m.wantProblem(428, "precondition_required", m.do("PATCH", f.tp(id), J{"title": "Walked"}))
	m.wantProblem(412, "revision_conflict", m.do("PATCH", f.tp(id), J{"title": "Walked"}, ifMatch(num(v, "revision")-1)))
	m.wantProblem(400, "validation_failed", m.do("PATCH", f.tp(id), J{"state": "completed"}, ifMatch(v["revision"])))
	w.wantProblem(403, "forbidden", w.do("PATCH", f.tp(id), J{"title": "w"}, ifMatch(v["revision"])))
	evs = f.step(t, m, &cur, "L19 title", func() {
		if pv := m.must(200, m.do("PATCH", f.tp(id), J{"title": "Walked"}, ifMatch(v["revision"]))).Body; pv["title"] != "Walked" {
			t.Fatalf("patch title: %v", pv)
		}
	}, "thread.updated", "task.updated")
	if th := m.must(200, m.do("GET", f.p("/threads/"+str(tk, "thread_id")), nil)).Body["thread"].(J); th["title"] != "Walked" {
		t.Fatalf("thread title after task PATCH: %v", th)
	}

	// L9 block / L11 unblock.
	v = f.getTask(t, m, id)
	m.wantProblem(400, "validation_failed", f.cmd(m, id, "block", J{}))
	m.wantProblem(412, "revision_conflict", f.cmd(m, id, "block", reason("x"), ifMatch(num(v, "revision")-1)))
	w.wantProblem(403, "forbidden", f.cmd(w, id, "block", reason("not a manager")))
	evs = f.step(t, m, &cur, "L9 block", func() {
		if bv := f.mustCmd(t, m, id, "block", reason("hold"), ifMatch(v["revision"])); ss(bv) != "blocked" || obj(bv, "block")["cause"] != "manual" {
			t.Fatalf("block: %v", bv)
		}
	}, "task.blocked", "thread.view_invalidated")
	if pl := obj(evs[0], "payload"); pl["cause"] != "manual" || pl["revoked_claim"] != nil || pl["reason"] != "hold" {
		t.Fatalf("task.blocked payload: %v", pl)
	}
	wantInvalid(t, m, "blocked", f.cmd(m, id, "block", reason("again")))
	wantInvalid(t, w, "blocked", f.claimResp(w, id, nil))
	f.step(t, m, &cur, "L11 unblock", func() { f.mustCmd(t, m, id, "unblock", nil) }, "task.unblocked", "thread.view_invalidated")
	wantInvalid(t, m, "ready", f.cmd(m, id, "unblock", nil))

	// L10 claimant block and manager block of a claimed task.
	c = f.claim(t, w, id, nil)
	p = str(c, "proof")
	cur = f.head(t, m)
	evs = f.step(t, m, &cur, "L10 claimant block", func() {
		if bv := f.mustCmd(t, w, id, "block", reason("stuck"), proof(p)); ss(bv) != "blocked" || obj(bv, "last_claim")["status"] != "revoked" {
			t.Fatalf("claimant block: %v", bv)
		}
	}, "task.blocked", "thread.view_invalidated")
	if pl := obj(evs[0], "payload"); pl["cause"] != "claimant" || obj(pl, "revoked_claim")["id"] != obj(c, "claim")["id"] {
		t.Fatalf("claimant block payload: %v", pl)
	}
	wantClaimLost(t, w, "blocked", 3, f.cmd(w, id, "complete", complete("x"), proof(p)))
	f.mustCmd(t, m, id, "unblock", nil)
	c = f.claim(t, w, id, nil)
	cur = f.head(t, m)
	evs = f.step(t, m, &cur, "L10 manager block", func() { f.mustCmd(t, m, id, "block", reason("stop")) }, "task.blocked", "thread.view_invalidated")
	if pl := obj(evs[0], "payload"); pl["cause"] != "manual" || obj(pl, "revoked_claim")["id"] != obj(c, "claim")["id"] {
		t.Fatalf("manager block payload: %v", pl)
	}
	f.mustCmd(t, m, id, "unblock", nil)
	cur = f.head(t, m)

	// L12 cancel, L13 reopen, L7 complete.
	m.wantProblem(400, "validation_failed", f.cmd(m, id, "cancel", J{}))
	f.step(t, m, &cur, "L12 cancel", func() { f.mustCmd(t, m, id, "cancel", reason("not now")) }, "task.canceled", "thread.view_invalidated")
	invalidFrom("canceled", false)
	f.step(t, m, &cur, "L13 reopen canceled", func() { f.mustCmd(t, m, id, "reopen", J{"reason": "now"}) }, "task.reopened", "thread.view_invalidated")
	c = f.claim(t, w, id, nil)
	p = str(c, "proof")
	cur = f.head(t, m)
	evs = f.step(t, m, &cur, "L7 complete", func() {
		dv := f.mustCmd(t, w, id, "complete", J{"result": J{"summary": "all good", "evidence": []J{{"kind": "commit", "ref": "abc123", "note": "fix"}}, "data": J{"n": 1}}}, proof(p))
		oc := obj(dv, "outcome")
		if ss(dv) != "completed" || oc["kind"] != "completed" || oc["summary"] != "all good" || oc["claim_id"] != obj(c, "claim")["id"] ||
			oc["execution_id"] != obj(c, "claim")["execution_id"] || len(arr(oc, "evidence")) != 1 || obj(dv, "last_claim")["status"] != "completed" {
			t.Fatalf("complete (CLAIM-030, CLAIM-031): %v", dv)
		}
	}, "task.completed", "thread.view_invalidated")
	invalidFrom("completed", true)
	wantClaimLost(t, w, "completed", 5, f.cmd(w, id, "complete", complete("again"), proof(p)))

	// Waiting tasks: L1 with prerequisites, L9 from waiting, L11 back to waiting.
	other := str(f.createTask(t, m, "walk", "pre", "x", nil), "id")
	if r := f.createTask(t, m, "walk", "after done", "x", J{"depends_on": []string{id}}); ss(r) != "ready" {
		t.Fatalf("dependent of completed task must be ready (TASK-015): %v", r)
	}
	wt := f.createTask(t, m, "walk", "waits", "x", J{"depends_on": []string{other}})
	wid := str(wt, "id")
	if ss(wt) != "waiting" {
		t.Fatalf("dependent of ready task: %v", wt)
	}
	wantInvalid(t, w, "waiting", f.claimResp(w, wid, nil))
	f.mustCmd(t, m, wid, "block", reason("x"))
	if v := f.mustCmd(t, m, wid, "unblock", nil); ss(v) != "waiting" {
		t.Fatalf("unblock to waiting: %v", v)
	}
	if v := f.mustCmd(t, m, wid, "cancel", reason("x")); ss(v) != "canceled" {
		t.Fatalf("cancel waiting: %v", v)
	}

	// Visibility (TASK-040, AUTH-015): an outsider sees nothing.
	out.wantProblem(404, "not_found", out.do("GET", f.tp(id), nil))
	out.wantProblem(404, "not_found", f.cmd(out, id, "cancel", reason("x")))
	if raw := string(out.must(200, out.do("GET", f.p("/tasks"), nil)).Raw); strings.Contains(raw, id) {
		t.Fatal("outsider task list leaks a task")
	}
	oevs, _ := f.events(t, out, nil, "")
	if len(oevs) != 0 {
		t.Fatalf("outsider sees task events: %v", types(oevs))
	}
	// Claim history: newest first, no proofs.
	hist := m.must(200, m.do("GET", f.tp(id, "/claims"), nil))
	items := arr(hist.Body, "items")
	if len(items) != 5 || num(items[0].(J), "epoch") != 5 || num(items[4].(J), "epoch") != 1 || strings.Contains(string(hist.Raw), p) {
		t.Fatalf("claim history: %s", hist.Raw)
	}
}

func TestCT030ClaimNextOrderAndFilters(t *testing.T) { // SEL-001..005, SEL-007, TASK-026, TASK-052
	f := newP2Board(t)
	m := f.manager(t)
	w := f.worker(t, "w")
	x := f.worker(t, "x")
	f.grant(t, m, "secret", "allow", "read", "post", "task_manage", "channel_manage")
	f.channel(t, m, "secret", "private")
	f.grant(t, w, "noclaim", "deny", "claim")
	mk := func(ch, title string, extra J) string {
		return str(f.createTask(t, m, ch, title, "brief "+title, extra), "id")
	}

	h1 := mk("secret", "h1", nil)
	h2 := mk("noclaim", "h2", nil)
	x3 := mk("q/a", "x3", J{"assignee_actor_id": x.id})
	b4 := mk("q/a", "b4", nil)
	f.mustCmd(t, m, b4, "block", reason("hold"))
	a5 := mk("q/b", "a5", J{"labels": J{"prio": "high"}})
	a6 := mk("q/a", "a6", J{"assignee_actor_id": w.id})
	a7 := mk("q/a", "a7", J{"labels": J{"prio": "low"}})

	// Task list filters (TASK-026).
	list := func(q string) []string {
		var ids []string
		for _, it := range arr(m.must(200, m.do("GET", f.p("/tasks?"+q), nil)).Body, "items") {
			ids = append(ids, str(it.(J), "id"))
		}
		return ids
	}
	for q, want := range map[string][]string{
		"channel=q/a":                    {x3, b4, a6, a7},
		"subtree=q&state=blocked":        {b4},
		"assignee=" + x.id:               {x3},
		"subtree=q&assignee=none":        {b4, a5, a7},
		"label=prio:low":                 {a7},
		"label=prio&state=ready":         {a5, a7},
		"owner=" + m.id + "&channel=q/b": {a5},
	} {
		if got := list(q); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("GET /tasks?%s = %v, want %v (creation order)", q, got, want)
		}
	}
	if raw := string(w.must(200, w.do("GET", f.p("/tasks"), nil)).Raw); strings.Contains(raw, h1) {
		t.Fatal("task list shows an unreadable task")
	}

	next := func(by *actorClient, body J) string {
		t.Helper()
		r := f.claimNext(by, body)
		if r.Status == 200 && r.Body["outcome"] == "no_work" {
			if r.Body["timed_out"] != false {
				t.Fatalf("no_work without wait must have timed_out false: %s", r.Raw)
			}
			return ""
		}
		if r.Status != 201 || r.Body["outcome"] != "claimed" || str(r.Body, "proof") == "" {
			t.Fatalf("claim-next %v: %d %s", body, r.Status, r.Raw)
		}
		tk := obj(r.Body, "task")
		if obj(r.Body, "brief")["text"] != "brief "+str(tk, "title") || tk["state"] != "claimed" {
			t.Fatalf("claim-next response: %s", r.Raw)
		}
		return str(obj(r.Body, "claim"), "task_id")
	}
	w.wantProblem(400, "validation_failed", f.claimNext(w, J{"assignee": "anyone"}))
	w.wantProblem(400, "validation_failed", f.claimNext(w, J{"bogus": true}))
	if got := next(w, J{"channel": "q/a"}); got != a6 {
		t.Fatalf("channel filter: got %s, want a6 %s (x3 assigned elsewhere, b4 blocked)", got, a6)
	}
	if got := next(w, J{"subtree": "q", "labels": J{"prio": []string{"low"}}}); got != a7 {
		t.Fatalf("subtree+label filter: got %s, want a7", got)
	}
	if got := next(w, J{"labels": J{"prio": []string{}}}); got != a5 {
		t.Fatalf("label presence filter: got %s, want a5", got)
	}
	if got := next(w, nil); got != "" {
		t.Fatalf("no eligible work left, got %s (h1 unreadable, h2 no claim, x3 assigned to x)", got)
	}
	if got := next(x, J{"subtree": "q"}); got != x3 {
		t.Fatalf("assignee claims its task: got %s", got)
	}
	// Oldest first across channels, and assignee filters.
	u8 := mk("r", "u8", nil)
	m9 := mk("r", "m9", J{"assignee_actor_id": w.id})
	u10 := mk("r", "u10", nil)
	if got := next(w, J{"assignee": "me"}); got != m9 {
		t.Fatalf("assignee=me: got %s, want m9", got)
	}
	if got := next(w, J{"assignee": "me"}); got != "" {
		t.Fatalf("assignee=me with nothing assigned: %s", got)
	}
	if got := next(w, J{"assignee": "unassigned"}); got != u8 {
		t.Fatalf("assignee=unassigned oldest: got %s, want u8", got)
	}
	if got := next(w, nil); got != u10 {
		t.Fatalf("default eligible: got %s, want u10", got)
	}
	// Released work goes back in its original creation position.
	cn := f.claim(t, w, mk("s", "late", nil), nil)
	early := mk("s", "early-but-newer", nil)
	f.mustCmd(t, w, str(obj(cn, "claim"), "task_id"), "release", nil, proof(str(cn, "proof")))
	if got := next(w, J{"channel": "s"}); got != str(obj(cn, "claim"), "task_id") {
		t.Fatalf("oldest by creation position after release: got %s (newer %s)", got, early)
	}
	_ = h2
}

func TestCT031ClaimNextWait(t *testing.T) { // SEL-006, SEL-007
	f := newP2Board(t)
	m := f.manager(t)
	w := f.worker(t, "w")
	maxWait := f.d.lim(t, "max_claim_wait_ms")
	w.wantProblem(400, "validation_failed", f.claimNext(w, J{"channel": "wait", "wait_ms": maxWait + 1}))

	start := time.Now()
	r := w.must(200, f.claimNext(w, J{"channel": "wait", "wait_ms": 1000}))
	if r.Body["outcome"] != "no_work" || r.Body["timed_out"] != true || time.Since(start) < 900*time.Millisecond {
		t.Fatalf("timeout: %s after %v", r.Raw, time.Since(start))
	}

	// A task created during the wait is claimed.
	res := make(chan resp, 1)
	go func() { res <- f.claimNext(w, J{"channel": "wait", "wait_ms": 20000}) }()
	time.Sleep(300 * time.Millisecond)
	id := str(f.createTask(t, m, "wait", "new", "x", nil), "id")
	select {
	case r := <-res:
		if r.Status != 201 || obj(r.Body, "claim")["task_id"] != id {
			t.Fatalf("wait result: %d %s", r.Status, r.Raw)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("claim-next wait did not pick up the new task")
	}

	// A task that becomes ready (unblock) during the wait is claimed.
	b := str(f.createTask(t, m, "wait2", "blocked", "x", nil), "id")
	f.mustCmd(t, m, b, "block", reason("later"))
	go func() { res <- f.claimNext(w, J{"channel": "wait2", "wait_ms": 20000}) }()
	time.Sleep(300 * time.Millisecond)
	f.mustCmd(t, m, b, "unblock", nil)
	select {
	case r := <-res:
		if r.Status != 201 || obj(r.Body, "claim")["task_id"] != b {
			t.Fatalf("wait result after unblock: %d %s", r.Status, r.Raw)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("claim-next wait did not pick up the unblocked task")
	}
}

func TestCT032LapsedLeaseRead(t *testing.T) { // CLAIM-050, CLAIM-051, CLAIM-053, TASK-023, TASK-025, TASK-027, SEL-002
	f := newP2Board(t)
	mn := f.d.lim(t, "claim_lease_min_seconds")
	m := f.manager(t)
	w, v := f.worker(t, "w"), f.worker(t, "v")
	id := str(f.createTask(t, m, "lapse", "one", "x", nil), "id")
	id2 := str(f.createTask(t, m, "lapse2", "two", "x", nil), "id")
	c := f.claim(t, w, id, J{"lease_seconds": mn})
	c2 := f.claim(t, w, id2, J{"lease_seconds": mn})
	cl := obj(c, "claim")
	rev := num(obj(c, "task"), "revision")
	tv := f.waitLapsed(t, v, id, str(cl, "id"), mn)
	lc := obj(tv, "last_claim")
	if tv["state"] != "ready" || tv["current_claim"] != nil || lc["status"] != "expired" || num(tv, "revision") != rev+1 ||
		!sameInstant(t, lc["ended_at"], cl["expires_at"]) || !sameInstant(t, tv["state_changed_at"], cl["expires_at"]) {
		t.Fatalf("effective view: %v", tv)
	}
	// Every read surface agrees (TASK-025, TASK-027).
	th := str(tv, "thread_id")
	if raw := string(v.must(200, v.do("GET", f.p("/tasks?channel=lapse&state=ready"), nil)).Raw); !strings.Contains(raw, id) {
		t.Fatal("state=ready list misses the lapsed task")
	}
	if raw := string(v.must(200, v.do("GET", f.p("/tasks?channel=lapse&state=claimed"), nil)).Raw); strings.Contains(raw, id) {
		t.Fatal("state=claimed list shows the lapsed task")
	}
	if raw := string(v.must(200, v.do("GET", f.p("/threads?label=abby.task.state:ready&channel=lapse"), nil)).Raw); !strings.Contains(raw, th) {
		t.Fatal("abby.task.state:ready thread filter misses the lapsed task")
	}
	tr := v.must(200, v.do("GET", f.p("/threads/"+th), nil)).Body
	if obj(obj(tr, "root_message"), "labels")["abby.task.state"] != "ready" {
		t.Fatalf("root label after lapse: %v", tr)
	}
	// Another actor claims at once, by ID and by claim-next.
	if nc := obj(f.claim(t, v, id, nil), "claim"); num(nc, "epoch") != 2 {
		t.Fatalf("claim after lapse: %v", nc)
	}
	f.waitLapsed(t, v, id2, str(obj(c2, "claim"), "id"), mn)
	if r := v.must(201, f.claimNext(v, J{"channel": "lapse2"})); obj(r.Body, "claim")["task_id"] != id2 {
		t.Fatalf("claim-next must select a lapsed lease: %s", r.Raw)
	}
}

func TestCT033HiddenPrerequisite(t *testing.T) { // DEP-001, DEP-002, DEP-003, DEP-005, DEP-009, DEP-010, AUTH-015
	f := newP2Board(t)
	m := newActor(t, "mgr")
	f.grant(t, m, "*", "allow", "read", "post", "task_manage", "channel_manage", "claim")
	f.grant(t, m, "ops", "allow", "read", "post", "task_manage", "channel_manage", "claim")
	f.channel(t, m, "ops", "private")
	r := newActor(t, "reader")
	f.grant(t, r, "server", "allow", "read", "post", "task_manage", "task_admin")
	mk := func(ch, title string, deps ...string) string {
		ex := J{}
		if len(deps) > 0 {
			ex["depends_on"] = deps
		}
		return str(f.createTask(t, m, ch, title, "x", ex), "id")
	}
	done := func(id string) {
		c := f.claim(t, m, id, nil)
		f.mustCmd(t, m, id, "complete", complete("ok"), proof(str(c, "proof")))
	}
	h := mk("ops", "hidden")
	b := mk("server", "visible")
	a := mk("server", "dependent", b, h)
	rcur := f.head(t, r)

	view := func() J {
		g := r.must(200, r.do("GET", f.tp(a), nil))
		if strings.Contains(string(g.Raw), h) {
			t.Fatalf("reader's view names the hidden prerequisite: %s", g.Raw)
		}
		return g.Body
	}
	if v := view(); v["state"] != "waiting" || len(arr(v, "dependencies")) != 1 || v["unreadable_dependencies_pending"] != false {
		t.Fatalf("hidden prerequisite, visible one pending: %v", v)
	}
	done(b)
	if v := view(); v["state"] != "waiting" || len(arr(v, "dependencies")) != 1 || arr(v, "dependencies")[0].(J)["state"] != "completed" || v["unreadable_dependencies_pending"] != true {
		t.Fatalf("visible prerequisite done, hidden pending: %v", v)
	}
	done(h)
	if v := view(); v["state"] != "ready" || v["unreadable_dependencies_pending"] != false {
		t.Fatalf("readiness follows hidden completion: %v", v)
	}
	evs, _ := f.since(t, r, rcur)
	if raw := eventsJSON(evs); strings.Contains(raw, h) || strings.Contains(raw, `"hidden"`) {
		t.Fatalf("reader's events reveal the hidden task: %s", raw)
	}
	var becameReady J
	for _, e := range evs {
		if e["type"] == "task.became_ready" {
			becameReady = obj(e, "payload")
		}
	}
	if becameReady == nil || obj(becameReady, "task")["id"] != a {
		t.Fatalf("reader must see task.became_ready on the dependent: %v", types(evs))
	}
	if _, ok := becameReady["prerequisite_task_id"]; ok {
		t.Fatalf("unreadable prerequisite_task_id must be omitted: %v", becameReady)
	}
	r.wantProblem(404, "not_found", r.do("GET", f.tp(h), nil))
	if raw := string(r.must(200, r.do("GET", f.p("/tasks"), nil)).Raw); strings.Contains(raw, h) {
		t.Fatal("task list shows the hidden task")
	}

	// Adding an unreadable prerequisite is not_found; nothing created.
	h2 := mk("ops", "hidden2")
	before := len(arr(r.must(200, r.do("GET", f.p("/tasks"), nil)).Body, "items"))
	r.wantProblem(404, "not_found", f.createTaskResp(r, "server", "sneak", "x", J{"depends_on": []string{h2}}))
	if after := len(arr(r.must(200, r.do("GET", f.p("/tasks"), nil)).Body, "items")); after != before {
		t.Fatal("rejected create left a task")
	}
	// PUT replaces only the readable part (DEP-005).
	b2 := mk("server", "visible2")
	a2 := mk("server", "dependent2", b2, h2)
	av := f.getTask(t, r, a2)
	r.wantProblem(404, "not_found", f.putDeps(r, a2, []string{b2, h}, ifMatch(av["revision"])))
	pv := r.must(200, f.putDeps(r, a2, nil, ifMatch(av["revision"]))).Body
	if len(arr(pv, "dependencies")) != 0 || pv["state"] != "waiting" || pv["unreadable_dependencies_pending"] != true {
		t.Fatalf("reader view after clearing readable deps: %v", pv)
	}
	mv := f.getTask(t, m, a2)
	if d := arr(mv, "dependencies"); len(d) != 1 || d[0].(J)["task_id"] != h2 {
		t.Fatalf("hidden prerequisite must be kept: %v", mv)
	}
	// A cycle through a hidden task is refused; only readable IDs listed.
	x := mk("server", "x")
	h3 := mk("ops", "h3", x)
	y := mk("server", "y", h3)
	xv := f.getTask(t, r, x)
	cy := f.putDeps(r, x, []string{y}, ifMatch(xv["revision"]))
	r.wantProblem(409, "dependency_cycle", cy)
	if strings.Contains(string(cy.Raw), h3) {
		t.Fatalf("dependency_cycle lists an unreadable task: %s", cy.Raw)
	}
}

func TestCT034RootProtection(t *testing.T) { // BRIEF-010, BRIEF-011, BRIEF-020, BRIEF-021, BRIEF-022, RED-001
	f := newP2Board(t)
	m := f.manager(t)
	w := f.worker(t, "w", "post")
	part := newActor(t, "part")
	f.grant(t, part, "*", "allow", "read", "post")
	f.grant(t, f.owner, "*", "allow", "read")
	rootText := "ROOT-SECRET-" + rnd()
	tk := f.createTask(t, m, "prot", "Protected", rootText, nil)
	id, th := str(tk, "id"), str(tk, "thread_id")
	tr := m.must(200, m.do("GET", f.p("/threads/"+th), nil)).Body
	rid := str(obj(tr, "root_message"), "id")
	thRev := obj(tr, "thread")["revision"]

	cur := f.head(t, m)
	for _, r := range []resp{
		m.do("PATCH", f.p("/messages/"+rid), J{"text": "changed"}, ifMatch(1)),
		m.do("DELETE", f.p("/messages/"+rid), nil, ifMatch(1)),
		m.do("PATCH", f.p("/threads/"+th), J{"title": "changed"}, ifMatch(thRev)),
		f.owner.do("DELETE", f.p("/messages/"+rid), J{"reason": "admin"}, ifMatch(1)),
	} {
		m.wantProblem(409, "task_root_protected", r)
		if r.Body["task_id"] != id {
			t.Fatalf("task_root_protected without task_id: %s", r.Raw)
		}
	}
	if evs, _ := f.since(t, m, cur); len(evs) != 0 {
		t.Fatalf("protected root operations committed %v", types(evs))
	}
	// Replies and non-abby root labels are unaffected; reply authorship
	// does not touch the claim (BRIEF-011).
	c := f.claim(t, w, id, nil)
	rep := f.reply(t, w, th, "progress note")
	w.must(200, w.do("DELETE", f.p("/messages/"+str(rep, "id")), nil, ifMatch(1)))
	m.must(200, m.do("PATCH", f.p("/messages/"+rid+"/labels"), J{"operations": []J{{"op": "set", "key": "prio", "value": "high"}}}))
	if v := f.getTask(t, m, id); obj(v, "current_claim")["id"] != obj(c, "claim")["id"] || num(v, "brief_revision") != 1 {
		t.Fatalf("message activity changed the claim or brief: %v", v)
	}

	// Brief revision 2 whose text must also be scrubbed.
	v := f.getTask(t, m, id)
	b2 := "BRIEF2-SECRET-" + rnd()
	m.must(201, f.postBrief(m, id, b2, "more", ifMatch(v["revision"])))
	f.mustCmd(t, m, id, "unblock", nil)
	kc := idem("claim-" + rnd())
	c2 := w.must(201, f.claimResp(w, id, nil, kc)).Body
	if obj(c2, "brief")["text"] != b2 {
		t.Fatalf("claim response brief: %v", c2["brief"])
	}
	// Root redaction requires a manager with root delete rights and If-Match.
	rootRev := m.must(200, m.do("GET", f.p("/messages/"+rid), nil)).Body["revision"]
	m.wantProblem(428, "precondition_required", m.do("POST", f.tp(id, "/root-redaction"), reason("pii"), newKey()))
	m.wantProblem(412, "revision_conflict", m.do("POST", f.tp(id, "/root-redaction"), reason("pii"), newKey(), ifMatch(rootRev.(float64)+1)))
	part.wantProblem(403, "forbidden", part.do("POST", f.tp(id, "/root-redaction"), reason("pii"), newKey(), ifMatch(rootRev)))
	cur = f.head(t, m)
	rv := m.must(200, m.do("POST", f.tp(id, "/root-redaction"), reason("pii"), newKey(), ifMatch(rootRev))).Body
	if rv["state"] != "canceled" || rv["root_redacted"] != true || obj(rv, "last_claim")["status"] != "revoked" {
		t.Fatalf("root redaction of active task: %v", rv)
	}
	evs, _ := f.since(t, m, cur)
	wantTypes(t, "root redaction", evs, "message.deleted", "task.canceled", "thread.view_invalidated")
	if pl := obj(evs[1], "payload"); pl["cause"] != "root_redacted" || obj(pl, "revoked_claim")["id"] != obj(c2, "claim")["id"] {
		t.Fatalf("task.canceled payload: %v", pl)
	}
	wantClaimLost(t, w, "canceled", 2, f.cmd(w, id, "complete", complete("x"), proof(str(c2, "proof"))))
	for _, it := range arr(m.must(200, m.do("GET", f.tp(id, "/briefs"), nil)).Body, "items") {
		if it.(J)["text"] != nil || it.(J)["redacted"] != true {
			t.Fatalf("brief not scrubbed: %v", it)
		}
	}
	scrubbed := func(where, raw string) {
		t.Helper()
		if strings.Contains(raw, rootText) || strings.Contains(raw, b2) {
			t.Fatalf("%s still holds redacted brief text", where)
		}
	}
	allEvs, _ := f.events(t, m, nil, "")
	scrubbed("event history", eventsJSON(allEvs))
	_, snapRaw, _ := f.snapshot(t, m, "")
	scrubbed("snapshot", snapRaw)
	scrubbed("root message", string(m.must(200, m.do("GET", f.p("/messages/"+rid), nil)).Raw))
	scrubbed("brief 1", string(m.do("GET", f.tp(id, "/briefs/1"), nil).Raw))
	scrubbed("claim replay", string(w.must(201, f.claimResp(w, id, nil, kc)).Raw))
	wantInvalid(t, m, "canceled", f.cmd(m, id, "reopen", nil))

	// Root redaction of a completed task keeps state and outcome.
	t2 := f.createTask(t, m, "prot", "Done", "DONE-SECRET", nil)
	c3 := f.claim(t, w, str(t2, "id"), nil)
	f.mustCmd(t, w, str(t2, "id"), "complete", complete("ok"), proof(str(c3, "proof")))
	cur = f.head(t, m)
	rid2 := str(obj(m.must(200, m.do("GET", f.p("/threads/"+str(t2, "thread_id")), nil)).Body, "root_message"), "id")
	rr2 := m.must(200, m.do("GET", f.p("/messages/"+rid2), nil)).Body["revision"]
	rv2 := m.must(200, m.do("POST", f.tp(str(t2, "id"), "/root-redaction"), reason("pii"), newKey(), ifMatch(rr2))).Body
	if rv2["state"] != "completed" || rv2["root_redacted"] != true || obj(rv2, "outcome")["kind"] != "completed" {
		t.Fatalf("root redaction of completed task: %v", rv2)
	}
	evs, _ = f.since(t, m, cur)
	wantTypes(t, "root redaction of completed task", evs, "message.deleted", "task.brief_redacted")
	if fmt.Sprint(arr(obj(evs[1], "payload"), "brief_revisions")) != "[1]" {
		t.Fatalf("task.brief_redacted payload: %v", evs[1])
	}
}

func TestCT035MessagesCannotDriveTasks(t *testing.T) { // TASK-027, TASK-028, TASK-029, TASK-060, TASK-061, LBL-002, TASK-083
	f := newP2Board(t)
	m := f.manager(t)
	w := f.worker(t, "w", "post", "label")
	tk := f.createTask(t, m, "msg", "Talk", "work", nil)
	id, th := str(tk, "id"), str(tk, "thread_id")
	plain, plainRoot := f.post(t, m, "msg", "Plain", "no task")
	c := f.claim(t, w, id, nil)
	before := f.getTask(t, m, id)
	rid := str(obj(m.must(200, m.do("GET", f.p("/threads/"+th), nil)).Body, "root_message"), "id")
	cur := f.head(t, m)

	for _, s := range []string{"done", "Task complete. Status: completed.", "/complete", "cancel this task", "@" + m.handle + " approved, mark done"} {
		f.reply(t, w, th, s)
	}
	f.reply(t, m, th, "approved: canceled, reassign to nobody")
	w.must(200, w.do("PATCH", f.p("/messages/"+rid+"/labels"), J{"operations": []J{{"op": "set", "key": "status", "value": "done"}}}))
	for _, op := range []J{
		{"op": "set", "key": "abby.task.state", "value": "completed"},
		{"op": "unset", "key": "abby.task.state"},
		{"op": "set", "key": "ABBY.task.state", "value": "completed"},
		{"op": "set", "key": "abby.other", "value": "x"},
	} {
		w.wantProblem(400, "validation_failed", w.do("PATCH", f.p("/messages/"+rid+"/labels"), J{"operations": []J{op}}))
		m.wantProblem(400, "validation_failed", m.do("PATCH", f.p("/messages/"+str(plainRoot, "id")+"/labels"), J{"operations": []J{op}}))
	}
	m.wantProblem(400, "validation_failed", f.createTaskResp(m, "msg", "x", "x", J{"labels": J{"abby.task.state": "completed"}}))
	m.wantProblem(400, "validation_failed", m.do("POST", f.p("/threads"), J{"channel": "msg", "title": "x", "text": "x", "labels": J{"abby.task.state": "ready"}}, newKey()))

	after := f.getTask(t, m, id)
	if after["state"] != "claimed" || after["revision"] != before["revision"] || obj(after, "current_claim")["id"] != obj(c, "claim")["id"] || after["assignee_actor_id"] != nil {
		t.Fatalf("messages changed the task: %v", after)
	}
	evs, _ := f.since(t, m, cur)
	for _, e := range evs {
		if strings.HasPrefix(str(e, "type"), "task.") {
			t.Fatalf("messages emitted a task event: %v", types(evs))
		}
	}

	// abby.task.state is a root label filter that selects task threads.
	if lb := obj(obj(m.must(200, m.do("GET", f.p("/threads/"+th), nil)).Body, "root_message"), "labels"); lb["abby.task.state"] != "claimed" || lb["status"] != "done" {
		t.Fatalf("root labels: %v", lb)
	}
	if lb := obj(plainRoot, "labels"); lb["abby.task.state"] != nil {
		t.Fatalf("plain thread has abby.task.state: %v", lb)
	}
	sel := string(m.must(200, m.do("GET", f.p("/threads?label=abby.task.state"), nil)).Raw)
	if !strings.Contains(sel, th) || strings.Contains(sel, str(plain, "id")) {
		t.Fatalf("label=abby.task.state must select exactly task threads: %s", sel)
	}
	if raw := string(m.must(200, m.do("GET", f.p("/threads?label=abby.task.state:ready"), nil)).Raw); strings.Contains(raw, th) {
		t.Fatal("abby.task.state:ready selects a claimed task")
	}
	// Event label filter sees the task enter and leave the view (TASK-083).
	q := url.Values{"label": {"abby.task.state:ready"}}
	_, lcur := f.events(t, m, q, "")
	f.mustCmd(t, w, id, "release", nil, proof(str(c, "proof")))
	f.claim(t, w, id, nil)
	levs, _ := f.events(t, m, q, lcur)
	wantTypes(t, "abby.task.state:ready view", levs, "task.released", "thread.view_invalidated", "task.claimed", "thread.view_invalidated")
	// Text and author clauses do not exclude task events (TASK-083).
	tq := url.Values{"text": {"zzz-" + rnd()}, "thread_id": {th}}
	_, tcur := f.events(t, m, tq, "")
	f.mustCmd(t, m, id, "block", reason("x"))
	tevs, _ := f.events(t, m, tq, tcur)
	if countType(tevs, "task.blocked") != 1 {
		t.Fatalf("text clause excluded a task event: %v", types(tevs))
	}
	tyevs, _ := f.events(t, m, url.Values{"type": {"task.blocked"}}, "")
	if len(tyevs) != 1 {
		t.Fatalf("type=task.blocked filter (TASK-091): %v", types(tyevs))
	}
}

func TestCT036TaskSnapshotThenReplay(t *testing.T) { // TASK-090, TASK-091, TASK-092, TASK-093, TASK-022
	f := newP2Board(t)
	mn := f.d.lim(t, "claim_lease_min_seconds")
	m := f.manager(t)
	w := f.worker(t, "w")
	t1 := str(f.createTask(t, m, "snap", "t1", "x", nil), "id")
	t2 := str(f.createTask(t, m, "snap", "t2", "x", nil), "id")
	t3 := str(f.createTask(t, m, "snap", "t3", "x", nil), "id")
	f.post(t, m, "snap", "plain", "no task")
	f.mustCmd(t, m, t3, "block", reason("x"))
	c1 := f.claim(t, w, t1, J{"lease_seconds": mn})

	local := map[string]J{}
	items, raw, cont := f.snapshot(t, m, "")
	if strings.Contains(raw, str(c1, "proof")) {
		t.Fatal("snapshot contains a claim proof")
	}
	for i, it := range items {
		if it["type"] == "task" {
			if i == 0 || items[i-1]["type"] != "thread" || obj(items[i-1], "data")["id"] != obj(it, "data")["thread_id"] {
				t.Fatalf("task item %d must follow its thread item (TASK-092)", i)
			}
			local[str(obj(it, "data"), "id")] = obj(it, "data")
		}
		if it["type"] == "thread" && obj(it, "data")["task_id"] == nil {
			if i+1 < len(items) && items[i+1]["type"] == "task" {
				t.Fatal("task item after a thread without task")
			}
		}
	}
	if len(local) != 3 || local[t1]["state"] != "claimed" {
		t.Fatalf("snapshot task items: %v", local)
	}

	f.waitLapsed(t, m, t1, str(obj(c1, "claim"), "id"), mn)
	// Effective state at capture time drives the label filter (TASK-093).
	items2, _, cont2 := f.snapshot(t, m, "?label=abby.task.state:ready")
	got := map[string]J{}
	for _, it := range items2 {
		if it["type"] == "task" {
			got[str(obj(it, "data"), "id")] = obj(it, "data")
		}
	}
	if got[t1] == nil || got[t2] == nil || got[t3] != nil || got[t1]["state"] != "ready" {
		t.Fatalf("label snapshot after lapse: %v", got)
	}
	// Changes in the gap, including materialization of the lapse.
	f.mustCmd(t, m, t1, "block", reason("after lapse"))
	c2 := f.claim(t, w, t2, nil)
	f.mustCmd(t, w, t2, "complete", complete("ok"), proof(str(c2, "proof")))
	f.mustCmd(t, m, t3, "unblock", nil)

	evs, _ := f.events(t, m, nil, cont)
	for _, e := range evs {
		tk := obj(obj(e, "payload"), "task")
		if tk == nil {
			continue
		}
		if l := local[str(tk, "id")]; l == nil || num(tk, "revision") > num(l, "revision") {
			local[str(tk, "id")] = tk
		}
	}
	for _, id := range []string{t1, t2, t3} {
		live := f.getTask(t, m, id)
		if local[id]["state"] != live["state"] || local[id]["revision"] != live["revision"] {
			t.Fatalf("snapshot+replay %s: local %v/%v, live %v/%v", id, local[id]["state"], local[id]["revision"], live["state"], live["revision"])
		}
	}
	// A claim_expired after the label snapshot carries the revision it showed.
	evs2, _ := f.events(t, m, url.Values{"label": {"abby.task.state:ready"}}, cont2)
	for _, e := range evs2 {
		if e["type"] == "task.claim_expired" && obj(e, "resource")["id"] == t1 && obj(e, "resource")["revision"] != got[t1]["revision"] {
			t.Fatalf("claim_expired revision %v differs from snapshot %v", obj(e, "resource")["revision"], got[t1]["revision"])
		}
	}
}

func TestCT037DependencyEdits(t *testing.T) { // DEP-001, DEP-002, DEP-005, DEP-006, TASK-072, L15
	f := newP2Board(t)
	m := f.manager(t)
	w := f.worker(t, "w")
	mk := func(title string, deps ...string) J {
		ex := J{}
		if len(deps) > 0 {
			ex["depends_on"] = deps
		}
		return f.createTask(t, m, "deps", title, "x", ex)
	}
	a, b, c := mk("a"), mk("b"), mk("c")
	aid, bid, cid := str(a, "id"), str(b, "id"), str(c, "id")
	put := func(id string, deps ...string) resp {
		return f.putDeps(m, id, deps, ifMatch(f.getTask(t, m, id)["revision"]))
	}
	m.wantProblem(428, "precondition_required", f.putDeps(m, aid, []string{bid}))
	m.wantProblem(412, "revision_conflict", f.putDeps(m, aid, []string{bid}, ifMatch(num(a, "revision")-1)))
	w.wantProblem(403, "forbidden", f.putDeps(w, aid, []string{bid}, ifMatch(a["revision"])))
	cur := f.head(t, m)
	evs := f.step(t, m, &cur, "add prerequisite", func() {
		if v := m.must(200, put(aid, bid)).Body; v["state"] != "waiting" {
			t.Fatalf("a after deps: %v", v)
		}
	}, "task.dependencies_changed", "thread.view_invalidated")
	if pl := obj(evs[0], "payload"); fmt.Sprint(pl["added"]) != fmt.Sprint([]any{bid}) || pl["state_before"] != "ready" || pl["state_after"] != "waiting" {
		t.Fatalf("task.dependencies_changed payload: %v", pl)
	}

	cycle := func(name string, r resp) {
		t.Helper()
		m.wantProblem(409, "dependency_cycle", r)
	}
	revB := f.getTask(t, m, bid)["revision"]
	cycle("two-cycle", put(bid, aid))
	cycle("self", put(aid, aid))
	m.must(200, put(cid, aid)) // c -> a -> b
	cycle("three-cycle", put(bid, cid))
	if v := f.getTask(t, m, bid); v["revision"] != revB || len(arr(v, "dependencies")) != 0 {
		t.Fatalf("refused cycle changed b: %v", v)
	}
	m.wantProblem(404, "not_found", put(aid, bid, "task_missing_"+rnd()))
	f2 := &tf{fixture: newBoard(t), d: f.d}
	f2.grant(t, m, "*", "allow", "read", "post", "task_manage", "channel_manage")
	xb := str(f2.createTask(t, m, "x", "x", "x", nil), "id")
	m.wantProblem(404, "not_found", put(aid, xb))
	m.wantProblem(404, "not_found", f.createTaskResp(m, "deps", "cross", "x", J{"depends_on": []string{xb}}))

	// Blocked stays blocked; no invalidation without a state change.
	e := mk("e")
	eid := str(e, "id")
	f.mustCmd(t, m, eid, "block", reason("x"))
	cur = f.head(t, m)
	evs = f.step(t, m, &cur, "deps on blocked", func() { m.must(200, put(eid, bid)) }, "task.dependencies_changed")
	if pl := obj(evs[0], "payload"); pl["state_before"] != "blocked" || pl["state_after"] != "blocked" {
		t.Fatalf("blocked deps payload: %v", pl)
	}
	f.step(t, m, &cur, "clear deps", func() {
		if v := m.must(200, put(aid)).Body; v["state"] != "ready" {
			t.Fatalf("a after clearing deps: %v", v)
		}
	}, "task.dependencies_changed", "thread.view_invalidated")

	// During a claim, and on terminal tasks.
	d := str(mk("d"), "id")
	cl := f.claim(t, w, d, nil)
	r := put(d, bid)
	m.wantProblem(409, "task_claim_active", r)
	if r.Body["claim_expires_at"] == nil {
		t.Fatalf("task_claim_active needs claim_expires_at: %s", r.Raw)
	}
	f.mustCmd(t, w, d, "complete", complete("ok"), proof(str(cl, "proof")))
	wantInvalid(t, m, "completed", put(d, bid))
	x := str(mk("x"), "id")
	f.mustCmd(t, m, x, "cancel", reason("x"))
	wantInvalid(t, m, "canceled", put(x, bid))

	// Limit on prerequisites in total.
	maxDeps := f.d.lim(t, "max_dependencies_per_task")
	var many []string
	for len(many) <= maxDeps {
		n := maxDeps + 1 - len(many)
		if mb := f.d.lim(t, "max_batch_tasks"); n > mb {
			n = mb
		}
		var items []J
		for i := 0; i < n; i++ {
			items = append(items, J{"ref": fmt.Sprint("p", i), "thread": J{"channel": "many", "title": "p", "text": "x"}})
		}
		for _, it := range arr(m.must(201, m.do("POST", f.p("/tasks/batch"), J{"tasks": items}, newKey())).Body, "tasks") {
			many = append(many, str(obj(it.(J), "task"), "id"))
		}
	}
	m.wantProblem(413, "limit_exceeded", put(eid, many...))
}

func TestCT038ProofHygiene(t *testing.T) { // CLAIM-010, CLAIM-011, CLAIM-012, CLAIM-013, CLAIM-014, TASK-022, TASK-042, TASK-043, TASK-084
	f := newP2Board(t)
	m := f.manager(t)
	w, v := f.worker(t, "w"), f.worker(t, "v")
	id := str(f.createTask(t, m, "proof", "p", "x", nil), "id")
	id2 := str(f.createTask(t, m, "proof", "p2", "x", nil), "id")
	c := f.claim(t, w, id, nil)
	p, cid := str(c, "proof"), str(obj(c, "claim"), "id")
	c2 := f.claim(t, w, id2, nil)
	if len(p) < 16 {
		t.Fatalf("proof too short to carry 128 bits: %q", p)
	}

	// Missing header: 428 on claimant routes.
	for _, r := range []resp{
		f.cmd(w, id, "release", nil),
		f.cmd(w, id, "complete", complete("x")),
		f.cmd(w, id, "fail", reason("x")),
		f.renew(w, id, cid, "", nil),
	} {
		w.wantProblem(428, "precondition_required", r)
	}
	// Proof in body or query is not accepted.
	if r := f.cmd(w, id, "complete", J{"result": J{}, "proof": p}); r.Status < 400 {
		t.Fatalf("proof accepted in body: %s", r.Raw)
	}
	if r := w.do("POST", f.tp(id, "/complete?proof="+url.QueryEscape(p)), complete("x"), newKey()); r.Status < 400 {
		t.Fatalf("proof accepted in query: %s", r.Raw)
	}
	// Claimant block without the header is a manager block: refused.
	w.wantProblem(403, "forbidden", f.cmd(w, id, "block", reason("x")))
	w.wantProblem(400, "validation_failed", f.cmd(w, id, "complete", complete("x"), proof("not a proof")))
	wantClaimLost(t, w, "claimed", 1, f.cmd(w, id, "complete", complete("x"), proof(str(c2, "proof")))) // other task's proof
	// Another actor with a valid proof: 403 proof_actor_mismatch.
	r := f.cmd(v, id, "complete", complete("x"), proof(p))
	v.wantProblem(403, "forbidden", r)
	if r.Body["reason"] != "proof_actor_mismatch" {
		t.Fatalf("reason: %s", r.Raw)
	}
	errRaw := string(r.Raw) + string(f.cmd(w, id, "complete", complete("x"), proof(str(c2, "proof"))).Raw)
	// Same actor on another credential may act (proof is bound to the actor).
	w2 := credential(t, w.id, "*")
	w2.must(200, f.renew(w2, id, cid, p, nil))
	// Loss of claim makes claimant operations fail; the claim stays.
	g := f.grant(t, w, "proof", "deny", "claim")
	if r := f.renew(w, id, cid, p, nil); r.Status != 403 && r.Status != 404 {
		t.Fatalf("renew after losing claim: %d %s", r.Status, r.Raw)
	}
	if tv := f.getTask(t, m, id); obj(tv, "current_claim")["id"] != cid {
		t.Fatalf("losing a right must not revoke the claim: %v", tv)
	}
	f.owner.must(200, f.owner.do("DELETE", f.p("/grants/"+g), nil))
	w.must(200, f.renew(w, id, cid, p, nil))

	// The proof never appears outside the claim response.
	f.mustCmd(t, w, id, "release", nil, proof(p))
	check := func(where, raw string) {
		t.Helper()
		if strings.Contains(raw, p) || strings.Contains(raw, str(c2, "proof")) {
			t.Fatalf("%s contains a claim proof", where)
		}
	}
	check("task view", string(m.do("GET", f.tp(id2), nil).Raw))
	check("task list", string(m.do("GET", f.p("/tasks"), nil).Raw))
	check("claim history", string(m.do("GET", f.tp(id, "/claims"), nil).Raw))
	allEvs, _ := f.events(t, m, nil, "")
	check("events", eventsJSON(allEvs))
	_, snapRaw, _ := f.snapshot(t, m, "")
	check("snapshot", snapRaw)
	check("problem documents", errRaw)
	check("renew response", string(w.must(200, f.renew(w, id2, str(obj(c2, "claim"), "id"), str(c2, "proof"), nil)).Raw))
}

// TestRenewKeepsLease checks that a renewal without lease_seconds keeps the
// claim's current lease, not the server default (CLAIM-020, CLAIM-023).
func TestRenewKeepsLease(t *testing.T) { // CLAIM-020, CLAIM-023, CLAIM-040
	f := newP2Board(t)
	mn, df := f.d.lim(t, "claim_lease_min_seconds"), f.d.lim(t, "claim_lease_default_seconds")
	if mn+1 >= df {
		t.Skipf("needs claim_lease_min_seconds + 1 < claim_lease_default_seconds (min %d, default %d)", mn, df)
	}
	m := f.manager(t)
	w := f.worker(t, "w")
	id := str(f.createTask(t, m, "lease", "keep", "x", nil), "id")
	c := f.claim(t, w, id, J{"lease_seconds": mn})
	cl, p := obj(c, "claim"), str(c, "proof")
	cid := str(cl, "id")
	acquired := ts(t, cl["acquired_at"])

	// A renewal without lease_seconds keeps the short lease.
	k := idem("renew-keep-" + rnd())
	rn := w.must(200, f.renew(w, id, cid, p, nil, k)).Body
	if num(rn, "lease_seconds") != float64(mn) || num(rn, "revision") != 2 {
		t.Fatalf("renew without lease_seconds must keep lease %d: %v", mn, rn)
	}
	if exp := ts(t, rn["expires_at"]); !exp.Before(acquired.Add(time.Duration(df) * time.Second)) {
		t.Fatalf("renew without lease_seconds used the default lease: expires_at %v, acquired_at %v", exp, acquired)
	}
	// The body is part of the fingerprint: an explicit lease under the same
	// key is a different request.
	w.wantProblem(409, "idempotency_conflict", f.renew(w, id, cid, p, J{"lease_seconds": df}, k))

	// An explicit value changes the lease; the next renewal without a value
	// keeps the new one.
	if rn := w.must(200, f.renew(w, id, cid, p, J{"lease_seconds": mn + 1})).Body; num(rn, "lease_seconds") != float64(mn+1) {
		t.Fatalf("renew with lease_seconds %d: %v", mn+1, rn)
	}
	if rn := w.must(200, f.renew(w, id, cid, p, nil)).Body; num(rn, "lease_seconds") != float64(mn+1) || num(rn, "revision") != 4 {
		t.Fatalf("renew without lease_seconds must keep lease %d: %v", mn+1, rn)
	}
	if v := f.getTask(t, m, id); num(obj(v, "current_claim"), "lease_seconds") != float64(mn+1) {
		t.Fatalf("task current_claim after renewals: %v", v)
	}
	f.mustCmd(t, w, id, "release", nil, proof(p))
}
