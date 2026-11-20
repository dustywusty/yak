package conformance

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// Requirement and scenario IDs refer to spec/protocol.md and design §14.1.

func TestDiscovery(t *testing.T) { // DISC-001..003
	c := &actorClient{t: t}
	skipIfUnconfigured(t)
	d := c.must(200, c.do("GET", "/v1/server", nil)).Body
	p := d["protocol"].(J)
	if !strings.Contains(fmt.Sprint(p["versions"]), "1") {
		t.Fatalf("protocol versions %v", p["versions"])
	}
	for _, k := range []string{"capabilities", "limits", "retention", "authentication", "implementation"} {
		if d[k] == nil {
			t.Fatalf("discovery missing %q", k)
		}
	}
	if strings.Contains(string(c.do("GET", "/v1/server", nil).Raw), "abby.") && strings.Contains(string(c.do("GET", "/v1/server", nil).Raw), "token\"") {
		t.Fatal("discovery must not expose secrets")
	}
}

func TestAuthentication(t *testing.T) { // AUTH-001..005
	skipIfUnconfigured(t)
	anon := &actorClient{t: t}
	anon.wantProblem(401, "unauthenticated", anon.do("GET", "/v1/me", nil))
	bad := &actorClient{t: t, token: "abby.cred_nope.secret"}
	bad.wantProblem(401, "unauthenticated", bad.do("GET", "/v1/me", nil))

	u := newActor(t, "auth")
	me := u.must(200, u.do("GET", "/v1/me", nil)).Body
	if me["actor"].(J)["id"] != u.id {
		t.Fatal("me must identify the credential's actor")
	}
	// Credential ceiling: a read-only credential cannot mint broader authority.
	ro := credential(t, u.id, "read")
	ro.wantProblem(403, "forbidden", ro.do("POST", "/v1/credentials", J{"scopes": []string{"read", "post"}}, newKey()))
	// Non-admins cannot create actors or boards.
	u.wantProblem(403, "forbidden", u.do("POST", "/v1/actors", J{"kind": "agent", "handle": "x" + rnd()}, newKey()))
	u.wantProblem(403, "forbidden", u.do("POST", "/v1/boards", J{"name": "x"}, newKey()))
	// Revocation takes effect for every later request.
	credID := me["credential"].(J)["id"].(string)
	u.must(200, u.do("DELETE", "/v1/credentials/"+credID, nil))
	u.wantProblem(401, "unauthenticated", u.do("GET", "/v1/me", nil))
}

func TestWireRules(t *testing.T) { // WIRE-001..006
	f := newBoard(t)
	a := newActor(t, "wire")
	f.grant(t, a, "*", "allow", "read", "post", "channel_manage")
	// Idempotency-Key is required on creates.
	a.wantProblem(400, "validation_failed", a.do("POST", f.p("/threads"), J{"channel": "c", "title": "t", "text": "x"}))
	// Unknown request fields are rejected.
	a.wantProblem(400, "validation_failed", a.do("POST", f.p("/threads"), J{"channel": "c", "title": "t", "text": "x", "titel": "typo"}, newKey()))
	// Extension objects must be namespaced.
	a.wantProblem(400, "validation_failed", a.do("POST", f.p("/threads"), J{"channel": "c", "title": "t", "text": "x", "extensions": J{"plain": 1}}, newKey()))
	_, root := f.post(t, a, "c", "t", "x")
	// Destructive edits require If-Match.
	a.wantProblem(428, "precondition_required", a.do("PATCH", f.p("/messages/"+root["id"].(string)), J{"text": "y"}))
	// Unknown routes produce problem documents.
	a.wantProblem(404, "not_found", a.do("GET", "/v1/nope", nil))
	// Malformed cursor.
	a.wantProblem(400, "validation_failed", a.do("GET", f.p("/events?cursor=garbage"), nil))
}

func TestChannelIsolation(t *testing.T) { // AUTH-010..016, PAR-003
	f := newBoard(t)
	alice, bob := newActor(t, "alice"), newActor(t, "bob")
	f.grant(t, nil, "*", "allow", "read", "post", "channel_manage")
	f.grant(t, alice, "eng/security", "allow", "read", "post")
	f.channel(t, alice, "eng/security", "private")

	secret, secRoot := f.post(t, alice, "eng/security", "incident", "the secret text")
	general, _ := f.post(t, bob, "general", "hi", "hello")
	sid := secret["id"].(string)

	// Direct reads are hidden as not found.
	bob.wantProblem(404, "not_found", bob.do("GET", f.p("/threads/"+sid), nil))
	bob.wantProblem(404, "not_found", bob.do("GET", f.p("/messages/"+secRoot["id"].(string)), nil))
	bob.wantProblem(404, "not_found", bob.do("GET", f.p("/threads/"+sid+"/messages"), nil))
	// Writes are refused.
	r := bob.do("POST", f.p("/threads/"+sid+"/messages"), J{"text": "peek"}, newKey())
	if r.Status != 404 && r.Status != 403 {
		t.Fatalf("reply into private thread: %d", r.Status)
	}
	bob.wantProblem(403, "forbidden", bob.do("POST", f.p("/threads"), J{"channel": "eng/security", "title": "x", "text": "y"}, newKey()))
	bob.wantProblem(403, "forbidden", bob.do("POST", f.p("/threads"), J{"channel": "eng/security/sub", "title": "x", "text": "y"}, newKey()))
	// Listings, channel lists, events and snapshots never mention it.
	for _, path := range []string{"/threads", "/channels", "/events?limit=200"} {
		raw := string(bob.must(200, bob.do("GET", f.p(path), nil)).Raw)
		if strings.Contains(raw, sid) || strings.Contains(raw, "secret text") || strings.Contains(raw, "eng/security") {
			t.Fatalf("bob %s leaks private content: %s", path, raw)
		}
	}
	snap := bob.must(201, bob.do("POST", f.p("/snapshots"), nil)).Body
	page := string(bob.must(200, bob.do("GET", f.p("/snapshots/"+snap["id"].(string)), nil)).Raw)
	if strings.Contains(page, sid) || strings.Contains(page, "secret") {
		t.Fatalf("snapshot leaks private content")
	}
	if !strings.Contains(page, general["id"].(string)) {
		t.Fatalf("snapshot missing readable thread")
	}
	// Alice sees both.
	raw := string(alice.must(200, alice.do("GET", f.p("/threads"), nil)).Raw)
	if !strings.Contains(raw, sid) || !strings.Contains(raw, general["id"].(string)) {
		t.Fatalf("alice listing incomplete: %s", raw)
	}
	// Explicit deny overrides allow.
	f.grant(t, alice, "eng/security", "deny", "read")
	alice.wantProblem(404, "not_found", alice.do("GET", f.p("/threads/"+sid), nil))
	// Effective-permission introspection.
	pv := bob.must(200, bob.do("GET", f.p("/permissions?channel=eng/security"), nil)).Body
	if len(pv["capabilities"].([]any)) != 0 || pv["private_boundary"] != "eng/security" {
		t.Fatalf("permissions: %v", pv)
	}
}

func TestBoardIsolation(t *testing.T) { // PAR-001
	f1, f2 := newBoard(t), newBoard(t)
	a := newActor(t, "iso")
	f1.grant(t, a, "*", "allow", "read", "post", "channel_manage")
	a.wantProblem(404, "not_found", a.do("GET", f2.p(""), nil))
	a.wantProblem(404, "not_found", a.do("GET", f2.p("/events"), nil))
	boards := string(a.must(200, a.do("GET", "/v1/boards", nil)).Raw)
	if strings.Contains(boards, f2.board) || !strings.Contains(boards, f1.board) {
		t.Fatalf("board listing: %s", boards)
	}
	// Board-restricted credential.
	b2 := credential(t, a.id, "*")
	_ = b2
	restricted := admin(t).must(201, admin(t).do("POST", "/v1/credentials", J{"actor_id": a.id, "scopes": []string{"*"}, "board_ids": []string{f2.board}}, newKey())).Body
	rc := &actorClient{t: t, token: restricted["token"].(string), id: a.id}
	rc.wantProblem(404, "not_found", rc.do("GET", f1.p(""), nil))
}

func TestCT002IdempotentRetry(t *testing.T) {
	f := newBoard(t)
	a := newActor(t, "idem")
	f.grant(t, a, "*", "allow", "read", "post", "channel_manage")
	body := J{"channel": "c", "title": "t", "text": "once"}
	k := idem("retry-" + rnd())
	r1 := a.must(201, a.do("POST", f.p("/threads"), body, k))
	r2 := a.must(201, a.do("POST", f.p("/threads"), J{"text": "once", "title": "t", "channel": "c"}, k))
	if r2.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatal("replay must be marked")
	}
	id1, id2 := r1.Body["thread"].(J)["id"], r2.Body["thread"].(J)["id"]
	if id1 != id2 {
		t.Fatalf("replay created a different thread: %v vs %v", id1, id2)
	}
	evs, _ := f.events(t, a, url.Values{"type": {"thread.created"}}, "")
	if len(evs) != 1 {
		t.Fatalf("want one thread.created event, got %d", len(evs))
	}
	// Concurrent identical requests resolve to one committed operation.
	k2 := idem("conc-" + rnd())
	var wg sync.WaitGroup
	ids := make([]string, 8)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := a.do("POST", f.p("/threads"), J{"channel": "c", "title": "c", "text": "c"}, k2)
			if r.Status == 201 {
				ids[i] = r.Body["thread"].(J)["id"].(string)
			}
		}(i)
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] || id == "" {
			t.Fatalf("concurrent idempotent requests diverged: %v", ids)
		}
	}
}

func TestCT003IdempotencyConflict(t *testing.T) {
	f := newBoard(t)
	a := newActor(t, "idemc")
	f.grant(t, a, "*", "allow", "read", "post", "channel_manage")
	k := idem("conflict-" + rnd())
	a.must(201, a.do("POST", f.p("/threads"), J{"channel": "c", "title": "t", "text": "one"}, k))
	a.wantProblem(409, "idempotency_conflict", a.do("POST", f.p("/threads"), J{"channel": "c", "title": "t", "text": "two"}, k))
	evs, _ := f.events(t, a, url.Values{"type": {"thread.created"}}, "")
	if len(evs) != 1 {
		t.Fatalf("conflict must not create side effects; %d events", len(evs))
	}
	// Failures do not reserve the key.
	k2 := idem("fail-" + rnd())
	a.wantProblem(400, "validation_failed", a.do("POST", f.p("/threads"), J{"channel": "BAD", "title": "t", "text": "x"}, k2))
	a.must(201, a.do("POST", f.p("/threads"), J{"channel": "c", "title": "t", "text": "x"}, k2))
}

func TestCT004ConcurrentEdits(t *testing.T) {
	f := newBoard(t)
	a := newActor(t, "edit")
	f.grant(t, a, "*", "allow", "read", "post", "channel_manage")
	_, root := f.post(t, a, "c", "t", "v1")
	mid := root["id"].(string)
	var wg sync.WaitGroup
	var mu sync.Mutex
	codes := map[int]int{}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := a.do("PATCH", f.p("/messages/"+mid), J{"text": fmt.Sprintf("edit %d", i)}, ifMatch(1))
			mu.Lock()
			codes[r.Status]++
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	if codes[200] != 1 || codes[412] != 9 {
		t.Fatalf("want one 200 and nine 412, got %v", codes)
	}
	m := a.must(200, a.do("GET", f.p("/messages/"+mid), nil))
	if m.Body["revision"].(float64) != 2 || m.Header.Get("ETag") != `"2"` {
		t.Fatalf("revision/etag after edit: %v %s", m.Body["revision"], m.Header.Get("ETag"))
	}
}

func TestCT005GuardedReplyRace(t *testing.T) {
	f := newBoard(t)
	a, b := newActor(t, "ga"), newActor(t, "gb")
	f.grant(t, nil, "*", "allow", "read", "post", "channel_manage", "label")
	th, root := f.post(t, a, "c", "t", "root")
	tid, rid := th["id"].(string), root["id"].(string)
	var wg sync.WaitGroup
	var mu sync.Mutex
	codes := map[string]int{}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		c := a
		if i%2 == 1 {
			c = b
		}
		go func(i int, c *actorClient) {
			defer wg.Done()
			r := c.do("POST", f.p("/threads/"+tid+"/messages"), J{"text": fmt.Sprint(i), "expect_latest_message_id": rid}, newKey())
			mu.Lock()
			codes[fmt.Sprint(r.Status, r.code())]++
			mu.Unlock()
		}(i, c)
	}
	wg.Wait()
	if codes["201"] != 1 || codes["409newer_message"] != 9 {
		t.Fatalf("guarded reply race: %v", codes)
	}
	tv := a.must(200, a.do("GET", f.p("/threads/"+tid), nil)).Body["thread"].(J)
	if tv["message_count"].(float64) != 2 {
		t.Fatalf("stale guards must not append: %v", tv["message_count"])
	}
	// Label changes are not new replies for guard purposes.
	latest := tv["last_message_id"].(string)
	a.must(200, a.do("PATCH", f.p("/messages/"+latest+"/labels"), J{"operations": []J{{"op": "set", "key": "k", "value": "v"}}}))
	a.must(201, a.do("POST", f.p("/threads/"+tid+"/messages"), J{"text": "ok", "expect_latest_message_id": latest}, newKey()))
}

func TestCT006LabelCompareAndSet(t *testing.T) {
	f := newBoard(t)
	a := newActor(t, "lab")
	f.grant(t, a, "*", "allow", "read", "post", "channel_manage", "label")
	_, root := f.post(t, a, "c", "t", "x")
	mid := root["id"].(string)
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := []string{}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v := fmt.Sprint("w", i)
			r := a.do("PATCH", f.p("/messages/"+mid+"/labels"), J{"operations": []J{{"op": "set", "key": "owner", "value": v, "expect": J{"absent": true}}}})
			if r.Status == 200 {
				mu.Lock()
				winners = append(winners, v)
				mu.Unlock()
			} else if r.Status != 412 {
				t.Errorf("unexpected %d %s", r.Status, r.Raw)
			}
		}(i)
	}
	wg.Wait()
	if len(winners) != 1 {
		t.Fatalf("want exactly one CAS winner, got %v", winners)
	}
	m := a.must(200, a.do("GET", f.p("/messages/"+mid), nil)).Body
	if m["labels"].(J)["owner"] != winners[0] {
		t.Fatalf("label state %v != winner %v", m["labels"], winners)
	}
	evs, _ := f.events(t, a, url.Values{"type": {"message.labels_changed"}}, "")
	if len(evs) != 1 {
		t.Fatalf("want one labels_changed event, got %d", len(evs))
	}
	// Reserved namespace.
	a.wantProblem(400, "validation_failed", a.do("PATCH", f.p("/messages/"+mid+"/labels"), J{"operations": []J{{"op": "set", "key": "abby.task.state", "value": "done"}}}))
}

func TestCT013NoPermanentSkips(t *testing.T) {
	f := newBoard(t)
	f.grant(t, nil, "*", "allow", "read", "post", "channel_manage")
	writers := []*actorClient{newActor(t, "w1"), newActor(t, "w2"), newActor(t, "w3")}
	reader := newActor(t, "r")
	th, _ := f.post(t, writers[0], "c", "t", "start")
	tid := th["id"].(string)
	_, cursor := f.events(t, reader, nil, "")

	created := sync.Map{}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	seen := map[string]bool{}
	var readErr error
	readerDone := make(chan struct{})
	go func() { // reader advances concurrently with commits
		defer close(readerDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			r := reader.do("GET", f.p("/events?limit=7&cursor="+url.QueryEscape(cursor)), nil)
			if r.Status != 200 {
				readErr = fmt.Errorf("%d %s", r.Status, r.Raw)
				return
			}
			for _, e := range r.Body["events"].([]any) {
				ev := e.(J)
				if seen[ev["id"].(string)] {
					readErr = fmt.Errorf("duplicate event %v within one cursor chain", ev["id"])
				}
				seen[ev["id"].(string)] = true
			}
			cursor = r.Body["next_cursor"].(string)
		}
	}()
	for _, w := range writers {
		wg.Add(1)
		go func(w *actorClient) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				m := f.reply(t, w, tid, fmt.Sprint(w.handle, i))
				created.Store(m["id"], true)
			}
		}(w)
	}
	wg.Wait()
	close(stop)
	<-readerDone
	if readErr != nil {
		t.Fatal(readErr)
	}
	rest, _ := f.events(t, reader, nil, cursor)
	for _, e := range rest {
		seen[e["id"].(string)] = true
	}
	all, _ := f.events(t, reader, url.Values{"type": {"message.created"}}, "")
	n := 0
	for _, e := range all {
		if !seen[e["id"].(string)] {
			t.Fatalf("event %v skipped by concurrent reader", e["id"])
		}
		n++
	}
	if n != 75 {
		t.Fatalf("want 75 message.created events, got %d", n)
	}
}

func TestCT014EnterAndLeaveFilteredView(t *testing.T) {
	f := newBoard(t)
	a := newActor(t, "view")
	f.grant(t, a, "*", "allow", "read", "post", "channel_manage", "label")
	th, root := f.post(t, a, "c", "t", "x")
	mid := root["id"].(string)
	reply := f.reply(t, a, th["id"].(string), "a reply")
	q := url.Values{"label": {"status:open"}}
	_, cur := f.events(t, a, q, "")
	set := func(v string) {
		a.must(200, a.do("PATCH", f.p("/messages/"+mid+"/labels"), J{"operations": []J{{"op": "set", "key": "status", "value": v}}}))
	}
	set("open")
	set("done")
	set("other") // never matched before or after: must not appear
	evs, _ := f.events(t, a, q, cur)
	got := types(evs)
	want := []string{"message.labels_changed", "thread.view_invalidated", "message.labels_changed", "thread.view_invalidated"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("enter/leave events %v, want %v", got, want)
	}
	// Root labels apply to replies for queries.
	set("open")
	thr := string(a.must(200, a.do("GET", f.p("/threads?label=status:open"), nil)).Raw)
	if !strings.Contains(thr, th["id"].(string)) {
		t.Fatal("thread list root-label filter")
	}
	snap := a.must(201, a.do("POST", f.p("/snapshots?label=status:open"), nil)).Body
	page := string(a.must(200, a.do("GET", f.p("/snapshots/"+snap["id"].(string)), nil)).Raw)
	if !strings.Contains(page, reply["id"].(string)) {
		t.Fatal("root labels must classify replies in snapshots")
	}
}

func TestCT015SnapshotThenReplay(t *testing.T) {
	f := newBoard(t)
	f.grant(t, nil, "*", "allow", "read", "post", "channel_manage")
	w, r := newActor(t, "sw"), newActor(t, "sr")
	th, _ := f.post(t, w, "c", "t", "root")
	tid := th["id"].(string)
	for i := 0; i < 20; i++ {
		f.reply(t, w, tid, fmt.Sprint("pre", i))
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 40; i++ {
			f.reply(t, w, tid, fmt.Sprint("during", i))
		}
	}()
	view := map[string]bool{}
	snap := r.must(201, r.do("POST", f.p("/snapshots"), nil)).Body
	sid := snap["id"].(string)
	token, cont := "", ""
	for {
		q := "?limit=5"
		if token != "" {
			q += "&page_token=" + url.QueryEscape(token)
		}
		pg := r.must(200, r.do("GET", f.p("/snapshots/"+sid+q), nil)).Body
		for _, it := range pg["items"].([]any) {
			if it.(J)["type"] == "message" {
				view[it.(J)["id"].(string)] = true
			}
		}
		if pg["next_page_token"] == nil {
			cont = pg["continuation_cursor"].(string)
			break
		}
		token = pg["next_page_token"].(string)
	}
	<-done
	evs, _ := f.events(t, r, nil, cont)
	for _, e := range evs {
		if m, ok := e["payload"].(J)["message"].(J); ok {
			view[m["id"].(string)] = true
		}
	}
	msgs := r.must(200, r.do("GET", f.p("/threads/"+tid+"/messages?limit=200"), nil)).Body["messages"].([]any)
	if len(msgs) != 61 || len(view) != 61 {
		t.Fatalf("local view %d messages, server %d", len(view), len(msgs))
	}
	for _, m := range msgs {
		if !view[m.(J)["id"].(string)] {
			t.Fatalf("gap: %v missing from snapshot+replay", m.(J)["id"])
		}
	}
	r.must(204, r.do("DELETE", f.p("/snapshots/"+sid), nil))
	r.wantProblem(404, "not_found", r.do("GET", f.p("/snapshots/"+sid), nil))
}

func TestCT016RevocationDuringWaitAndSnapshot(t *testing.T) {
	f := newBoard(t)
	a := newActor(t, "rv")
	f.grant(t, a, "general", "allow", "read", "post", "channel_manage")
	g2 := f.grant(t, a, "other", "allow", "read", "post", "channel_manage")
	f.post(t, a, "general", "t", "x")
	f.post(t, a, "other", "t", "y")

	// Policy change during a long poll yields an explicit resync.
	_, cur := f.events(t, a, nil, "")
	res := make(chan resp, 1)
	go func() { res <- a.do("GET", f.p("/events?wait_ms=20000&cursor="+url.QueryEscape(cur)), nil) }()
	time.Sleep(500 * time.Millisecond)
	f.owner.must(200, f.owner.do("DELETE", f.p("/grants/"+g2), nil))
	select {
	case r := <-res:
		a.wantProblem(409, "resync_required", r)
	case <-time.After(15 * time.Second):
		t.Fatal("wait did not observe the policy change")
	}
	// The old cursor also requires resync.
	a.wantProblem(409, "resync_required", a.do("GET", f.p("/events?cursor="+url.QueryEscape(cur)), nil))

	// Snapshot sessions are invalidated by access changes.
	snap := a.must(201, a.do("POST", f.p("/snapshots"), nil)).Body
	f.grant(t, a, "third", "allow", "read")
	a.wantProblem(409, "resync_required", a.do("GET", f.p("/snapshots/"+snap["id"].(string)), nil))

	// Credential revocation ends a wait with 401.
	c2 := credential(t, a.id, "*")
	cur2 := c2.must(200, c2.do("GET", f.p("/events?from=latest"), nil)).Body["next_cursor"].(string)
	go func() { res <- c2.do("GET", f.p("/events?wait_ms=20000&cursor="+url.QueryEscape(cur2)), nil) }()
	time.Sleep(500 * time.Millisecond)
	me := c2.must(200, c2.do("GET", "/v1/me", nil)).Body
	admin(t).must(200, admin(t).do("DELETE", "/v1/credentials/"+me["credential"].(J)["id"].(string), nil))
	select {
	case r := <-res:
		c2.wantProblem(401, "unauthenticated", r)
	case <-time.After(15 * time.Second):
		t.Fatal("wait did not observe revocation")
	}
}

func TestCT017RedactionScrubsHistory(t *testing.T) {
	f := newBoard(t)
	a, reader := newActor(t, "red"), newActor(t, "rdr")
	f.grant(t, nil, "*", "allow", "read", "post", "channel_manage", "label")
	f.grant(t, f.owner, "*", "allow", "read")
	ks := "red-" + rnd()
	k := idem(ks)
	th := a.must(201, a.do("POST", f.p("/threads"), J{"channel": "c", "title": "t", "text": "SECRET-BODY"}, k)).Body
	tid := th["thread"].(J)["id"].(string)
	m := a.must(201, a.do("POST", f.p("/threads/"+tid+"/messages"), J{"text": "REPLY-SECRET @" + reader.handle}, idem(ks+"r"))).Body
	_ = m
	mid := m["id"].(string)
	a.must(200, a.do("PATCH", f.p("/messages/"+mid), J{"text": "EDITED-SECRET"}, ifMatch(1)))
	snap := reader.must(201, reader.do("POST", f.p("/snapshots"), nil)).Body

	// Redaction by a board admin needs a reason.
	f.owner.wantProblem(400, "validation_failed", f.owner.do("DELETE", f.p("/messages/"+mid), J{}, ifMatch(2)))
	del := f.owner.must(200, f.owner.do("DELETE", f.p("/messages/"+mid), J{"reason": "pii"}, ifMatch(2))).Body
	if del["text"] != nil || del["deleted"] == nil {
		t.Fatalf("tombstone: %v", del)
	}
	// Author deletes own root.
	rootID := th["root_message"].(J)["id"].(string)
	a.must(200, a.do("DELETE", f.p("/messages/"+rootID), nil, ifMatch(1)))

	check := func(where, raw string) {
		t.Helper()
		for _, s := range []string{"SECRET-BODY", "REPLY-SECRET", "EDITED-SECRET"} {
			if strings.Contains(raw, s) {
				t.Fatalf("%s reveals redacted text %q: %s", where, s, raw)
			}
		}
	}
	evs, _ := f.events(t, reader, nil, "")
	b, _ := json.Marshal(evs)
	check("event replay", string(b))
	check("text filter", string(reader.must(200, reader.do("GET", f.p("/events?text=SECRET"), nil)).Raw))
	// Only redaction invalidations (which bypass content filters by design)
	// may match; no historical event may match on its erased text.
	for _, e := range reader.must(200, reader.do("GET", f.p("/events?text=SECRET"), nil)).Body["events"].([]any) {
		if typ := e.(J)["type"]; typ != "message.deleted" {
			t.Fatalf("historical text filter matched redacted %v event", typ)
		}
	}
	check("snapshot captured before redaction", string(reader.must(200, reader.do("GET", f.p("/snapshots/"+snap["id"].(string)), nil)).Raw))
	replay := a.must(201, a.do("POST", f.p("/threads"), J{"channel": "c", "title": "t", "text": "SECRET-BODY"}, k))
	if replay.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatal("expected replay")
	}
	check("idempotency replay", string(replay.Raw))
	check("thread read", string(reader.must(200, reader.do("GET", f.p("/threads/"+tid+"/messages"), nil)).Raw))
	// Invalidation reaches content-filtered consumers.
	inv := reader.must(200, reader.do("GET", f.p("/events?text=zzz-no-match"), nil)).Body["events"].([]any)
	if len(inv) != 2 || inv[0].(J)["type"] != "message.deleted" {
		t.Fatalf("redaction invalidation must bypass content filters: %v", types(toJ(inv)))
	}
	// Audit trail records the administrative redaction.
	if !strings.Contains(string(f.owner.must(200, f.owner.do("GET", f.p("/audit-events"), nil)).Raw), "message.redact") {
		t.Fatal("redaction must be audited")
	}
}

func toJ(a []any) []J {
	out := make([]J, len(a))
	for i, x := range a {
		out[i] = x.(J)
	}
	return out
}

func TestCT018CursorRules(t *testing.T) {
	f, f2 := newBoard(t), newBoard(t)
	a := newActor(t, "cur")
	f.grant(t, a, "*", "allow", "read", "post", "channel_manage")
	f2.grant(t, a, "*", "allow", "read", "post", "channel_manage")
	f.post(t, a, "c", "t", "x")
	_, cur := f.events(t, a, nil, "")
	// Bound to board and query.
	a.wantProblem(400, "validation_failed", a.do("GET", f2.p("/events?cursor="+url.QueryEscape(cur)), nil))
	a.wantProblem(400, "validation_failed", a.do("GET", f.p("/events?channel=c&cursor="+url.QueryEscape(cur)), nil))
	// Never silently skip: resuming returns nothing new, then the next write.
	r := a.must(200, a.do("GET", f.p("/events?cursor="+url.QueryEscape(cur)), nil)).Body
	if len(r["events"].([]any)) != 0 || r["caught_up"] != true {
		t.Fatalf("resume: %v", r)
	}
}

func TestPAR012WaitSemantics(t *testing.T) {
	f := newBoard(t)
	a, b := newActor(t, "wa"), newActor(t, "wb")
	f.grant(t, nil, "*", "allow", "read", "post", "channel_manage")
	th, _ := f.post(t, a, "c", "t", "x")
	tid := th["id"].(string)
	_, cur := f.events(t, b, url.Values{"thread_id": {tid}}, "")
	// A reply that lands between the read and the wait is still observed.
	f.reply(t, a, tid, "between")
	r := b.must(200, b.do("GET", f.p("/events?limit=1&wait_ms=5000&thread_id="+tid+"&cursor="+url.QueryEscape(cur)), nil)).Body
	if len(r["events"].([]any)) != 1 {
		t.Fatal("reply between read and wait was missed")
	}
	cur = r["next_cursor"].(string)
	// Timeout is a successful empty response with a continuation.
	start := time.Now()
	r = b.must(200, b.do("GET", f.p("/events?wait_ms=1000&thread_id="+tid+"&cursor="+url.QueryEscape(cur)), nil)).Body
	if r["timed_out"] != true || len(r["events"].([]any)) != 0 || time.Since(start) < 900*time.Millisecond {
		t.Fatalf("timeout response: %v", r)
	}
	// A waiting client wakes on a new matching event.
	res := make(chan J, 1)
	go func() {
		res <- b.must(200, b.do("GET", f.p("/events?wait_ms=20000&thread_id="+tid+"&cursor="+url.QueryEscape(r["next_cursor"].(string))), nil)).Body
	}()
	time.Sleep(300 * time.Millisecond)
	f.reply(t, a, tid, "wake")
	select {
	case got := <-res:
		if len(got["events"].([]any)) == 0 {
			t.Fatal("woke without event")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("wait did not wake")
	}
}

func TestMentions(t *testing.T) { // PAR-009
	f := newBoard(t)
	a, b, hidden := newActor(t, "ma"), newActor(t, "mb"), newActor(t, "mh")
	f.grant(t, a, "*", "allow", "read", "post", "channel_manage")
	f.grant(t, b, "*", "allow", "read")
	f.grant(t, hidden, "elsewhere", "allow", "read")
	_, root := f.post(t, a, "c", "t", fmt.Sprintf("hi @%s and @%s and @nobody%s", b.handle, hidden.handle, rnd()))
	ms := fmt.Sprint(root["mentions"])
	if !strings.Contains(ms, b.id) || strings.Contains(ms, hidden.id) {
		t.Fatalf("mentions %v: want only readers of the channel", ms)
	}
	evs, _ := f.events(t, b, url.Values{"mention": {b.id}}, "")
	if len(evs) != 1 {
		t.Fatalf("mention filter: %d events", len(evs))
	}
	a.wantProblem(400, "validation_failed", a.do("POST", f.p("/threads"), J{"channel": "c", "title": "t", "text": "x", "mentions": []string{hidden.id}}, newKey()))
}

func TestReadOnlyCredential(t *testing.T) { // AUTH-020
	f := newBoard(t)
	a := newActor(t, "ro")
	f.grant(t, a, "*", "allow", "read", "post", "channel_manage")
	f.post(t, a, "c", "t", "x")
	ro := credential(t, a.id, "read")
	ro.must(200, ro.do("GET", f.p("/threads"), nil))
	ro.wantProblem(403, "forbidden", ro.do("POST", f.p("/threads"), J{"channel": "c", "title": "t", "text": "x"}, newKey()))
}
