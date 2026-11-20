package conformance

import (
	"encoding/json"
	"testing"
)

// DLG-090 and the direct-credential portion of DLG-099 ship before delegation.
func TestGateFieldDirectLifecycle(t *testing.T) {
	f := newP2Board(t)
	m := f.manager(t)
	w := f.worker(t, "gateworker")
	plain := f.createTask(t, m, "work", "plain", "brief", nil)
	if plain["direct_action_required"] != false {
		t.Fatal("default must be explicit false")
	}
	gate := f.createTask(t, m, "work", "gate", "brief", J{"direct_action_required": true})
	id := str(gate, "id")
	cursor := f.head(t, m)
	if f.getTask(t, m, id)["direct_action_required"] != true {
		t.Fatal("flag not persisted")
	}
	m.must(200, m.do("PATCH", f.tp(id), J{"direct_action_required": false}, ifMatch(gate["revision"])))
	gate = f.getTask(t, m, id)
	w.must(403, w.do("PATCH", f.tp(id), J{"direct_action_required": true}, ifMatch(gate["revision"])))
	gate = m.must(200, m.do("PATCH", f.tp(id), J{"direct_action_required": true}, ifMatch(gate["revision"]))).Body

	updated, _ := f.since(t, m, cursor)
	for _, ev := range updated {
		if err := schemaNamed(t, "Event").Validate(ev); err != nil {
			t.Fatalf("gate update schema: %v", err)
		}
	}
	cursor = f.head(t, m)
	f.mustCmd(t, m, id, "cancel", J{"reason": "cancel"})
	reopened := f.mustCmd(t, m, id, "reopen", nil)
	if reopened["state"] != "blocked" || reopened["direct_action_required"] != true || obj(reopened, "block")["reason"] != "direct action required" {
		t.Fatalf("reopen bypass: %v", reopened)
	}

	evs, _ := f.since(t, m, cursor)
	wantTypes(t, "cancel then flagged reopen", evs, "task.canceled", "thread.view_invalidated", "task.reopened", "task.blocked", "thread.view_invalidated")
	for _, ev := range evs {
		if err := schemaNamed(t, "Event").Validate(ev); err != nil {
			t.Fatalf("gate reopen schema: %v", err)
		}
	}
	w.must(409, f.claimResp(w, id, nil))
	f.mustCmd(t, m, id, "unblock", nil)
	if f.getTask(t, m, id)["direct_action_required"] != true {
		t.Fatal("unblock cleared flag")
	}
	for _, value := range []any{"true", 1, nil} {
		m.must(400, m.do("PATCH", f.tp(id), J{"direct_action_required": value}, ifMatch(f.getTask(t, m, id)["revision"])))
	}
}

func TestGateFieldBatchConvertAndSnapshot(t *testing.T) {
	f := newP2Board(t)
	m := f.manager(t)
	batch := m.must(201, m.do("POST", f.p("/tasks/batch"), J{"tasks": []any{
		J{"ref": "gate", "thread": J{"channel": "work", "title": "gate", "text": "brief"}, "direct_action_required": true},
		J{"ref": "plain", "thread": J{"channel": "work", "title": "plain", "text": "brief"}, "direct_action_required": false},
	}}, newKey())).Body
	entries := arr(batch, "tasks")
	gate := obj(entries[0].(J), "task")
	plain := obj(entries[1].(J), "task")
	if gate["direct_action_required"] != true || plain["direct_action_required"] != false {
		t.Fatal("batch flags lost")
	}
	th, _ := f.post(t, m, "work", "convert", "brief")
	converted := m.must(201, m.do("POST", f.p("/tasks"), J{"thread_id": str(th, "id"), "direct_action_required": true}, newKey())).Body
	if converted["direct_action_required"] != true {
		t.Fatal("convert flag lost")
	}
	list := m.must(200, m.do("GET", f.p("/tasks"), nil)).Body
	if len(arr(list, "items")) != 3 {
		t.Fatal("list count")
	}
	for _, raw := range arr(list, "items") {
		task := raw.(J)
		if _, ok := task["direct_action_required"].(bool); !ok {
			t.Fatal("list omitted flag")
		}
	}
	items, _, _ := f.snapshot(t, m, "")
	count := 0
	for _, it := range items {
		if it["type"] == "task" {
			count++
			v := obj(it, "data")
			if v["direct_action_required"] != (str(v, "id") != str(plain, "id")) {
				t.Fatalf("snapshot flag: %v", v)
			}
		}
	}
	if count != 3 {
		t.Fatal("snapshot count")
	}
}

func TestGateFieldParserBooleanValidation(t *testing.T) {
	f := newP2Board(t)
	m := f.manager(t)
	task := f.createTask(t, m, "work", "original", "brief", J{"direct_action_required": true})
	id := str(task, "id")
	th, _ := f.post(t, m, "work", "convert", "brief")
	for _, fields := range []string{
		`"DIRECT_ACTION_REQUIRED":null`,
		`"Direct_Action_Required":null`,
		`"direct_action_required":null,"direct_action_required":true`,
		`"DIRECT_ACTION_REQUIRED":null,"direct_action_required":true`,
		`"direct_action_required":true,"DIRECT_ACTION_REQUIRED":null`,
		`"direct_action_required":true,"Direct_Action_Required":null,"direct_action_required":false`,
	} {
		m.must(400, m.do("POST", f.p("/tasks"), json.RawMessage(`{"thread":{"channel":"work","title":"gate","text":"brief"},`+fields+`}`), newKey()))
		m.must(400, m.do("POST", f.p("/tasks"), json.RawMessage(`{"thread_id":"`+str(th, "id")+`",`+fields+`}`), newKey()))
		m.must(400, m.do("POST", f.p("/tasks/batch"), json.RawMessage(`{"TaſKs":[{"ref":"gate","thread":{"channel":"work","title":"gate","text":"brief"},`+fields+`}]}`), newKey()))
		m.must(400, m.do("PATCH", f.tp(id), json.RawMessage(`{"title":"should not commit",`+fields+`}`), ifMatch(task["revision"])))
	}
	got := f.getTask(t, m, id)
	if got["title"] != "original" || got["revision"] != task["revision"] || got["direct_action_required"] != true {
		t.Fatal("invalid parser input changed task")
	}
	page := m.must(200, m.do("GET", f.p("/tasks"), nil)).Body
	if len(arr(page, "items")) != 1 {
		t.Fatal("invalid parser input created tasks")
	}
	// Valid duplicates keep the request decoder's last-value behavior.
	cleared := m.must(200, m.do("PATCH", f.tp(id), json.RawMessage(`{"DIRECT_ACTION_REQUIRED":true,"direct_action_required":false}`), ifMatch(task["revision"]))).Body
	if cleared["direct_action_required"] != false {
		t.Fatal("valid duplicate boolean order changed")
	}
}
