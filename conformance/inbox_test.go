package conformance

import (
	"fmt"
	"net/url"
	"testing"
	"time"
)

func inboxFixture(t *testing.T) (*fixture, *actorClient, *actorClient) {
	t.Helper()
	f := newBoard(t)
	writer := newActor(t, "inwriter")
	reader := newActor(t, "inreader", "read")
	f.grant(t, writer, "*", "allow", "read", "post", "channel_manage")
	f.grant(t, reader, "*", "allow", "read")
	return f, writer, reader
}

func TestInboxAcknowledgmentReplayAndMentionDeduplication(t *testing.T) {
	f, w, r := inboxFixture(t)
	_, root := f.post(t, w, "work", "inbox", "Hello @"+r.handle)
	first := r.must(200, r.do("GET", f.p("/inbox"), nil)).Body
	if len(first["items"].([]any)) != 1 || first["unread_count"] != float64(1) {
		t.Fatalf("want one durable item: %v", first)
	}
	item := first["items"].([]any)[0].(J)
	if item["source_kind"] != "mention" || item["source_id"] != root["id"] || item["message_id"] != root["id"] {
		t.Fatalf("wrong source: %v", item)
	}
	second := r.must(200, r.do("GET", f.p("/inbox"), nil)).Body
	if second["items"].([]any)[0].(J)["id"] != item["id"] {
		t.Fatal("read changed item identity")
	}
	key := "ack-" + rnd()
	ack := J{"cursor": first["ack_cursor"]}
	r.must(200, r.do("POST", f.p("/inbox/ack"), ack, idem(key)))
	replay := r.must(200, r.do("POST", f.p("/inbox/ack"), ack, idem(key)))
	if replay.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatal("ack replay marker missing")
	}
	empty := r.must(200, r.do("GET", f.p("/inbox"), nil)).Body
	if len(empty["items"].([]any)) != 0 || empty["unread_count"] != float64(0) {
		t.Fatalf("ack did not consume prefix: %v", empty)
	}
	w.must(200, w.do("PATCH", f.p("/messages/"+root["id"].(string)), J{"text": "Removed mention"}, ifMatch(1)))
	w.must(200, w.do("PATCH", f.p("/messages/"+root["id"].(string)), J{"text": "Hello again @" + r.handle}, ifMatch(2)))
	after := r.must(200, r.do("GET", f.p("/inbox"), nil)).Body
	if len(after["items"].([]any)) != 0 {
		t.Fatalf("re-added mention duplicated acknowledged delivery: %v", after)
	}
	r.wantProblem(400, "validation_failed", r.do("POST", f.p("/inbox/ack"), J{"cursor": "i1.never-issued"}, newKey()))
}

func TestInboxLongPollAndPolicyRecheck(t *testing.T) {
	f, w, r := inboxFixture(t)
	thread, _ := f.post(t, w, "work", "wait", "No mention yet")
	initial := r.must(200, r.do("GET", f.p("/inbox"), nil)).Body
	done := make(chan resp, 1)
	go func() {
		done <- r.do("GET", f.p("/inbox?cursor="+url.QueryEscape(initial["next_cursor"].(string))+"&wait_ms=2000"), nil)
	}()
	time.Sleep(50 * time.Millisecond)
	f.reply(t, w, thread["id"].(string), "Wake @"+r.handle)
	select {
	case got := <-done:
		r.must(200, got)
		if len(got.Body["items"].([]any)) != 1 || got.Body["timed_out"] != false {
			t.Fatalf("wait did not deliver: %v", got.Body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("inbox wait did not wake")
	}
	page := r.must(200, r.do("GET", f.p("/inbox"), nil)).Body
	before := page["next_cursor"].(string)
	f.grant(t, r, "work", "deny", "read")
	r.wantProblem(409, "resync_required", r.do("GET", f.p("/inbox?cursor="+url.QueryEscape(before)), nil))
	hidden := r.must(200, r.do("GET", f.p("/inbox"), nil)).Body
	if len(hidden["items"].([]any)) != 0 || hidden["unread_count"] != float64(0) {
		t.Fatalf("inaccessible item leaked: %v", hidden)
	}
}

func TestInboxIssuedCursorBindingRedactionAndTimeout(t *testing.T) {
	f, w, r := inboxFixture(t)
	_, root := f.post(t, w, "work", "private item", "Hello @"+r.handle)
	page := r.must(200, r.do("GET", f.p("/inbox"), nil)).Body
	other := newActor(t, "inother", "read")
	f.grant(t, other, "*", "allow", "read")
	other.wantProblem(400, "validation_failed", other.do("GET", f.p("/inbox?cursor="+url.QueryEscape(page["next_cursor"].(string))), nil))
	w.must(200, w.do("DELETE", f.p("/messages/"+root["id"].(string)), nil, ifMatch(1)))
	hidden := r.must(200, r.do("GET", f.p("/inbox"), nil)).Body
	if len(hidden["items"].([]any)) != 0 || hidden["unread_count"] != float64(0) {
		t.Fatalf("redaction leaked inbox item: %v", hidden)
	}
	timeout := r.must(200, r.do("GET", f.p(fmt.Sprintf("/inbox?wait_ms=30&cursor=%s", url.QueryEscape(hidden["next_cursor"].(string)))), nil)).Body
	if timeout["timed_out"] != true || timeout["caught_up"] != true || len(timeout["items"].([]any)) != 0 {
		t.Fatalf("wrong timeout: %v", timeout)
	}
}

func TestInboxWaitRechecksPolicyAndRevokedCredential(t *testing.T) {
	for _, change := range []string{"policy", "credential"} {
		t.Run(change, func(t *testing.T) {
			f, w, r := inboxFixture(t)
			f.post(t, w, "work", "empty wait", "No mention")
			initial := r.must(200, r.do("GET", f.p("/inbox"), nil)).Body
			me := r.must(200, r.do("GET", "/v1/me", nil)).Body
			result := make(chan resp, 1)
			go func() {
				result <- r.do("GET", f.p("/inbox?cursor="+url.QueryEscape(initial["next_cursor"].(string))+"&wait_ms=5000"), nil)
			}()
			time.Sleep(100 * time.Millisecond)
			if change == "policy" {
				f.grant(t, r, "work", "deny", "read")
			} else {
				admin(t).must(200, admin(t).do("DELETE", "/v1/credentials/"+me["credential"].(J)["id"].(string), nil))
			}
			select {
			case got := <-result:
				if change == "policy" {
					r.wantProblem(409, "resync_required", got)
				} else {
					r.wantProblem(401, "unauthenticated", got)
				}
			case <-time.After(6 * time.Second):
				t.Fatal("wait did not recheck changed authorization")
			}
		})
	}
}

func TestInboxPaginationCountsAndHiddenAcknowledgment(t *testing.T) {
	f, w, r := inboxFixture(t)
	f.post(t, w, "visible", "one", "@"+r.handle)
	f.post(t, w, "hidden", "two", "@"+r.handle)
	f.post(t, w, "visible", "three", "@"+r.handle)
	first := r.must(200, r.do("GET", f.p("/inbox?limit=1"), nil)).Body
	if len(first["items"].([]any)) != 1 || first["unread_count"] != float64(3) || first["caught_up"] != false {
		t.Fatalf("wrong first page: %v", first)
	}
	next := r.must(200, r.do("GET", f.p("/inbox?limit=1&cursor="+url.QueryEscape(first["next_cursor"].(string))), nil)).Body
	if first["items"].([]any)[0].(J)["id"] == next["items"].([]any)[0].(J)["id"] {
		t.Fatal("continuation repeated item")
	}
	r.must(200, r.do("POST", f.p("/inbox/ack"), J{"cursor": first["ack_cursor"]}, newKey()))
	remaining := r.must(200, r.do("GET", f.p("/inbox"), nil)).Body
	if len(remaining["items"].([]any)) != 2 {
		t.Fatalf("prefix acknowledgment consumed later work: %v", remaining)
	}
	f.grant(t, r, "hidden", "deny", "read")
	visible := r.must(200, r.do("GET", f.p("/inbox"), nil)).Body
	if visible["unread_count"] != float64(1) || len(visible["items"].([]any)) != 1 {
		t.Fatalf("count included hidden work: %v", visible)
	}
	// This explicit prefix covers the hidden item too. It is allowed with a
	// read-only credential, because acknowledgment grants no execution rights.
	r.must(200, r.do("POST", f.p("/inbox/ack"), J{"cursor": visible["ack_cursor"]}, newKey()))
	empty := r.must(200, r.do("GET", f.p("/inbox"), nil)).Body
	if empty["unread_count"] != float64(0) || len(empty["items"].([]any)) != 0 {
		t.Fatalf("explicit acknowledgment did not cover hidden prefix: %v", empty)
	}
}

func TestInboxRejectsUnknownParametersAndUnissuedAcknowledgments(t *testing.T) {
	f, w, r := inboxFixture(t)
	f.post(t, w, "work", "validation", "@"+r.handle)
	for _, query := range []string{"actor_id=someone", "limit=0", "limit=1&limit=2", "wait_ms=9223372036854775807", "cursor=i1.forged"} {
		r.wantProblem(400, "validation_failed", r.do("GET", f.p("/inbox?"+query), nil))
	}
	r.wantProblem(400, "validation_failed", r.do("POST", f.p("/inbox/ack"), J{"cursor": "i1.forged", "actor_id": "someone"}, newKey()))
	r.wantProblem(400, "validation_failed", r.do("POST", f.p("/inbox/ack"), J{}, newKey()))
	page := r.must(200, r.do("GET", f.p("/inbox"), nil)).Body
	r.wantProblem(400, "validation_failed", r.do("POST", f.p("/inbox/ack"), J{"cursor": page["ack_cursor"]}))
}
