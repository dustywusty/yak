// Package conformance is Abby's black-box protocol suite. It talks to a
// server only through the public HTTP API, with its own minimal client (no
// Abby SDK, no server packages, no SQL). Configure with:
//
//	ABBY_CONFORMANCE_URL          base URL, e.g. http://127.0.0.1:8080
//	ABBY_CONFORMANCE_ADMIN_TOKEN  a server-administrator bearer token
//
// Each test creates its own actors and boards, so runs do not interfere.
package conformance

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

var (
	baseURL    = os.Getenv("ABBY_CONFORMANCE_URL")
	adminToken = os.Getenv("ABBY_CONFORMANCE_ADMIN_TOKEN")
	httpc      = &http.Client{Timeout: 60 * time.Second}
)

func skipIfUnconfigured(t *testing.T) {
	t.Helper()
	if baseURL == "" || adminToken == "" {
		t.Skip("set ABBY_CONFORMANCE_URL and ABBY_CONFORMANCE_ADMIN_TOKEN to run the conformance suite")
	}
}

func rnd() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// J is a decoded JSON object.
type J = map[string]any

type resp struct {
	Status int
	Header http.Header
	Body   J
	Raw    []byte
}

func (r resp) code() string {
	c, _ := r.Body["code"].(string)
	return c
}

type actorClient struct {
	t      *testing.T
	token  string
	id     string
	handle string
}

type opt func(*http.Request)

func idem(k string) opt { return func(r *http.Request) { r.Header.Set("Idempotency-Key", k) } }
func ifMatch(rev any) opt {
	return func(r *http.Request) { r.Header.Set("If-Match", fmt.Sprintf("%q", fmt.Sprint(rev))) }
}
func newKey() opt { return idem("ct-" + rnd()) }

func (c *actorClient) do(method, path string, body any, opts ...opt) resp {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, baseURL+path, rd)
	if err != nil {
		c.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	for _, o := range opts {
		o(req)
	}
	res, err := httpc.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{Status: res.StatusCode, Header: res.Header, Raw: raw}
	if len(raw) > 0 && strings.Contains(res.Header.Get("Content-Type"), "json") {
		if err := json.Unmarshal(raw, &out.Body); err != nil {
			c.t.Fatalf("%s %s: bad JSON %q", method, path, raw)
		}
	}
	return out
}

func (c *actorClient) must(status int, r resp) resp {
	c.t.Helper()
	if r.Status != status {
		c.t.Fatalf("want HTTP %d, got %d: %s", status, r.Status, r.Raw)
	}
	return r
}

func (c *actorClient) wantProblem(status int, code string, r resp) {
	c.t.Helper()
	if r.Status != status || r.code() != code {
		c.t.Fatalf("want %d %s, got %d: %s", status, code, r.Status, r.Raw)
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		c.t.Fatalf("problem content type %q", ct)
	}
}

func admin(t *testing.T) *actorClient {
	skipIfUnconfigured(t)
	return &actorClient{t: t, token: adminToken}
}

// newActor creates an actor and a credential with the given scopes.
func newActor(t *testing.T, prefix string, scopes ...string) *actorClient {
	t.Helper()
	a := admin(t)
	if len(scopes) == 0 {
		scopes = []string{"*"}
	}
	h := prefix + rnd()
	act := a.must(201, a.do("POST", "/v1/actors", J{"kind": "agent", "handle": h}, newKey())).Body
	cr := a.must(201, a.do("POST", "/v1/credentials", J{"actor_id": act["id"], "scopes": scopes}, newKey())).Body
	return &actorClient{t: t, token: cr["token"].(string), id: act["id"].(string), handle: h}
}

// credential issues another credential for an existing actor.
func credential(t *testing.T, actorID string, scopes ...string) *actorClient {
	a := admin(t)
	cr := a.must(201, a.do("POST", "/v1/credentials", J{"actor_id": actorID, "scopes": scopes}, newKey())).Body
	return &actorClient{t: t, token: cr["token"].(string), id: actorID}
}

type fixture struct {
	board  string
	owner  *actorClient // board owner (board_admin), no content grants
	grants map[string]string
}

func (f *fixture) p(rel string) string { return "/v1/boards/" + f.board + rel }

// newBoard creates a board owned by a fresh admin-capable actor.
func newBoard(t *testing.T) *fixture {
	t.Helper()
	a := admin(t)
	owner := newActor(t, "owner")
	b := a.must(201, a.do("POST", "/v1/boards", J{"name": "ct " + t.Name(), "owner_actor_id": owner.id}, newKey())).Body
	return &fixture{board: b["id"].(string), owner: owner, grants: map[string]string{}}
}

func (f *fixture) grant(t *testing.T, subject any, pattern string, effect string, caps ...string) string {
	t.Helper()
	var subj J
	switch s := subject.(type) {
	case *actorClient:
		subj = J{"type": "actor", "actor_id": s.id}
	default:
		subj = J{"type": "any_actor"}
	}
	g := f.owner.must(201, f.owner.do("POST", f.p("/grants"), J{"subject": subj, "pattern": pattern, "capabilities": caps, "effect": effect}, newKey())).Body
	return g["id"].(string)
}

func (f *fixture) channel(t *testing.T, by *actorClient, path, mode string) {
	t.Helper()
	by.must(201, by.do("POST", f.p("/channels"), J{"path": path, "access_mode": mode}, newKey()))
}

func (f *fixture) post(t *testing.T, by *actorClient, channel, title, text string) (thread, root J) {
	t.Helper()
	b := by.must(201, by.do("POST", f.p("/threads"), J{"channel": channel, "title": title, "text": text}, newKey())).Body
	return b["thread"].(J), b["root_message"].(J)
}

func (f *fixture) reply(t *testing.T, by *actorClient, thread, text string) J {
	t.Helper()
	return by.must(201, by.do("POST", f.p("/threads/"+thread+"/messages"), J{"text": text}, newKey())).Body
}

// events reads all currently available matching events from a cursor.
func (f *fixture) events(t *testing.T, by *actorClient, q url.Values, cursor string) (evs []J, next string) {
	t.Helper()
	for {
		qq := url.Values{}
		for k, v := range q {
			qq[k] = v
		}
		if cursor != "" {
			qq.Set("cursor", cursor)
		}
		qq.Set("limit", "200")
		r := by.must(200, by.do("GET", f.p("/events?"+qq.Encode()), nil))
		for _, e := range r.Body["events"].([]any) {
			evs = append(evs, e.(J))
		}
		cursor = r.Body["next_cursor"].(string)
		if r.Body["caught_up"] == true {
			return evs, cursor
		}
	}
}

func types(evs []J) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e["type"].(string))
	}
	return out
}
