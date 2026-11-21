package conformance

import (
	"net/http"
	"testing"
)

// CT-OPR-GATE, CEILING and NOCORS exercise only direct credentials. Delegated
// actor enforcement belongs to the separately delivered delegation runtime.
func TestOperatorSecurityContract(t *testing.T) {
	f := newP2Board(t)
	m := f.manager(t)
	a := admin(t)
	human := a.must(201, a.do("POST", "/v1/actors", J{"kind": "human", "handle": "person" + rnd()}, newKey())).Body
	id := str(human, "id")
	cr := a.must(201, a.do("POST", "/v1/credentials", J{"actor_id": id, "label": "operator-session", "scopes": []string{"read", "post", "label", "claim", "task_manage", "task_admin"}, "board_ids": []string{f.board}, "expires_in_seconds": 28800, "may_manage_credentials": false, "may_delegate": false}, newKey())).Body
	op := &actorClient{t: t, id: id, token: str(cr, "token")}
	cid := str(obj(cr, "credential"), "id")
	f.grant(t, op, "work", "allow", "read", "post", "label", "claim", "task_manage")
	gate := f.createTask(t, m, "work", "approval", "brief", J{"direct_action_required": true})
	tid := str(gate, "id")
	f.mustCmd(t, m, tid, "block", J{"reason": "waiting for a person"})
	gate = f.getTask(t, op, tid)
	thread := obj(m.must(200, m.do("GET", f.p("/threads/"+str(gate, "thread_id")), nil)).Body, "thread")
	labels := f.p("/messages/" + str(thread, "root_message_id") + "/labels")
	op.must(200, op.do("PATCH", labels, J{"operations": []any{J{"op": "set", "key": "gate", "value": "approved"}}}))
	if f.getTask(t, op, tid)["state"] != "blocked" {
		t.Fatal("display label changed gate state")
	}
	op.wantProblem(403, "forbidden", op.do("POST", f.tp(tid)+"/unblock", J{"reason": "gate approved"}, ifMatch(gate["revision"]), newKey()))
	f.grant(t, op, "work", "allow", "task_admin")
	cursor := f.head(t, op)
	key := newKey()
	first := op.must(200, op.do("POST", f.tp(tid)+"/unblock", J{"reason": "gate approved: reviewed"}, ifMatch(gate["revision"]), key))
	op.must(200, op.do("POST", f.tp(tid)+"/unblock", J{"reason": "gate approved: reviewed"}, ifMatch(gate["revision"]), key))
	if first.Body["direct_action_required"] != true {
		t.Fatal("approval removed the gate flag")
	}
	events, _ := f.since(t, op, cursor)
	count := 0
	for _, e := range events {
		if e["type"] == "task.unblocked" {
			count++
			if e["actor_id"] != id || e["credential_id"] != cid || e["on_behalf_of_actor_id"] != nil || obj(e, "payload")["reason"] != "gate approved: reviewed" {
				t.Fatal("approval provenance differs from credential")
			}
		}
	}
	if count != 1 {
		t.Fatalf("unblock replay recorded %d events", count)
	}
	credentialProblem(t, 403, "credential_restricted", op.do("POST", "/v1/credentials", J{"scopes": []string{"read"}}, newKey()))
	credentialProblem(t, 403, "credential_restricted", op.do("GET", "/v1/credentials", nil))
	credentialProblem(t, 403, "credential_restricted", op.do("DELETE", "/v1/credentials/cred_other", nil))
	op.must(200, op.do("DELETE", "/v1/credentials/"+cid, nil))
	credentialProblem(t, 401, "unauthenticated", op.do("GET", "/v1/me", nil))
	req, _ := http.NewRequest("OPTIONS", baseURL+f.p("/tasks"), nil)
	req.Header.Set("Origin", "https://foreign.test")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Authorization")
	res, err := httpc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.Header.Get("Access-Control-Allow-Origin") != "" || res.Header.Get("Access-Control-Allow-Headers") != "" || res.Header.Get("Access-Control-Allow-Methods") != "" {
		t.Fatal("API enabled foreign browser writes")
	}
}
