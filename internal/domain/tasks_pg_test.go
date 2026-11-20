package domain_test

// P2 reference tests against real Postgres (spec/p2-tasks.md §20 REF rows
// and the concurrency cases that black-box tests cannot force). Leases use
// a lowered minimum bound (1 s) and real time.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dustywusty/yak/internal/domain"
	"github.com/dustywusty/yak/internal/events"
)

type p2 struct {
	t     *testing.T
	svc   *domain.Service
	admin *domain.Principal
	board string
	n     int
}

func setupP2(t *testing.T) *p2 {
	t.Helper()
	svc, admin, board := setup(t)
	svc.Cfg.ClaimLeaseMin = time.Second
	ctx := context.Background()
	aid := admin.ActorID
	if _, err := svc.CreateGrant(ctx, admin, board, &domain.Idem{Key: "p2admin", Namespace: board, Method: "POST", Path: "/g"},
		domain.CreateGrantRequest{Subject: domain.GrantSubject{Type: "actor", ActorID: &aid}, Pattern: "*",
			Capabilities: []string{"read", "post", "channel_manage", "claim", "task_manage", "task_admin", "label"}}); err != nil {
		t.Fatal(err)
	}
	return &p2{t: t, svc: svc, admin: admin, board: board}
}

// actor creates an actor with a credential and a grant of caps on pattern.
func (x *p2) actor(pattern string, caps ...string) *domain.Principal {
	x.t.Helper()
	ctx := context.Background()
	x.n++
	h := fmt.Sprintf("p2a%d%d", time.Now().UnixNano(), x.n)
	var id, cred string
	if err := x.svc.DB.QueryRow(ctx, `INSERT INTO actors(id, kind, handle, display_name) VALUES ('act_'||$1,'agent',$1,$1) RETURNING id`, h).Scan(&id); err != nil {
		x.t.Fatal(err)
	}
	if err := x.svc.DB.QueryRow(ctx, `INSERT INTO credentials(id, actor_id, label, scopes, verifier, expires_at) VALUES ('cred_'||$1,$2,'t','{*}','\x00', now()+interval '1 day') RETURNING id`, h, id).Scan(&cred); err != nil {
		x.t.Fatal(err)
	}
	if _, err := x.svc.CreateGrant(ctx, x.admin, x.board, x.key(), domain.CreateGrantRequest{Subject: domain.GrantSubject{Type: "actor", ActorID: &id},
		Pattern: pattern, Capabilities: caps}); err != nil {
		x.t.Fatal(err)
	}
	return &domain.Principal{MayManageCredentials: true, MayDelegate: true, ActorID: id, CredentialID: cred, Scopes: []string{"*"}, ExpiresAt: time.Now().Add(time.Hour)}
}

func (x *p2) key() *domain.Idem {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return &domain.Idem{Key: base64.RawURLEncoding.EncodeToString(b), Namespace: x.board, Method: "POST", Path: "/t"}
}

func (x *p2) task(channel, title string, deps ...string) domain.TaskView {
	x.t.Helper()
	res, err := x.svc.CreateTask(context.Background(), x.admin, x.board, x.key(), domain.CreateTaskRequest{
		Thread: &domain.TaskThreadSpec{Channel: channel, Title: title, Text: "brief for " + title}, DependsOn: deps})
	if err != nil {
		x.t.Fatal(err)
	}
	return res.Body.(domain.TaskView)
}

func (x *p2) get(id string) domain.TaskView {
	x.t.Helper()
	v, err := x.svc.GetTask(context.Background(), x.admin, x.board, id)
	if err != nil {
		x.t.Fatal(err)
	}
	return *v
}

func (x *p2) head() int64 {
	var h int64
	if err := x.svc.DB.QueryRow(context.Background(), `SELECT head_position FROM board_heads WHERE board_id=$1`, x.board).Scan(&h); err != nil {
		x.t.Fatal(err)
	}
	return h
}

func lease(n int) *int { return &n }

func code(err error) string {
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Code
	}
	if err == nil {
		return ""
	}
	return "internal: " + err.Error()
}

func proofOf(t *testing.T, r *domain.ClaimResponse) *domain.ClaimProof {
	t.Helper()
	if r.Proof == nil {
		t.Fatal("claim response has no proof")
	}
	pr, err := domain.ParseClaimProof(*r.Proof)
	if err != nil {
		t.Fatal(err)
	}
	return pr
}

func (x *p2) claim(p *domain.Principal, taskID string, secs int) (*domain.ClaimResponse, error) {
	res, err := x.svc.ClaimTask(context.Background(), p, x.board, taskID, x.key(), domain.ClaimRequest{LeaseSeconds: lease(secs)})
	if err != nil {
		return nil, err
	}
	return res.Body.(*domain.ClaimResponse), nil
}

// CLAIM-001..003, CT-007: N parallel claimers, exactly one winner.
func TestConcurrentClaimsOneWinner(t *testing.T) {
	x := setupP2(t)
	tk := x.task("work", "race")
	workers := make([]*domain.Principal, 12)
	for i := range workers {
		workers[i] = x.actor("*", "read", "claim")
	}
	var wg sync.WaitGroup
	results := make(chan error, len(workers))
	for _, w := range workers {
		wg.Add(1)
		go func(w *domain.Principal) {
			defer wg.Done()
			_, err := x.claim(w, tk.ID, 60)
			results <- err
		}(w)
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		switch code(err) {
		case "":
			wins++
		case "task_claimed":
		default:
			t.Fatalf("unexpected claim error: %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("want exactly one winner, got %d", wins)
	}
	v := x.get(tk.ID)
	if v.State != "claimed" || v.Epoch != 1 || v.CurrentClaim == nil {
		t.Fatalf("after race: state=%s epoch=%d claim=%v", v.State, v.Epoch, v.CurrentClaim)
	}
	var n int
	_ = x.svc.DB.QueryRow(context.Background(), `SELECT count(*) FROM claims WHERE task_id=$1`, tk.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("want 1 claim row, got %d", n)
	}
}

// SEL-001..005: claim-next under concurrency never claims a task twice and
// hands out the oldest eligible task first.
func TestClaimNextUnderBoardLock(t *testing.T) {
	x := setupP2(t)
	ctx := context.Background()
	const N = 24
	created := map[string]int{}
	for i := 0; i < N; i++ {
		created[x.task("queue", fmt.Sprint("q", i)).ID] = i
	}
	workers := make([]*domain.Principal, 6)
	for i := range workers {
		workers[i] = x.actor("*", "read", "claim")
	}
	var mu sync.Mutex
	claimed := map[string]int{}
	var wg sync.WaitGroup
	errs := make(chan error, len(workers))
	for _, w := range workers {
		wg.Add(1)
		go func(w *domain.Principal) {
			defer wg.Done()
			for {
				res, nw, err := x.svc.ClaimNext(ctx, w, "", x.board, x.key(), domain.ClaimNextRequest{LeaseSeconds: lease(60)})
				if err != nil {
					errs <- err
					return
				}
				if nw != nil {
					return
				}
				id := res.Body.(*domain.ClaimResponse).Task.ID
				mu.Lock()
				claimed[id]++
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if len(claimed) != N {
		t.Fatalf("want %d distinct claims, got %d", N, len(claimed))
	}
	for id, n := range claimed {
		if n != 1 {
			t.Fatalf("task %s claimed %d times", id, n)
		}
	}
	// In commit (position) order, claims follow creation order.
	rows, err := x.svc.DB.Query(ctx, `SELECT resource_id FROM events WHERE board_id=$1 AND type='task.claimed' ORDER BY position`, x.board)
	if err != nil {
		t.Fatal(err)
	}
	prev := -1
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		if created[id] <= prev {
			t.Fatalf("claim-next did not pick the oldest eligible task: %d after %d", created[id], prev)
		}
		prev = created[id]
	}
	rows.Close()
}

// CLAIM-014, CLAIM-053, CT-008: a stale proof after expiry is claim_lost,
// commits nothing (not even the lapse), and leaves a newer claim intact.
func TestStaleProofAfterExpiry(t *testing.T) {
	x := setupP2(t)
	ctx := context.Background()
	w1, w2 := x.actor("*", "read", "claim"), x.actor("*", "read", "claim")
	tk := x.task("work", "stale")
	c1, err := x.claim(w1, tk.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	pr1 := proofOf(t, c1)
	time.Sleep(1200 * time.Millisecond)
	h := x.head()
	_, err = x.svc.CompleteTask(ctx, w1, x.board, tk.ID, pr1, x.key(), domain.CompleteRequest{})
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != "claim_lost" || de.Extra["state"] != "ready" {
		t.Fatalf("want claim_lost with effective state ready, got %v %v", err, de)
	}
	if x.head() != h {
		t.Fatal("a rejected request must commit nothing, including materialization")
	}
	var recorded string
	_ = x.svc.DB.QueryRow(ctx, `SELECT state FROM tasks WHERE id=$1`, tk.ID).Scan(&recorded)
	if recorded != "claimed" {
		t.Fatalf("lapse must not be recorded by a rejected request; recorded=%s", recorded)
	}
	c2, err := x.claim(w2, tk.ID, 60)
	if err != nil {
		t.Fatal(err)
	}
	if c2.Claim.Epoch != 2 {
		t.Fatalf("epoch must strictly increase: %d", c2.Claim.Epoch)
	}
	before := x.get(tk.ID)
	h = x.head()
	for _, op := range []func() error{
		func() error {
			_, err := x.svc.CompleteTask(ctx, w1, x.board, tk.ID, pr1, x.key(), domain.CompleteRequest{})
			return err
		},
		func() error {
			_, err := x.svc.RenewClaim(ctx, w1, x.board, tk.ID, c1.Claim.ID, pr1, x.key(), domain.RenewRequest{})
			return err
		},
		func() error {
			_, err := x.svc.ReleaseTask(ctx, w1, x.board, tk.ID, pr1, x.key(), domain.ReasonRequest{})
			return err
		},
	} {
		if err := op(); code(err) != "claim_lost" {
			t.Fatalf("stale proof: want claim_lost, got %v", err)
		}
	}
	// The old claimant's proof presented by the new claimant's actor is 403.
	if _, err := x.svc.ReleaseTask(ctx, w2, x.board, tk.ID, pr1, x.key(), domain.ReasonRequest{}); code(err) != "claim_lost" && code(err) != "forbidden" {
		t.Fatalf("other actor's proof: %v", err)
	}
	if _, err := x.svc.ReleaseTask(ctx, w1, x.board, tk.ID, proofOf(t, c2), x.key(), domain.ReasonRequest{}); code(err) != "forbidden" {
		t.Fatalf("valid proof of another actor must be 403, got %v", err)
	}
	after := x.get(tk.ID)
	if x.head() != h || after.Revision != before.Revision || after.State != "claimed" || after.CurrentClaim.ID != c2.Claim.ID {
		t.Fatalf("newer state changed: %+v", after)
	}
}

// CLAIM-050..052, CT-032: the lapse is visible in every read before the
// sweeper runs, and materialization writes exactly that view.
func TestExpiryProjectionMatchesMaterialization(t *testing.T) {
	x := setupP2(t)
	ctx := context.Background()
	w := x.actor("*", "read", "claim")
	tk := x.task("work", "lapse")
	c, err := x.claim(w, tk.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1200 * time.Millisecond)
	v := x.get(tk.ID)
	if v.State != "ready" || v.CurrentClaim != nil || v.LastClaim == nil || v.LastClaim.Status != "expired" ||
		v.LastClaim.EndRecordedAt != nil || !v.LastClaim.EndedAt.Equal(c.Claim.ExpiresAt) || !v.UpdatedAt.Equal(c.Claim.ExpiresAt) ||
		v.Revision != c.Task.Revision+1 {
		t.Fatalf("effective view wrong: %+v last=%+v", v, v.LastClaim)
	}
	pg, err := x.svc.ListTasks(ctx, x.admin, x.board, domain.TaskFilter{States: []string{"ready"}}, 0, 50)
	if err != nil || len(pg.Items) != 1 || pg.Items[0].ID != tk.ID {
		t.Fatalf("state filter must use the effective state: %v %+v", err, pg)
	}
	th, err := x.svc.ListThreads(ctx, x.admin, x.board, events.Filter{Labels: map[string][]string{"yak.task.state": {"ready"}}}, 0, 50)
	if err != nil || len(th.Threads) != 1 || th.Threads[0].Labels["yak.task.state"] != "ready" {
		t.Fatalf("yak.task.state label must be effective: %v %+v", err, th)
	}
	n, err := x.svc.SweepClaimExpiry(ctx, 100)
	if err != nil || n < 1 {
		t.Fatalf("sweep: n=%d err=%v", n, err)
	}
	m := x.get(tk.ID)
	if m.Revision != v.Revision || m.State != "ready" || !m.UpdatedAt.Equal(v.UpdatedAt) || !m.StateChangedAt.Equal(v.StateChangedAt) || m.LastClaim.EndRecordedAt == nil {
		t.Fatalf("materialized view differs from projection: %+v vs %+v", m, v)
	}
	var effAt time.Time
	var rev int64
	if err := x.svc.DB.QueryRow(ctx, `SELECT effective_at, resource_revision FROM events WHERE board_id=$1 AND type='task.claim_expired' AND resource_id=$2`,
		x.board, tk.ID).Scan(&effAt, &rev); err != nil {
		t.Fatal(err)
	}
	if !effAt.Equal(c.Claim.ExpiresAt) || rev != v.Revision {
		t.Fatalf("claim_expired: effective_at=%v rev=%d", effAt, rev)
	}
	if n, _ := x.svc.SweepClaimExpiry(ctx, 100); n != 0 {
		t.Fatal("a lapse must be materialized at most once")
	}
	// Another actor claims at once.
	if _, err := x.claim(x.actor("*", "read", "claim"), tk.ID, 60); err != nil {
		t.Fatal(err)
	}
}

// DEP-002, CT-037: cycles (including self-dependency) are rejected.
func TestDependencyCycle(t *testing.T) {
	x := setupP2(t)
	ctx := context.Background()
	a := x.task("work", "a")
	b := x.task("work", "b", a.ID)
	c := x.task("work", "c", b.ID)
	if b.State != "waiting" {
		t.Fatalf("b must wait for a: %s", b.State)
	}
	h := x.head()
	_, err := x.svc.SetDependencies(ctx, x.admin, x.board, a.ID, x.get(a.ID).Revision, nil, domain.DependenciesRequest{TaskIDs: []string{c.ID}})
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != "dependency_cycle" {
		t.Fatalf("want dependency_cycle, got %v", err)
	}
	if cyc, _ := de.Extra["cycle"].([]string); len(cyc) < 3 {
		t.Fatalf("cycle path missing: %v", de.Extra)
	}
	if _, err := x.svc.SetDependencies(ctx, x.admin, x.board, a.ID, x.get(a.ID).Revision, nil, domain.DependenciesRequest{TaskIDs: []string{a.ID}}); code(err) != "dependency_cycle" {
		t.Fatalf("self-dependency: %v", err)
	}
	if x.head() != h {
		t.Fatal("rejected dependency edits must commit nothing")
	}
}

// BATCH-003, CT-012: any failure rejects the whole batch.
func TestBatchAtomicRollback(t *testing.T) {
	x := setupP2(t)
	ctx := context.Background()
	hidden := x.task("secret", "hidden")
	// A planner that can manage tasks in "plan" but cannot read "secret".
	planner := x.actor("plan", "read", "post", "task_manage", "channel_manage")
	count := func() (n int) {
		_ = x.svc.DB.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE board_id=$1`, x.board).Scan(&n)
		return
	}
	n0, h0 := count(), x.head()
	item := func(ref string, deps ...domain.BatchDependency) domain.BatchTaskItem {
		return domain.BatchTaskItem{Ref: ref, Thread: &domain.TaskThreadSpec{Channel: "plan", Title: ref, Text: "do " + ref}, DependsOn: deps}
	}
	_, err := x.svc.CreateTaskBatch(ctx, planner, x.board, x.key(), domain.CreateTaskBatchRequest{Tasks: []domain.BatchTaskItem{
		item("a", domain.BatchDependency{Ref: "c"}), item("b", domain.BatchDependency{Ref: "a"}), item("c", domain.BatchDependency{Ref: "b"})}})
	if code(err) != "dependency_cycle" {
		t.Fatalf("cycle: want dependency_cycle, got %v", err)
	}
	_, err = x.svc.CreateTaskBatch(ctx, planner, x.board, x.key(), domain.CreateTaskBatchRequest{Tasks: []domain.BatchTaskItem{
		item("a"), item("b", domain.BatchDependency{Ref: "a"}), item("c", domain.BatchDependency{TaskID: hidden.ID})}})
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != "not_found" {
		t.Fatalf("unreadable ref: want not_found, got %v", err)
	}
	be, _ := de.Extra["errors"].([]domain.BatchError)
	if len(be) != 1 || be[0].Index != 2 || be[0].Ref != "c" {
		t.Fatalf("errors list: %+v", de.Extra)
	}
	if count() != n0 || x.head() != h0 {
		t.Fatal("a rejected batch must commit nothing")
	}
	res, err := x.svc.CreateTaskBatch(ctx, planner, x.board, x.key(), domain.CreateTaskBatchRequest{Tasks: []domain.BatchTaskItem{
		item("b", domain.BatchDependency{Ref: "a"}), item("a")}})
	if err != nil {
		t.Fatal(err)
	}
	out := res.Body.(domain.TaskBatchView)
	if out.Tasks[0].Ref != "b" || out.Tasks[0].Task.State != "waiting" || out.Tasks[1].Task.State != "ready" ||
		len(out.Tasks[0].Task.Dependencies) != 1 || out.Tasks[0].Task.Dependencies[0].TaskID != out.Tasks[1].Task.ID {
		t.Fatalf("batch mapping: %+v", out)
	}
}

// BRIEF-005, CT-026: brief update and completion serialize; the first to
// commit wins and the other fails cleanly.
func TestBriefUpdateRacingCompletion(t *testing.T) {
	x := setupP2(t)
	ctx := context.Background()
	w := x.actor("*", "read", "claim")
	for i := 0; i < 6; i++ {
		tk := x.task("work", fmt.Sprint("race", i))
		c, err := x.claim(w, tk.ID, 60)
		if err != nil {
			t.Fatal(err)
		}
		pr := proofOf(t, c)
		var wg sync.WaitGroup
		var errC, errB error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, errC = x.svc.CompleteTask(ctx, w, x.board, tk.ID, pr, x.key(), domain.CompleteRequest{})
		}()
		go func() {
			defer wg.Done()
			r := "scope changed"
			_, errB = x.svc.UpdateBrief(ctx, x.admin, x.board, tk.ID, c.Task.Revision, x.key(), domain.BriefUpdateRequest{Text: "new brief", Reason: &r})
		}()
		wg.Wait()
		v := x.get(tk.ID)
		switch {
		case errC == nil && (code(errB) == "invalid_transition" || code(errB) == "revision_conflict"):
			if v.State != "completed" || v.BriefRevision != 1 {
				t.Fatalf("completion won but state=%s brief=%d", v.State, v.BriefRevision)
			}
		case errB == nil && code(errC) == "claim_lost":
			if v.State != "blocked" || v.BriefRevision != 2 || v.CurrentClaim != nil || v.LastClaim.Status != "revoked" {
				t.Fatalf("brief update won but %+v", v)
			}
		default:
			t.Fatalf("both or neither committed: complete=%v brief=%v", errC, errB)
		}
	}
}

// TASK-053, CT-027: assignment during a live claim is task_claim_active
// and changes neither the claim nor the assignment.
func TestAssignmentVsLiveClaim(t *testing.T) {
	x := setupP2(t)
	ctx := context.Background()
	w, other := x.actor("*", "read", "claim"), x.actor("*", "read", "claim")
	tk := x.task("work", "assign")
	c, err := x.claim(w, tk.ID, 60)
	if err != nil {
		t.Fatal(err)
	}
	h := x.head()
	_, err = x.svc.SetAssignee(ctx, x.admin, x.board, tk.ID, c.Task.Revision, nil, domain.AssigneeRequest{AssigneeActorID: &other.ActorID})
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != "task_claim_active" || de.Extra["claim_expires_at"] == nil {
		t.Fatalf("want task_claim_active, got %v", err)
	}
	v := x.get(tk.ID)
	if x.head() != h || v.AssigneeActorID != nil || v.CurrentClaim == nil || v.CurrentClaim.ID != c.Claim.ID {
		t.Fatalf("claim or assignment changed: %+v", v)
	}
	// Block (revokes), reassign, unblock; the old claimant is no longer eligible.
	r := "reassign"
	if _, err := x.svc.BlockTask(ctx, x.admin, x.board, tk.ID, nil, nil, x.key(), domain.ReasonRequest{Reason: &r}); err != nil {
		t.Fatal(err)
	}
	if _, err := x.svc.SetAssignee(ctx, x.admin, x.board, tk.ID, x.get(tk.ID).Revision, nil, domain.AssigneeRequest{AssigneeActorID: &other.ActorID}); err != nil {
		t.Fatal(err)
	}
	if _, err := x.svc.UnblockTask(ctx, x.admin, x.board, tk.ID, nil, x.key(), domain.ReasonRequest{}); err != nil {
		t.Fatal(err)
	}
	_, err = x.claim(w, tk.ID, 60)
	if !errors.As(err, &de) || de.Code != "forbidden" || de.Extra["reason"] != "not_assignee" {
		t.Fatalf("want 403 not_assignee, got %v", err)
	}
	if _, err := x.claim(other, tk.ID, 60); err != nil {
		t.Fatal(err)
	}
}

// CLAIM-041..043, CT-009: replay of a claim returns the original claim and
// never extends or restores its lease; replay material is ciphertext only
// and is destroyed when the claim ends.
func TestClaimReplayNeverExtends(t *testing.T) {
	x := setupP2(t)
	ctx := context.Background()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	kr, err := domain.ParseClaimKeyring("k1:" + base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	x.svc.ClaimKeys = kr
	w := x.actor("*", "read", "claim")
	tk := x.task("work", "replay")
	idem := x.key()
	res, err := x.svc.ClaimTask(ctx, w, x.board, tk.ID, idem, domain.ClaimRequest{LeaseSeconds: lease(2)})
	if err != nil {
		t.Fatal(err)
	}
	orig := res.Body.(*domain.ClaimResponse)
	secret := strings.SplitN(*orig.Proof, ".", 3)[2]
	var ct []byte
	if err := x.svc.DB.QueryRow(ctx, `SELECT proof_ciphertext FROM claims WHERE id=$1`, orig.Claim.ID).Scan(&ct); err != nil || len(ct) == 0 || strings.Contains(string(ct), secret) {
		t.Fatalf("replay material must be ciphertext: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	h := x.head()
	rep, err := x.svc.ClaimTask(ctx, w, x.board, tk.ID, idem, domain.ClaimRequest{LeaseSeconds: lease(2)})
	if err != nil || !rep.Replayed || rep.ResourceID != orig.Claim.ID {
		t.Fatalf("replay: %v %+v", err, rep)
	}
	cr, err := x.svc.ReplayClaim(ctx, w, x.board, rep.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	if !cr.Claim.ExpiresAt.Equal(orig.Claim.ExpiresAt) || cr.Claim.Epoch != orig.Claim.Epoch || cr.Proof == nil || *cr.Proof != *orig.Proof || x.head() != h {
		t.Fatalf("active replay must return the same claim and proof without writes: %+v", cr)
	}
	time.Sleep(2 * time.Second)
	rep, err = x.svc.ClaimTask(ctx, w, x.board, tk.ID, idem, domain.ClaimRequest{LeaseSeconds: lease(2)})
	if err != nil || !rep.Replayed {
		t.Fatalf("replay after lapse: %v", err)
	}
	cr, err = x.svc.ReplayClaim(ctx, w, x.board, rep.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	if cr.Proof != nil || !cr.ProofUnavailable || cr.Claim.Status != "expired" || !cr.Claim.ExpiresAt.Equal(orig.Claim.ExpiresAt) || x.head() != h {
		t.Fatalf("replay after lapse must not restore the lease: %+v", cr)
	}
	if v := x.get(tk.ID); v.State != "ready" {
		t.Fatalf("lapsed claim stays lapsed: %s", v.State)
	}
	// Ending a claim destroys its replay material.
	c2, err := x.claim(w, tk.ID, 60)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.svc.ReleaseTask(ctx, w, x.board, tk.ID, proofOf(t, c2), x.key(), domain.ReasonRequest{}); err != nil {
		t.Fatal(err)
	}
	var left int
	_ = x.svc.DB.QueryRow(ctx, `SELECT count(*) FROM claims WHERE task_id=$1 AND proof_ciphertext IS NOT NULL`, tk.ID).Scan(&left)
	if left != 0 {
		t.Fatalf("ended claims must not keep replay material (%d left)", left)
	}
}

// DEP-007, DEP-009: completion readies dependents in the same transaction;
// hidden prerequisites are neither listed nor counted.
func TestCompletionReadiesDependentsAndHidesPrereqs(t *testing.T) {
	x := setupP2(t)
	ctx := context.Background()
	w := x.actor("*", "read", "claim")
	reader := x.actor("server", "read")
	b := x.task("server", "b")
	h := x.task("ops", "h")
	a := x.task("server", "a", b.ID, h.ID)
	ra, err := x.svc.GetTask(ctx, reader, x.board, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ra.Dependencies) != 1 || ra.Dependencies[0].TaskID != b.ID || ra.UnreadableDependenciesPending {
		t.Fatalf("reader view: %+v", ra)
	}
	for _, id := range []string{b.ID, h.ID} {
		c, err := x.claim(w, id, 60)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := x.svc.CompleteTask(ctx, w, x.board, id, proofOf(t, c), x.key(), domain.CompleteRequest{}); err != nil {
			t.Fatal(err)
		}
		if id == b.ID {
			ra, _ = x.svc.GetTask(ctx, reader, x.board, a.ID)
			if ra.State != "waiting" || !ra.UnreadableDependenciesPending {
				t.Fatalf("after b: %+v", ra)
			}
		}
	}
	if v := x.get(a.ID); v.State != "ready" {
		t.Fatalf("a must be ready after its last prerequisite completes: %s", v.State)
	}
	page, err := x.svc.ReadEvents(ctx, reader, x.board, domain.ReadEventsRequest{Limit: 200, Filter: events.Filter{Types: []string{"task.became_ready"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("want 1 became_ready, got %d", len(page.Events))
	}
	var pl map[string]any
	_ = json.Unmarshal(page.Events[0].Payload, &pl)
	if _, ok := pl["prerequisite_task_id"]; ok {
		t.Fatalf("hidden prerequisite leaked: %s", page.Events[0].Payload)
	}
	if strings.Contains(string(page.Events[0].Payload), h.ID) {
		t.Fatalf("hidden prerequisite ID leaked: %s", page.Events[0].Payload)
	}
}

// DEP-007: an older unsatisfied dependent must not stop later eligible ones.
func TestCompletionSkipsUnsatisfiedDependents(t *testing.T) {
	x := setupP2(t)
	ctx := context.Background()
	w := x.actor("*", "read", "claim")
	reader := x.actor("server", "read")
	p := x.task("ops", "p")
	q := x.task("ops", "q")
	a := x.task("server", "a", p.ID, q.ID)
	blocked := x.task("server", "blocked", p.ID)
	reason := "manual hold"
	if _, err := x.svc.BlockTask(ctx, x.admin, x.board, blocked.ID, nil, &blocked.Revision, x.key(), domain.ReasonRequest{Reason: &reason}); err != nil {
		t.Fatal(err)
	}
	b := x.task("server", "b", p.ID)
	c := x.task("server", "c", p.ID)
	claim, err := x.claim(w, p.ID, 60)
	if err != nil {
		t.Fatal(err)
	}
	head := x.head()
	if _, err := x.svc.CompleteTask(ctx, w, x.board, p.ID, proofOf(t, claim), x.key(), domain.CompleteRequest{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		task     domain.TaskView
		state    string
		revision int64
	}{
		{a, "waiting", a.Revision}, {blocked, "blocked", blocked.Revision + 1},
		{b, "ready", b.Revision + 1}, {c, "ready", c.Revision + 1},
	} {
		v := x.get(want.task.ID)
		if v.State != want.state || v.Revision != want.revision {
			t.Errorf("%s: want %s revision %d, got %s revision %d", want.task.Title, want.state, want.revision, v.State, v.Revision)
		}
	}
	visible, err := x.svc.GetTask(ctx, reader, x.board, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if visible.State != "waiting" || len(visible.Dependencies) != 0 || !visible.UnreadableDependenciesPending {
		t.Fatalf("hidden pending q: %+v", visible)
	}
	page, err := x.svc.ReadEvents(ctx, reader, x.board, domain.ReadEventsRequest{Limit: 200, Filter: events.Filter{Types: []string{"task.became_ready"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 {
		t.Fatalf("want B and C ready events, got %d", len(page.Events))
	}
	for i, id := range []string{b.ID, c.ID} {
		ev := page.Events[i]
		if ev.Resource.ID != id {
			t.Fatalf("ready event order: %+v", ev)
		}
		var payload struct {
			Task  domain.TaskView `json:"task"`
			Cause string          `json:"cause"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Task.ID != id || payload.Task.State != "ready" || payload.Task.Revision != 2 || payload.Cause != "dependencies_satisfied" {
			t.Fatalf("ready event payload: %s", ev.Payload)
		}
		if strings.Contains(string(ev.Payload), p.ID) || strings.Contains(string(ev.Payload), "prerequisite_task_id") {
			t.Fatalf("hidden prerequisite leaked: %s", ev.Payload)
		}
	}
	// The completion, both state changes, and both events must commit together.
	var transactions int
	if err := x.svc.DB.QueryRow(ctx, `SELECT count(DISTINCT xid) FROM (
  SELECT xmin::text AS xid FROM tasks WHERE board_id=$1 AND id=ANY($2::text[])
  UNION ALL SELECT xmin::text FROM events WHERE board_id=$1 AND position>$3
 ) changes`, x.board, []string{p.ID, b.ID, c.ID}, head).Scan(&transactions); err != nil {
		t.Fatal(err)
	}
	if transactions != 1 {
		t.Fatalf("completion and activation used %d transactions", transactions)
	}
	// Completing the remaining hidden prerequisite activates A, but never the blocked task.
	claim, err = x.claim(w, q.ID, 60)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.svc.CompleteTask(ctx, w, x.board, q.ID, proofOf(t, claim), x.key(), domain.CompleteRequest{}); err != nil {
		t.Fatal(err)
	}
	if v := x.get(a.ID); v.State != "ready" {
		t.Fatalf("a after q: %+v", v)
	}
	if v := x.get(blocked.ID); v.State != "blocked" {
		t.Fatalf("blocked task changed: %+v", v)
	}
}
