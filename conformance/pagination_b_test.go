package conformance

// PAGE-001 for the administrative lists: GET …/grants, GET /v1/credentials,
// GET /v1/actors, GET …/audit-events, and GET /v1/audit-events. Keyset
// pages in (created_at, id) ascending order, except audit events, which are
// newest first by (recorded_at, id) descending.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// pagbItem is one listed item with its keyset (time, id).
type pagbItem struct {
	ID string
	T  time.Time
}

// pagbPage fetches one page and returns its items (keyed by tsField) and the
// next page token ("" on the last page).
func pagbPage(t *testing.T, c *actorClient, path, field, tsField string, q url.Values) ([]pagbItem, string) {
	t.Helper()
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	r := c.must(200, c.do("GET", path+sep+q.Encode(), nil))
	raw, ok := r.Body["next_page_token"]
	if !ok {
		t.Fatalf("%s: response has no next_page_token: %s", path, r.Raw)
	}
	next := ""
	if raw != nil {
		s, ok := raw.(string)
		if !ok || s == "" {
			t.Fatalf("%s: next_page_token must be a non-empty string or null, got %v", path, raw)
		}
		next = s
	}
	list, ok := r.Body[field].([]any)
	if !ok {
		t.Fatalf("%s: no %q array: %s", path, field, r.Raw)
	}
	var out []pagbItem
	for _, x := range list {
		m := x.(J)
		ts, err := time.Parse(time.RFC3339Nano, m[tsField].(string))
		if err != nil {
			t.Fatalf("%s: bad %s %v", path, tsField, m[tsField])
		}
		out = append(out, pagbItem{ID: m["id"].(string), T: ts})
	}
	return out, next
}

// pagbAll follows next_page_token from the first page to the last. between
// runs after each page (used to insert items while paging).
func pagbAll(t *testing.T, c *actorClient, path, field, tsField string, limit int, between func(page int)) []pagbItem {
	t.Helper()
	var all []pagbItem
	q := url.Values{"limit": {fmt.Sprint(limit)}}
	for page := 0; ; page++ {
		if page > 10000 {
			t.Fatalf("%s: pagination does not terminate", path)
		}
		items, next := pagbPage(t, c, path, field, tsField, q)
		if len(items) > limit {
			t.Fatalf("%s: page has %d items, limit %d", path, len(items), limit)
		}
		all = append(all, items...)
		if between != nil {
			between(page)
		}
		if next == "" {
			return all
		}
		q.Set("page_token", next)
	}
}

// pagbCheck asserts no item repeats, every id in want appears exactly once,
// and the walk is ordered (ascending, or descending when desc).
func pagbCheck(t *testing.T, what string, got []pagbItem, want []string, desc bool) {
	t.Helper()
	seen := map[string]int{}
	for i, it := range got {
		seen[it.ID]++
		if seen[it.ID] > 1 {
			t.Fatalf("%s: item %s repeated across pages", what, it.ID)
		}
		if i == 0 {
			continue
		}
		p := got[i-1]
		cmp := p.T.Compare(it.T)
		if cmp == 0 {
			cmp = strings.Compare(p.ID, it.ID)
		}
		if (!desc && cmp >= 0) || (desc && cmp <= 0) {
			t.Fatalf("%s: order broken at %d: (%s,%s) then (%s,%s) (desc=%v)", what, i, p.T.Format(time.RFC3339Nano), p.ID, it.T.Format(time.RFC3339Nano), it.ID, desc)
		}
	}
	for _, id := range want {
		if seen[id] != 1 {
			t.Fatalf("%s: existing item %s seen %d times, want exactly once", what, id, seen[id])
		}
	}
}

// pagbPost is a raw POST for use off the test goroutine (no t.Fatal).
func pagbPost(token, path string, body any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", baseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", "ct-"+rnd())
	res, err := httpc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != 201 {
		return fmt.Errorf("POST %s: %d %s", path, res.StatusCode, raw)
	}
	return nil
}

// pagbInserter runs inserts on other goroutines while paging runs, and
// wait fails the test if any insert failed.
type pagbInserter struct {
	wg   sync.WaitGroup
	mu   sync.Mutex
	errs []error
}

func (p *pagbInserter) kick(insert func() error) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		if err := insert(); err != nil {
			p.mu.Lock()
			p.errs = append(p.errs, err)
			p.mu.Unlock()
		}
	}()
}

func (p *pagbInserter) wait(t *testing.T) {
	t.Helper()
	p.wg.Wait()
	if len(p.errs) > 0 {
		t.Fatalf("concurrent insert failed: %v", p.errs[0])
	}
}

func pagbIDs(items []pagbItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

func pagbMaxPageSize(t *testing.T) int {
	t.Helper()
	c := &actorClient{t: t}
	d := c.must(200, c.do("GET", "/v1/server", nil)).Body
	lim, _ := d["limits"].(J)
	n, ok := lim["max_page_size"].(float64)
	if !ok || n < 1 {
		t.Fatalf("discovery limits.max_page_size missing: %v", lim)
	}
	return int(n)
}

func TestPAGBGrantsPaging(t *testing.T) { // PAGE-001
	skipIfUnconfigured(t)
	f := newBoard(t)
	subj := newActor(t, "pgsub")
	for i := 0; i < 7; i++ {
		f.owner.must(201, f.owner.do("POST", f.p("/grants"), grantBody(subj.id, "pg/"+rnd(), "read"), newKey()))
	}
	// Revoke one so include_revoked changes the walk.
	first := f.owner.must(200, f.owner.do("GET", f.p("/grants?limit=1"), nil)).Body["grants"].([]any)[0].(J)
	f.owner.must(200, f.owner.do("DELETE", f.p("/grants/"+first["id"].(string)), nil))

	existing := pagbAll(t, f.owner, f.p("/grants?include_revoked=true"), "grants", "created_at", pagbMaxPageSize(t), nil)
	if len(existing) != 7 {
		t.Fatalf("want 7 grants including revoked, got %d", len(existing))
	}
	pagbCheck(t, "grants baseline", existing, nil, false)

	var ins pagbInserter
	got := pagbAll(t, f.owner, f.p("/grants?include_revoked=true"), "grants", "created_at", 2, func(int) {
		ins.kick(func() error { return pagbPost(f.owner.token, f.p("/grants"), grantBody(subj.id, "pg/"+rnd(), "read")) })
	})
	ins.wait(t)
	pagbCheck(t, "grants under inserts", got, pagbIDs(existing), false)

	active := pagbAll(t, f.owner, f.p("/grants"), "grants", "created_at", 3, nil)
	for _, it := range active {
		if it.ID == first["id"] {
			t.Fatal("revoked grant listed without include_revoked")
		}
	}
	pagbCheck(t, "active grants", active, pagbIDs(existing[1:]), false)
}

func TestPAGBCredentialsPaging(t *testing.T) { // PAGE-001
	a := admin(t)
	who := newActor(t, "pgcred")
	for i := 0; i < 6; i++ {
		a.must(201, a.do("POST", "/v1/credentials", J{"actor_id": who.id, "scopes": []string{"read"}}, newKey()))
	}
	path := "/v1/credentials?actor_id=" + who.id
	existing := pagbAll(t, a, path, "credentials", "created_at", pagbMaxPageSize(t), nil)
	if len(existing) != 7 {
		t.Fatalf("want 7 credentials, got %d", len(existing))
	}
	var ins pagbInserter
	got := pagbAll(t, a, path, "credentials", "created_at", 2, func(int) {
		ins.kick(func() error {
			return pagbPost(adminToken, "/v1/credentials", J{"actor_id": who.id, "scopes": []string{"read"}})
		})
	})
	ins.wait(t)
	pagbCheck(t, "credentials under inserts", got, pagbIDs(existing), false)

	// The actor's own view (no actor_id) pages the same way.
	own := pagbAll(t, who, "/v1/credentials", "credentials", "created_at", 1, nil)
	pagbCheck(t, "own credentials", own, pagbIDs(existing), false)
}

func TestPAGBActorsPaging(t *testing.T) { // PAGE-001
	a := admin(t)
	var mine []string
	for i := 0; i < 3; i++ {
		mine = append(mine, newActor(t, "pgact").id)
	}
	// A shared server may hold many actors: walk with max_page_size pages while
	// actors are created concurrently, then check the small-page prefix.
	var ins pagbInserter
	got := pagbAll(t, a, "/v1/actors", "actors", "created_at", pagbMaxPageSize(t), func(int) {
		ins.kick(func() error {
			return pagbPost(adminToken, "/v1/actors", J{"kind": "agent", "handle": "pgins" + rnd()})
		})
	})
	ins.wait(t)
	pagbCheck(t, "actors under inserts", got, mine, false)

	var small []pagbItem
	q := url.Values{"limit": {"1"}}
	for i := 0; i < 4; i++ {
		items, next := pagbPage(t, a, "/v1/actors", "actors", "created_at", q)
		if len(items) != 1 || next == "" {
			t.Fatalf("limit=1 page %d: %d items, next %q", i, len(items), next)
		}
		small = append(small, items...)
		q.Set("page_token", next)
	}
	for i := range small {
		if small[i].ID != got[i].ID {
			t.Fatalf("limit=1 walk diverges from max-page walk at %d: %s vs %s", i, small[i].ID, got[i].ID)
		}
	}
}

func TestPAGBBoardAuditPaging(t *testing.T) { // PAGE-001, AUTH-018
	skipIfUnconfigured(t)
	f := newBoard(t)
	subj := newActor(t, "pgaud")
	var newest string
	for i := 0; i < 6; i++ {
		g := f.owner.must(201, f.owner.do("POST", f.p("/grants"), grantBody(subj.id, "pg/"+rnd(), "read"), newKey())).Body
		newest = g["id"].(string)
	}
	// The newest event is the last grant's revoke.
	f.owner.must(200, f.owner.do("DELETE", f.p("/grants/"+newest), nil))

	existing := pagbAll(t, f.owner, f.p("/audit-events"), "audit_events", "recorded_at", pagbMaxPageSize(t), nil)
	if len(existing) < 7 {
		t.Fatalf("want at least 7 board audit events, got %d", len(existing))
	}
	pagbCheck(t, "board audit baseline", existing, nil, true)
	top := f.owner.must(200, f.owner.do("GET", f.p("/audit-events?limit=1"), nil)).Body["audit_events"].([]any)[0].(J)
	if top["action"] != "grant.revoke" || top["target_id"] != newest {
		t.Fatalf("audit must be newest first; first entry %v", top)
	}

	var ins pagbInserter
	got := pagbAll(t, f.owner, f.p("/audit-events"), "audit_events", "recorded_at", 2, func(int) {
		ins.kick(func() error { return pagbPost(f.owner.token, f.p("/grants"), grantBody(subj.id, "pg/"+rnd(), "read")) })
	})
	ins.wait(t)
	pagbCheck(t, "board audit under inserts", got, pagbIDs(existing), true)
}

func TestPAGBServerAuditPaging(t *testing.T) { // PAGE-001, AUTH-018
	a := admin(t)
	older := newActor(t, "pgold").id
	newer := newActor(t, "pgnew").id
	// Walk newest-first pages until both actor.create events are found; the
	// newer actor's event must come first.
	pos := map[string]int{}
	var walked []pagbItem
	q := url.Values{"limit": {"5"}}
	for page := 0; page < 400 && len(pos) < 2; page++ {
		r := a.must(200, a.do("GET", "/v1/audit-events?"+q.Encode(), nil)).Body
		for _, x := range r["audit_events"].([]any) {
			m := x.(J)
			ts, _ := time.Parse(time.RFC3339Nano, m["recorded_at"].(string))
			walked = append(walked, pagbItem{ID: m["id"].(string), T: ts})
			if m["action"] == "actor.create" && (m["target_id"] == older || m["target_id"] == newer) {
				pos[m["target_id"].(string)] = len(walked)
			}
		}
		next, _ := r["next_page_token"].(string)
		if next == "" {
			break
		}
		q.Set("page_token", next)
	}
	if len(pos) != 2 {
		t.Fatalf("actor.create events not found in server audit walk: %v", pos)
	}
	if pos[newer] >= pos[older] {
		t.Fatalf("server audit must be newest first: newer at %d, older at %d", pos[newer], pos[older])
	}
	pagbCheck(t, "server audit", walked, nil, true)
}

func TestPAGBBadTokensAndLimits(t *testing.T) { // PAGE-001
	a := admin(t)
	f := newBoard(t)
	max := pagbMaxPageSize(t)
	routes := []struct {
		c    *actorClient
		path string
	}{
		{f.owner, f.p("/grants")},
		{a, "/v1/credentials"},
		{a, "/v1/actors"},
		{f.owner, f.p("/audit-events")},
		{a, "/v1/audit-events"},
	}
	bad := []string{
		"page_token=garbage",
		"page_token=k1.%21%21%21",
		"page_token=k1." + "e30", // {} has no id
		"page_token=p1.AAAAAAAAAAE",
		"limit=0",
		"limit=-1",
		"limit=abc",
		fmt.Sprintf("limit=%d", max+1),
	}
	for _, rt := range routes {
		for _, q := range bad {
			rt.c.wantProblem(400, "validation_failed", rt.c.do("GET", rt.path+"?"+q, nil))
		}
		for _, q := range []string{"limit=1", fmt.Sprintf("limit=%d", max), ""} {
			r := rt.c.must(200, rt.c.do("GET", rt.path+"?"+q, nil))
			if _, ok := r.Body["next_page_token"]; !ok {
				t.Fatalf("%s?%s: no next_page_token field", rt.path, q)
			}
		}
	}
	// The last page reports null.
	r := f.owner.must(200, f.owner.do("GET", f.p("/grants?limit="+fmt.Sprint(max)), nil)).Body
	if r["next_page_token"] != nil {
		t.Fatalf("last grants page must have null next_page_token, got %v", r["next_page_token"])
	}
}
