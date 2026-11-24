package conformance

import (
	"encoding/json"
	"fmt"
	"net/url"
	"testing"
	"time"
)

func TestPresenceHeartbeatReplayAndReadOnlyAccess(t *testing.T) {
	f, w, r := inboxFixture(t)
	discovery := w.must(200, w.do("GET", "/v1/server", nil)).Body
	limits := discovery["limits"].(J)
	for key, want := range map[string]float64{"presence_ttl_default_seconds": 120, "presence_ttl_min_seconds": 15, "presence_ttl_max_seconds": 300} {
		if limits[key] != want {
			t.Fatalf("discovery %s=%v, want %v", key, limits[key], want)
		}
	}
	f.post(t, w, "work", "presence", "hello")
	path := f.p("/actors/" + w.id + "/presence")
	unseen := r.must(200, r.do("GET", path, nil)).Body
	if unseen["state"] != "offline" || unseen["revision"] != float64(0) || unseen["last_seen_at"] != nil || unseen["expires_at"] != nil {
		t.Fatalf("unseen presence: %v", unseen)
	}
	presenceValidate(t, "ActorPresence", unseen)
	key := "presence-" + rnd()
	first := w.must(200, w.do("POST", f.p("/presence"), J{"state": "online"}, idem(key)))
	if first.Body["state"] != "online" || first.Body["actor_id"] != w.id {
		t.Fatalf("heartbeat: %v", first.Body)
	}
	presenceValidate(t, "ActorPresence", first.Body)
	w.must(200, w.do("POST", f.p("/presence"), J{"state": "away", "ttl_seconds": 15}, newKey()))
	replay := w.must(200, w.do("POST", f.p("/presence"), J{"state": "online"}, idem(key)))
	if replay.Header.Get("Idempotent-Replayed") != "true" || replay.Body["state"] != "away" || replay.Body["revision"] != float64(2) {
		t.Fatalf("replay reset presence: %v", replay)
	}
	current := r.must(200, r.do("GET", path, nil)).Body
	if current["expires_at"] != replay.Body["expires_at"] {
		t.Fatal("replay refreshed TTL")
	}
	for _, body := range []J{{"state": "offline", "ttl_seconds": 15}, {"state": "online", "ttl_seconds": 14}, {"state": "online", "ttl_seconds": 301}, {"state": "online", "actor_id": r.id}, {"state": "busy"}, {"state": "online", "ttl_seconds": nil}} {
		w.wantProblem(400, "validation_failed", w.do("POST", f.p("/presence"), body, newKey()))
	}
	r.wantProblem(403, "forbidden", r.do("POST", f.p("/presence"), J{"state": "online"}, newKey()))
	offline := w.must(200, w.do("POST", f.p("/presence"), J{"state": "offline"}, newKey())).Body
	if offline["expires_at"] != nil || offline["state"] != "offline" {
		t.Fatalf("offline TTL retained: %v", offline)
	}
}

func TestPresenceEventsRespectCurrentReadAndContentFilters(t *testing.T) {
	f, w, r := inboxFixture(t)
	thread, _ := f.post(t, w, "work", "presence events", "hello")
	w.must(200, w.do("POST", f.p("/presence"), J{"state": "online"}, newKey()))
	events := presenceEvents(t, f, r, "type=actor.presence_changed")
	if len(events) != 1 {
		t.Fatalf("presence event missing: %v", events)
	}
	ev := events[0]
	if ev["channel_id"] != nil || ev["thread_id"] != nil || ev["resource"].(J)["type"] != "actor_presence" {
		t.Fatalf("wrong envelope: %v", ev)
	}
	presenceValidate(t, "Event", ev)
	for _, filter := range []string{"channel=work", "subtree=work", "thread_id=" + thread["id"].(string), "author_id=" + w.id, "mention=" + w.id, "label=source", "text=online"} {
		page := presenceEvents(t, f, r, "type=actor.presence_changed&"+filter)
		if len(page) != 0 {
			t.Fatalf("presence leaked into content query %s: %v", filter, page)
		}
	}
	// Board ownership exposes metadata, not channel content or presence.
	f.owner.wantProblem(404, "not_found", f.owner.do("GET", f.p("/actors/"+w.id+"/presence"), nil))
	if len(presenceEvents(t, f, f.owner, "type=actor.presence_changed")) != 0 {
		t.Fatal("board ownership leaked presence without channel read")
	}
	outsider := newActor(t, "presenceoutside", "read")
	r.wantProblem(404, "not_found", r.do("GET", f.p("/actors/"+outsider.id+"/presence"), nil))
	postOnly := newActor(t, "postonly", "post")
	f.grant(t, postOnly, "*", "allow", "read", "post")
	postOnly.wantProblem(404, "not_found", postOnly.do("GET", f.p("/actors/"+w.id+"/presence"), nil))
	if len(presenceEvents(t, f, postOnly, "type=actor.presence_changed")) != 0 {
		t.Fatal("presence leaked outside credential read ceiling")
	}
	f.grant(t, r, "*", "deny", "read")
	r.wantProblem(404, "not_found", r.do("GET", f.p("/actors/"+w.id+"/presence"), nil))
	if len(presenceEvents(t, f, r, "type=actor.presence_changed")) != 0 {
		t.Fatal("revoked read leaked presence")
	}
}

func TestPresenceExpiryWithoutAnExtraRight(t *testing.T) {
	f, w, r := inboxFixture(t)
	f.post(t, w, "work", "expires", "hello")
	first := w.must(200, w.do("POST", f.p("/presence"), J{"state": "online", "ttl_seconds": 15}, newKey())).Body
	deadline, err := time.Parse(time.RFC3339Nano, first["expires_at"].(string))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(deadline) + 20*time.Millisecond)
	path := f.p("/actors/" + url.PathEscape(w.id) + "/presence")
	expired := r.must(200, r.do("GET", path, nil)).Body
	if expired["state"] != "offline" || expired["last_seen_at"] != first["last_seen_at"] || expired["expires_at"] != first["expires_at"] || expired["revision"] != float64(2) {
		t.Fatalf("expiry projection: %v", expired)
	}
	r.must(200, r.do("GET", path, nil))
	list := presenceEvents(t, f, r, "type=actor.presence_changed")
	if len(list) != 2 {
		t.Fatalf("expected heartbeat+one expiry: %v", list)
	}
	last := list[1]
	if last["effective_at"] != first["expires_at"] {
		t.Fatal(fmt.Sprint("expiry time wrong: ", last))
	}
}

func presenceValidate(t *testing.T, name string, v J) {
	t.Helper()
	raw, _ := json.Marshal(v)
	if err := schemaNamed(t, name).Validate(decodeInstance(t, raw)); err != nil {
		t.Fatal(err)
	}
}
func presenceEvents(t *testing.T, f *fixture, c *actorClient, q string) []J {
	t.Helper()
	query, err := url.ParseQuery(q)
	if err != nil {
		t.Fatal(err)
	}
	evs, _ := f.events(t, c, query, "")
	return evs
}
