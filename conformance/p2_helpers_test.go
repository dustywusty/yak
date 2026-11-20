package conformance

// Helpers for the P2 (abby.p2.tasks) black-box tests in p2_test.go. They are
// written from spec/p2-tasks.md and ADR-0016 only; no server code is used.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const p2Profile = "abby.p2.tasks"

// p2disc is the P2 part of GET /v1/server.
type p2disc struct {
	limits      J
	proofReplay bool
	capsMap     bool // capabilities is an object (TASK-003 wording) rather than a P1 array
	raw         J
}

var (
	p2once sync.Once
	p2d    *p2disc
	p2skip string
)

// requireP2 skips the test unless the server advertises abby.p2.tasks.
func requireP2(t *testing.T) *p2disc {
	t.Helper()
	skipIfUnconfigured(t)
	p2once.Do(func() {
		res, err := httpc.Get(baseURL + "/v1/server")
		if err != nil {
			p2skip = "cannot read GET /v1/server: " + err.Error()
			return
		}
		defer res.Body.Close()
		var d J
		if err := json.NewDecoder(res.Body).Decode(&d); err != nil {
			p2skip = "GET /v1/server: bad JSON: " + err.Error()
			return
		}
		proto, _ := d["protocol"].(J)
		has := false
		if ps, ok := proto["profiles"].([]any); ok {
			for _, p := range ps {
				if p == p2Profile {
					has = true
				}
			}
		}
		if !has {
			p2skip = "server does not advertise profile " + p2Profile + " in GET /v1/server protocol.profiles (P1-only server); P2 conformance skipped"
			return
		}
		out := &p2disc{raw: d}
		out.limits, _ = d["limits"].(J)
		switch c := d["capabilities"].(type) {
		case J:
			out.capsMap = true
			out.proofReplay, _ = c["claim_proof_replay"].(bool)
		case []any:
			for _, x := range c {
				if s, _ := x.(string); s == "claim_proof_replay" || strings.HasSuffix(s, ".claim_proof_replay") {
					out.proofReplay = true
				}
			}
		}
		p2d = out
	})
	if p2d == nil {
		t.Skip(p2skip)
	}
	return p2d
}

func (d *p2disc) lim(t *testing.T, k string) int {
	t.Helper()
	v, ok := d.limits[k].(float64)
	if !ok {
		t.Fatalf("discovery limits.%s missing or not a number (TASK-002): %v", k, d.limits[k])
	}
	return int(v)
}

// tf is a board fixture for task tests.
type tf struct {
	*fixture
	d *p2disc
}

// newP2Board gates on P2, marks the test parallel, and creates a board.
func newP2Board(t *testing.T) *tf {
	t.Helper()
	d := requireP2(t)
	t.Parallel()
	return &tf{fixture: newBoard(t), d: d}
}

// manager creates an actor that may create and manage tasks everywhere
// (outside private channels).
func (f *tf) manager(t *testing.T) *actorClient {
	t.Helper()
	m := newActor(t, "mgr")
	f.grant(t, m, "*", "allow", "read", "post", "label", "task_manage", "channel_manage")
	return m
}

// worker creates an actor with read and claim on every channel.
func (f *tf) worker(t *testing.T, prefix string, extra ...string) *actorClient {
	t.Helper()
	w := newActor(t, prefix)
	f.grant(t, w, "*", "allow", append([]string{"read", "claim"}, extra...)...)
	return w
}

func proof(p string) opt { return header("Abby-Claim-Proof", p) }

// --- tiny JSON accessors ---

func str(m J, k string) string { s, _ := m[k].(string); return s }
func num(m J, k string) float64 {
	n, _ := m[k].(float64)
	return n
}
func obj(m J, k string) J { o, _ := m[k].(J); return o }
func arr(m J, k string) []any {
	a, _ := m[k].([]any)
	return a
}

func ts(t *testing.T, s any) time.Time {
	t.Helper()
	x, ok := s.(string)
	if !ok {
		t.Fatalf("timestamp %v is not a string", s)
	}
	v, err := time.Parse(time.RFC3339Nano, x)
	if err != nil {
		t.Fatalf("timestamp %q: %v", x, err)
	}
	return v
}

func sameInstant(t *testing.T, a, b any) bool {
	t.Helper()
	return ts(t, a).Equal(ts(t, b))
}

// --- task routes (ADR-0016 §2) ---

func (f *tf) tp(id string, rest ...string) string {
	return f.p("/tasks/" + id + strings.Join(rest, ""))
}

func (f *tf) createTaskResp(by *actorClient, channel, title, text string, extra J, opts ...opt) resp {
	th := J{"channel": channel, "title": title, "text": text}
	body := J{"thread": th}
	for k, v := range extra {
		if k == "labels" {
			th[k] = v
			continue
		}
		body[k] = v
	}
	return by.do("POST", f.p("/tasks"), body, append([]opt{newKey()}, opts...)...)
}

// createTask creates a task with a new thread (L1) and returns the Task.
func (f *tf) createTask(t *testing.T, by *actorClient, channel, title, text string, extra J) J {
	t.Helper()
	r := f.createTaskResp(by, channel, title, text, extra)
	if r.Status != 201 {
		t.Fatalf("create task in %s: want 201, got %d: %s", channel, r.Status, r.Raw)
	}
	return r.Body
}

func (f *tf) getTask(t *testing.T, by *actorClient, id string) J {
	t.Helper()
	return by.must(200, by.do("GET", f.tp(id), nil)).Body
}

func (f *tf) claimResp(by *actorClient, id string, body J, opts ...opt) resp {
	if body == nil {
		body = J{}
	}
	return by.do("POST", f.tp(id, "/claims"), body, append([]opt{newKey()}, opts...)...)
}

// claim claims a task and checks the basic claim response (§19.2).
func (f *tf) claim(t *testing.T, by *actorClient, id string, body J, opts ...opt) J {
	t.Helper()
	r := f.claimResp(by, id, body, opts...)
	if r.Status != 201 {
		t.Fatalf("claim %s: want 201, got %d: %s", id, r.Status, r.Raw)
	}
	if r.Body["outcome"] != "claimed" || str(r.Body, "proof") == "" || obj(r.Body, "claim") == nil {
		t.Fatalf("claim response: %s", r.Raw)
	}
	return r.Body
}

func (f *tf) claimNext(by *actorClient, body J, opts ...opt) resp {
	if body == nil {
		body = J{}
	}
	return by.do("POST", f.p("/tasks/claim-next"), body, append([]opt{newKey()}, opts...)...)
}

// cmd posts a task command (release, complete, fail, block, unblock,
// cancel, reopen). A fresh Idempotency-Key is added; a later idem() opt
// overrides it.
func (f *tf) cmd(by *actorClient, id, verb string, body J, opts ...opt) resp {
	if body == nil {
		body = J{}
	}
	return by.do("POST", f.tp(id, "/"+verb), body, append([]opt{newKey()}, opts...)...)
}

func (f *tf) renew(by *actorClient, id, claimID, pr string, body J, opts ...opt) resp {
	if body == nil {
		body = J{}
	}
	o := []opt{newKey()}
	if pr != "" {
		o = append(o, proof(pr))
	}
	return by.do("POST", f.tp(id, "/claims/"+claimID+"/renew"), body, append(o, opts...)...)
}

func (f *tf) mustCmd(t *testing.T, by *actorClient, id, verb string, body J, opts ...opt) J {
	t.Helper()
	r := f.cmd(by, id, verb, body, opts...)
	if r.Status != 200 {
		t.Fatalf("%s %s: want 200, got %d: %s", verb, id, r.Status, r.Raw)
	}
	return r.Body
}

func complete(summary string) J { return J{"result": J{"summary": summary}} }

func reason(s string) J { return J{"reason": s} }

func (f *tf) putAssignee(by *actorClient, id string, assignee any, opts ...opt) resp {
	return by.do("PUT", f.tp(id, "/assignee"), J{"assignee_actor_id": assignee}, opts...)
}

func (f *tf) putDeps(by *actorClient, id string, ids []string, opts ...opt) resp {
	if ids == nil {
		ids = []string{}
	}
	return by.do("PUT", f.tp(id, "/dependencies"), J{"task_ids": ids}, opts...)
}

func (f *tf) postBrief(by *actorClient, id, text, why string, opts ...opt) resp {
	return by.do("POST", f.tp(id, "/briefs"), J{"text": text, "reason": why}, append([]opt{newKey()}, opts...)...)
}

// wantInvalid checks a 409 invalid_transition with the given current_state.
func wantInvalid(t *testing.T, c *actorClient, state string, r resp) {
	t.Helper()
	c.wantProblem(409, "invalid_transition", r)
	if r.Body["current_state"] != state {
		t.Fatalf("invalid_transition current_state = %v, want %s: %s", r.Body["current_state"], state, r.Raw)
	}
}

// wantClaimLost checks 409 claim_lost with state and epoch (CLAIM-014).
func wantClaimLost(t *testing.T, c *actorClient, state string, epoch float64, r resp) {
	t.Helper()
	c.wantProblem(409, "claim_lost", r)
	if r.Body["state"] != state || num(r.Body, "epoch") != epoch {
		t.Fatalf("claim_lost members state=%v epoch=%v, want %s/%v: %s", r.Body["state"], r.Body["epoch"], state, epoch, r.Raw)
	}
}

// --- events ---

func (f *tf) head(t *testing.T, by *actorClient) string {
	t.Helper()
	return by.must(200, by.do("GET", f.p("/events?from=latest"), nil)).Body["next_cursor"].(string)
}

func (f *tf) since(t *testing.T, by *actorClient, cur string) ([]J, string) {
	t.Helper()
	return f.events(t, by, nil, cur)
}

// wantTypes asserts the exact sequence of event types.
func wantTypes(t *testing.T, step string, evs []J, want ...string) {
	t.Helper()
	got := types(evs)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%s: events %v, want %v", step, got, want)
	}
}

// step runs fn and asserts the exact events it committed; it returns the
// events and the advanced cursor.
func (f *tf) step(t *testing.T, by *actorClient, cur *string, name string, fn func(), want ...string) []J {
	t.Helper()
	fn()
	evs, next := f.since(t, by, *cur)
	*cur = next
	wantTypes(t, name, evs, want...)
	checkTaskEvents(t, evs)
	return evs
}

// checkTaskEvents verifies TASK-082 resources and TASK-085 view
// invalidations for a run of events.
func checkTaskEvents(t *testing.T, evs []J) {
	t.Helper()
	var lastTask J
	for _, e := range evs {
		typ := str(e, "type")
		pl := obj(e, "payload")
		if strings.HasPrefix(typ, "task.") {
			tk := obj(pl, "task")
			if tk == nil {
				t.Fatalf("%s payload has no task: %v", typ, e)
			}
			res := obj(e, "resource")
			if typ == "task.claim_renewed" {
				if res["type"] != "claim" || res["id"] != obj(pl, "claim")["id"] || res["revision"] != obj(pl, "claim")["revision"] {
					t.Fatalf("task.claim_renewed resource %v, want claim resource", res)
				}
			} else if res["type"] != "task" || res["id"] != tk["id"] || res["revision"] != tk["revision"] {
				t.Fatalf("%s resource %v, want {task,%v,%v}", typ, res, tk["id"], tk["revision"])
			}
			if str(e, "channel_id") == "" || str(e, "thread_id") == "" || e["thread_id"] != tk["thread_id"] {
				t.Fatalf("%s channel_id/thread_id: %v", typ, e)
			}
			lastTask = tk
		}
		if typ == "thread.view_invalidated" && pl["reason"] == "task_state_changed" {
			after := obj(pl, "labels_after")
			if lastTask == nil || after["abby.task.state"] != lastTask["state"] || e["thread_id"] != lastTask["thread_id"] {
				t.Fatalf("task_state_changed invalidation %v does not follow its task event (last task %v)", pl, lastTask)
			}
		}
	}
}

func countType(evs []J, typ string) int {
	n := 0
	for _, e := range evs {
		if e["type"] == typ {
			n++
		}
	}
	return n
}

func eventsJSON(evs []J) string { b, _ := json.Marshal(evs); return string(b) }

// --- waiting on observed server state ---

// waitLapsed polls the effective task view until the claim's lease is
// over (CLAIM-051). It never compares local time with expires_at.
func (f *tf) waitLapsed(t *testing.T, by *actorClient, id, claimID string, lease int) J {
	t.Helper()
	deadline := time.Now().Add(time.Duration(lease)*time.Second + 45*time.Second)
	for {
		v := f.getTask(t, by, id)
		if cc := obj(v, "current_claim"); cc == nil || cc["id"] != claimID {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatalf("lease of claim %s did not lapse within %ds + 45s: %v", claimID, lease, v)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// --- snapshots ---

func (f *tf) snapshot(t *testing.T, by *actorClient, q string) (items []J, raw string, cont string) {
	t.Helper()
	snap := by.must(201, by.do("POST", f.p("/snapshots"+q), nil)).Body
	sid := str(snap, "id")
	if sid == "" {
		sid = str(obj(snap, "snapshot"), "id")
	}
	token := ""
	var b strings.Builder
	for {
		qq := "?limit=200"
		if token != "" {
			qq += "&page_token=" + url.QueryEscape(token)
		}
		pg := by.must(200, by.do("GET", f.p("/snapshots/"+sid+qq), nil))
		b.Write(pg.Raw)
		for _, it := range arr(pg.Body, "items") {
			items = append(items, it.(J))
		}
		if pg.Body["next_page_token"] == nil {
			cont, _ = pg.Body["continuation_cursor"].(string)
			return items, b.String(), cont
		}
		token = pg.Body["next_page_token"].(string)
	}
}

// statusSet counts responses by "status code" strings.
type tally struct {
	mu sync.Mutex
	m  map[string]int
}

func (c *tally) add(r resp) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]int{}
	}
	c.m[fmt.Sprint(r.Status, r.code())]++
}

// race runs the functions concurrently, released together.
func race(fns ...func()) {
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, fn := range fns {
		wg.Add(1)
		go func(fn func()) {
			defer wg.Done()
			<-start
			fn()
		}(fn)
	}
	close(start)
	wg.Wait()
}
