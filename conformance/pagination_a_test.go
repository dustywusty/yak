package conformance

// PAGE-001 for the board, channel, and participant lists:
// GET /v1/boards, GET /v1/boards/{id}/channels, GET /v1/boards/{id}/actors.
// Raw HTTP only. Each list is walked with pages of 1 and 2 items, also while
// other requests insert items, and every existing visible item must appear
// exactly once, in (created_at, id) order, with hidden items never returned.

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"testing"
)

// listPage fetches one page of a PAGE-001 list and checks its envelope.
// next is "" on the last page.
func listPage(c *actorClient, path, field, token string, limit int) (items []J, next string) {
	c.t.Helper()
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if token != "" {
		q.Set("page_token", token)
	}
	u := path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	body := c.must(200, c.do("GET", u, nil)).Body
	raw, ok := body[field].([]any)
	if !ok {
		c.t.Fatalf("GET %s: no %q array: %v", u, field, body)
	}
	for _, it := range raw {
		items = append(items, it.(J))
	}
	nt, present := body["next_page_token"]
	if !present {
		c.t.Fatalf("GET %s: next_page_token missing", u)
	}
	if nt != nil {
		s, ok := nt.(string)
		if !ok || s == "" {
			c.t.Fatalf("GET %s: next_page_token %v is not a non-empty string or null", u, nt)
		}
		next = s
		// Visibility filtering happens before the page is cut, so only the
		// last page may be short.
		if limit > 0 && len(items) != limit {
			c.t.Fatalf("GET %s: %d items on a non-final page", u, len(items))
		}
	}
	if limit > 0 && len(items) > limit {
		c.t.Fatalf("GET %s: %d items, limit %d", u, len(items), limit)
	}
	return items, next
}

// listAll follows next_page_token until it is null (limit 0: server default).
func listAll(c *actorClient, path, field string, limit int) []J {
	c.t.Helper()
	var all []J
	tok := ""
	for pages := 0; ; pages++ {
		if pages > 20000 {
			c.t.Fatalf("GET %s: pagination did not end", path)
		}
		items, next := listPage(c, path, field, tok, limit)
		all = append(all, items...)
		if next == "" {
			return all
		}
		tok = next
	}
}

func idsOf(items []J) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i], _ = it["id"].(string)
	}
	return out
}

// checkPaged asserts strict (created_at, id) order, no repeats, every id in
// want exactly once, and no id in hidden.
func checkPaged(t *testing.T, what string, items []J, want, hidden []string) {
	t.Helper()
	seen := map[string]int{}
	for i, it := range items {
		id, _ := it["id"].(string)
		seen[id]++
		if seen[id] > 1 {
			t.Fatalf("%s: %s repeated", what, id)
		}
		if i > 0 {
			prev := items[i-1]
			pt, ct := ts(t, prev["created_at"]), ts(t, it["created_at"])
			if ct.Before(pt) || (ct.Equal(pt) && id <= prev["id"].(string)) {
				t.Fatalf("%s: %s (%v) after %s (%v) breaks (created_at, id) order", what, id, it["created_at"], prev["id"], prev["created_at"])
			}
		}
	}
	for _, id := range want {
		if seen[id] != 1 {
			t.Fatalf("%s: %s seen %d times, want once; got %v", what, id, seen[id], idsOf(items))
		}
	}
	for _, id := range hidden {
		if seen[id] != 0 {
			t.Fatalf("%s: hidden item %s returned", what, id)
		}
	}
}

// background runs fn in a goroutine; failures are reported by wait.
type background struct {
	wg   sync.WaitGroup
	mu   sync.Mutex
	errs []string
}

func (b *background) run(fn func(fail func(string, ...any))) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		fn(func(f string, a ...any) {
			b.mu.Lock()
			b.errs = append(b.errs, fmt.Sprintf(f, a...))
			b.mu.Unlock()
		})
	}()
}

func (b *background) wait(t *testing.T) {
	t.Helper()
	b.wg.Wait()
	if len(b.errs) > 0 {
		t.Fatalf("concurrent inserts failed: %v", b.errs)
	}
}

func expect(fail func(string, ...any), status int, r resp, what string) J {
	if r.Status != status {
		fail("%s: want %d, got %d: %s", what, status, r.Status, r.Raw)
	}
	return r.Body
}

func grantBody(actorID, pattern string, caps ...string) J {
	return J{"subject": J{"type": "actor", "actor_id": actorID}, "pattern": pattern, "capabilities": caps, "effect": "allow"}
}

func TestPAGABoardsPaging(t *testing.T) {
	a := admin(t)
	x, y := newActor(t, "pgbx"), newActor(t, "pgby")
	mk := func(c *actorClient, owner string) resp {
		return c.do("POST", "/v1/boards", J{"name": "pg " + t.Name(), "owner_actor_id": owner}, newKey())
	}
	var mine, hidden, spare []string
	for i := 0; i < 5; i++ {
		mine = append(mine, a.must(201, mk(a, x.id)).Body["id"].(string))
		hidden = append(hidden, a.must(201, mk(a, y.id)).Body["id"].(string))
	}
	for i := 0; i < 3; i++ {
		spare = append(spare, a.must(201, mk(a, y.id)).Body["id"].(string))
	}
	// The listing of an unrestricted credential spans every board with an
	// any_actor grant on the server, so walk a board-restricted credential.
	scope := append(append(append([]string{}, mine...), hidden...), spare...)
	cr := a.must(201, a.do("POST", "/v1/credentials", J{"actor_id": x.id, "scopes": []string{"*"}, "board_ids": scope}, newKey())).Body
	rx := &actorClient{t: t, token: cr["token"].(string), id: x.id}

	for _, lim := range []int{1, 2} {
		checkPaged(t, fmt.Sprintf("boards limit %d", lim), listAll(rx, "/v1/boards", "boards", lim), mine, hidden)
	}

	// Concurrent inserts: new boards owned by x (outside the credential's
	// board_ids, so filtered) and grants that make spare boards visible.
	for i, lim := range []int{1, 2} {
		var bg background
		bg.run(func(fail func(string, ...any)) {
			for j := 0; j < 4; j++ {
				expect(fail, 201, mk(a, x.id), "create board")
			}
		})
		sb := spare[i]
		bg.run(func(fail func(string, ...any)) {
			expect(fail, 201, y.do("POST", "/v1/boards/"+sb+"/grants", grantBody(x.id, "*", "read"), newKey()), "grant spare")
		})
		got := listAll(rx, "/v1/boards", "boards", lim)
		bg.wait(t)
		checkPaged(t, fmt.Sprintf("boards limit %d, concurrent", lim), got, mine, hidden)
	}
	// After the grants settle, the newly visible boards are listed once.
	checkPaged(t, "boards after grants", listAll(rx, "/v1/boards", "boards", 1), append(append([]string{}, mine...), spare[:2]...), append(append([]string{}, hidden...), spare[2]))
}

func TestPAGAChannelsPaging(t *testing.T) {
	f := newBoard(t)
	f.grant(t, f.owner, "*", "allow", "read", "post", "channel_manage")
	r := newActor(t, "pgcr")
	f.grant(t, r, "pub/*", "allow", "read")
	mk := func(path string) resp {
		return f.owner.do("POST", f.p("/channels"), J{"path": path, "access_mode": "inherited"}, newKey())
	}
	var pub, priv []string
	for i := 0; i < 5; i++ {
		pub = append(pub, f.owner.must(201, mk(fmt.Sprintf("pub/c%d", i))).Body["id"].(string))
		if i < 3 {
			priv = append(priv, f.owner.must(201, mk(fmt.Sprintf("priv/c%d", i))).Body["id"].(string))
		}
	}
	for _, lim := range []int{1, 2} {
		checkPaged(t, fmt.Sprintf("reader channels limit %d", lim), listAll(r, f.p("/channels"), "channels", lim), pub, priv)
		// A board administrator sees every channel.
		checkPaged(t, fmt.Sprintf("admin channels limit %d", lim), listAll(f.owner, f.p("/channels"), "channels", lim), append(append([]string{}, pub...), priv...), nil)
	}
	for _, lim := range []int{1, 2} {
		var bg background
		var mu sync.Mutex
		var newPriv []string
		bg.run(func(fail func(string, ...any)) {
			for j := 0; j < 4; j++ {
				expect(fail, 201, mk(fmt.Sprintf("pub/n%d-%d", lim, j)), "create pub")
				b := expect(fail, 201, mk(fmt.Sprintf("priv/n%d-%d", lim, j)), "create priv")
				if id, ok := b["id"].(string); ok {
					mu.Lock()
					newPriv = append(newPriv, id)
					mu.Unlock()
				}
			}
		})
		got := listAll(r, f.p("/channels"), "channels", lim)
		bg.wait(t)
		checkPaged(t, fmt.Sprintf("reader channels limit %d, concurrent", lim), got, pub, append(append([]string{}, priv...), newPriv...))
	}
}

func TestPAGAParticipantsPaging(t *testing.T) {
	f := newBoard(t)
	f.grant(t, f.owner, "*", "allow", "read", "post", "channel_manage")
	f.channel(t, f.owner, "open", "inherited")
	f.channel(t, f.owner, "secret", "private")
	v := newActor(t, "pgpv")
	f.grant(t, v, "open", "allow", "read")
	var open, secret []string
	for i := 0; i < 4; i++ {
		o := newActor(t, "pgpo")
		f.grant(t, o, "open", "allow", "read")
		open = append(open, o.id)
		if i < 2 {
			s := newActor(t, "pgps")
			f.grant(t, s, "secret", "allow", "read")
			secret = append(secret, s.id)
		}
	}
	base := idsOf(listAll(v, f.p("/actors"), "actors", 0))
	checkPaged(t, "participants (default page)", listAll(v, f.p("/actors"), "actors", 0), append(append([]string{}, open...), v.id), secret)
	for _, lim := range []int{1, 2} {
		checkPaged(t, fmt.Sprintf("participants limit %d", lim), listAll(v, f.p("/actors"), "actors", lim), base, secret)
	}
	// A board administrator sees every actor with a grant.
	checkPaged(t, "admin participants limit 1", listAll(f.owner, f.p("/actors"), "actors", 1), append(append(append([]string{}, base...), secret...), f.owner.id), nil)

	a := admin(t)
	for _, lim := range []int{1, 2} {
		var bg background
		var mu sync.Mutex
		var newSecret []string
		bg.run(func(fail func(string, ...any)) {
			for j := 0; j < 3; j++ {
				for _, ch := range []string{"open", "secret"} {
					act := expect(fail, 201, a.do("POST", "/v1/actors", J{"kind": "agent", "handle": "pgpn" + rnd()}, newKey()), "create actor")
					id, _ := act["id"].(string)
					expect(fail, 201, f.owner.do("POST", f.p("/grants"), grantBody(id, ch, "read"), newKey()), "grant "+ch)
					if ch == "secret" {
						mu.Lock()
						newSecret = append(newSecret, id)
						mu.Unlock()
					}
				}
			}
		})
		got := listAll(v, f.p("/actors"), "actors", lim)
		bg.wait(t)
		checkPaged(t, fmt.Sprintf("participants limit %d, concurrent", lim), got, base, append(append([]string{}, secret...), newSecret...))
	}
}

func TestPAGAMalformedTokenAndLimit(t *testing.T) {
	f := newBoard(t)
	f.grant(t, f.owner, "*", "allow", "read", "post", "channel_manage")
	f.channel(t, f.owner, "c", "inherited")
	anon := &actorClient{t: t}
	disc := anon.must(200, anon.do("GET", "/v1/server", nil)).Body
	maxPage := int(disc["limits"].(J)["max_page_size"].(float64))
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	badTokens := []string{"garbage", "k1.", "k1.!!!", "k1." + b64("not json"), "k1." + b64(`{"t":"2026-11-19T20:00:00Z"}`), "p1." + b64("1")}
	badLimits := []string{"0", "-1", "abc", "1.5", strconv.Itoa(maxPage + 1)}
	for _, l := range []struct{ path, field string }{
		{"/v1/boards", "boards"}, {f.p("/channels"), "channels"}, {f.p("/actors"), "actors"},
	} {
		for _, tok := range badTokens {
			f.owner.wantProblem(400, "validation_failed", f.owner.do("GET", l.path+"?page_token="+url.QueryEscape(tok), nil))
		}
		for _, lim := range badLimits {
			f.owner.wantProblem(400, "validation_failed", f.owner.do("GET", l.path+"?limit="+url.QueryEscape(lim), nil))
		}
		listPage(f.owner, l.path, l.field, "", 1)
		if items, _ := listPage(f.owner, l.path, l.field, "", maxPage); len(items) == 0 {
			t.Fatalf("GET %s: empty first page", l.path)
		}
	}
}
