package conformance

// EVT-013 / TASK-086: message.* event payloads carry the thread context at
// commit (thread_title; with abby.p2.tasks also task_id). Written from
// spec/protocol.md and spec/p2-tasks.md only.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// advertisesP2 reports whether GET /v1/server lists abby.p2.tasks, without
// skipping the test.
func advertisesP2(t *testing.T) bool {
	t.Helper()
	res, err := httpc.Get(baseURL + "/v1/server")
	if err != nil {
		t.Fatalf("GET /v1/server: %v", err)
	}
	defer res.Body.Close()
	var d J
	if err := json.NewDecoder(res.Body).Decode(&d); err != nil {
		t.Fatalf("GET /v1/server: %v", err)
	}
	ps, _ := obj(d, "protocol")["profiles"].([]any)
	for _, p := range ps {
		if p == p2Profile {
			return true
		}
	}
	return false
}

// messageEvents returns the message.* events of one thread, in stream order.
func messageEvents(evs []J, thread string) []J {
	var out []J
	for _, e := range evs {
		if strings.HasPrefix(str(e, "type"), "message.") && e["thread_id"] == thread {
			out = append(out, e)
		}
	}
	return out
}

// wantContext checks thread_title, and task_id when the server has P2.
// task is nil for "must be null".
func wantContext(t *testing.T, step string, e J, title string, p2 bool, task any) {
	t.Helper()
	pl := obj(e, "payload")
	if pl["thread_title"] != title {
		t.Errorf("%s: %s payload thread_title = %v, want %q", step, str(e, "type"), pl["thread_title"], title)
	}
	if !p2 {
		return
	}
	got, ok := pl["task_id"]
	if !ok {
		t.Errorf("%s: %s payload has no task_id (TASK-086)", step, str(e, "type"))
		return
	}
	if got != task {
		t.Errorf("%s: %s payload task_id = %v, want %v", step, str(e, "type"), got, task)
	}
}

func TestEVT013MessageEventsCarryThreadTitle(t *testing.T) { // EVT-013, TASK-086, RED-001
	skipIfUnconfigured(t)
	p2 := advertisesP2(t)
	f := newBoard(t)
	a := newActor(t, "ttl")
	f.grant(t, a, "*", "allow", "read", "post", "label", "channel_manage")
	f.grant(t, f.owner, "*", "allow", "read")

	secret := "TITLE-CTX-SECRET-" + rnd()
	th, _ := f.post(t, a, "c", "Title A", "root")
	tid := str(th, "id")
	r1 := f.reply(t, a, tid, secret)
	r1id := str(r1, "id")
	a.must(200, a.do("PATCH", f.p("/messages/"+r1id), J{"text": secret + " v2"}, ifMatch(1)))
	a.must(200, a.do("PATCH", f.p("/messages/"+r1id+"/labels"), J{"operations": []J{{"op": "set", "key": "status", "value": "open"}}}))
	gt := a.must(200, a.do("GET", f.p("/threads/"+tid), nil))
	a.must(200, a.do("PATCH", f.p("/threads/"+tid), J{"title": "Title B"}, func(r *http.Request) { r.Header.Set("If-Match", gt.Header.Get("ETag")) }))
	r2 := f.reply(t, a, tid, "after rename")
	a.must(200, a.do("DELETE", f.p("/messages/"+r1id), nil, ifMatch(3)))
	f.owner.must(200, f.owner.do("DELETE", f.p("/messages/"+str(r2, "id")), J{"reason": "pii"}, ifMatch(1)))

	evs, _ := f.events(t, f.owner, nil, "")
	msgs := messageEvents(evs, tid)
	want := []struct{ typ, title string }{
		{"message.created", "Title A"},
		{"message.edited", "Title A"},
		{"message.labels_changed", "Title A"},
		{"message.created", "Title B"},
		{"message.deleted", "Title B"},
		{"message.deleted", "Title B"},
	}
	if len(msgs) != len(want) {
		t.Fatalf("message events of the thread: %v, want %d", types(msgs), len(want))
	}
	for i, w := range want {
		if str(msgs[i], "type") != w.typ {
			t.Fatalf("message event %d: %s, want %s (%v)", i, str(msgs[i], "type"), w.typ, types(msgs))
		}
		// A later title change does not rewrite earlier events; deletion
		// scrubs the message but keeps the thread context (RED-001).
		wantContext(t, "plain thread", msgs[i], w.title, p2, nil)
	}
	if raw := eventsJSON(evs); strings.Contains(raw, secret) {
		t.Fatalf("event history still holds deleted text: %s", raw)
	}
	if pl := obj(msgs[0], "payload"); obj(pl, "message")["text"] != nil || obj(pl, "message")["deleted"] == nil {
		t.Fatalf("message.created of a deleted message not scrubbed: %v", pl)
	}
	// The text filter never matches the erased text of a message event; only
	// the deletion invalidations come through (EVT-007). Thread-scoped events
	// (thread.updated) are not excluded by content clauses at all.
	for _, e := range f.owner.must(200, f.owner.do("GET", f.p("/events?text="+secret), nil)).Body["events"].([]any) {
		if typ := str(e.(J), "type"); strings.HasPrefix(typ, "message.") && typ != "message.deleted" {
			t.Fatalf("text filter matched a %s event of a deleted message", typ)
		}
	}
}

func TestTASK086MessageEventsCarryTaskID(t *testing.T) { // TASK-086, EVT-013, BRIEF-021
	f := newP2Board(t)
	m := f.manager(t)

	// A task created with its thread: replies, brief updates, and the root
	// redaction all carry task_id and the current title.
	tk := f.createTask(t, m, "ctx", "Task one", "brief", nil)
	id, tid := str(tk, "id"), str(tk, "thread_id")
	f.reply(t, m, tid, "note 1")
	v := f.getTask(t, m, id)
	m.must(200, m.do("PATCH", f.tp(id), J{"title": "Task one v2"}, ifMatch(v["revision"])))
	f.reply(t, m, tid, "note 2")
	v = f.getTask(t, m, id)
	m.must(201, f.postBrief(m, id, "brief 2", "more", ifMatch(v["revision"])))
	rid := str(obj(m.must(200, m.do("GET", f.p("/threads/"+tid), nil)).Body, "root_message"), "id")
	rootRev := m.must(200, m.do("GET", f.p("/messages/"+rid), nil)).Body["revision"]
	m.must(200, m.do("POST", f.tp(id, "/root-redaction"), reason("pii"), newKey(), ifMatch(rootRev)))

	evs, _ := f.events(t, m, nil, "")
	msgs := messageEvents(evs, tid)
	want := []struct{ typ, title string }{
		{"message.created", "Task one"},    // reply
		{"message.created", "Task one v2"}, // reply after L19 title change
		{"message.created", "Task one v2"}, // brief update (L17)
		{"message.deleted", "Task one v2"}, // root redaction (L18)
	}
	if len(msgs) != len(want) {
		t.Fatalf("message events of the task thread: %v, want %d", types(msgs), len(want))
	}
	for i, w := range want {
		if str(msgs[i], "type") != w.typ {
			t.Fatalf("message event %d: %s, want %s (%v)", i, str(msgs[i], "type"), w.typ, types(msgs))
		}
		wantContext(t, "task thread", msgs[i], w.title, true, id)
	}

	// A converted thread: events before the conversion keep null.
	th, _ := f.post(t, m, "ctx", "Plan", "root")
	ptid := str(th, "id")
	f.reply(t, m, ptid, "before")
	conv := m.must(201, m.do("POST", f.p("/tasks"), J{"thread_id": ptid}, newKey())).Body
	f.reply(t, m, ptid, "after")
	evs, _ = f.events(t, m, nil, "")
	msgs = messageEvents(evs, ptid)
	if len(msgs) != 2 {
		t.Fatalf("message events of the converted thread: %v, want 2", types(msgs))
	}
	wantContext(t, "before conversion", msgs[0], "Plan", true, nil)
	wantContext(t, "after conversion", msgs[1], "Plan", true, str(conv, "id"))
}
