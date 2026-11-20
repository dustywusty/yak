package conformance

// Wire-contract checks for spec/: the OpenAPI document parses and its
// references resolve, every JSON Schema compiles, every example validates
// against its schema, and (when a server is configured) live responses
// validate against the schemas. Like the rest of this package, the live part
// uses only raw HTTP; it imports no server packages.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

const specDir = "../spec"

var (
	schemaOnce sync.Once
	schemaSet  map[string]*jsonschema.Schema
	schemaErr  error
)

// specSchemas compiles every spec/schemas/<Name>.schema.json, keyed by Name.
func specSchemas(t *testing.T) map[string]*jsonschema.Schema {
	t.Helper()
	schemaOnce.Do(func() {
		files, err := filepath.Glob(filepath.Join(specDir, "schemas", "*.schema.json"))
		if err != nil || len(files) == 0 {
			schemaErr = fmt.Errorf("no schema files found (%v)", err)
			return
		}
		c := jsonschema.NewCompiler()
		c.DefaultDraft(jsonschema.Draft2020)
		c.AssertFormat()
		schemaSet = map[string]*jsonschema.Schema{}
		for _, f := range files {
			abs, err := filepath.Abs(f)
			if err != nil {
				schemaErr = err
				return
			}
			s, err := c.Compile(abs)
			if err != nil {
				schemaErr = fmt.Errorf("%s: %w", filepath.Base(f), err)
				return
			}
			schemaSet[strings.TrimSuffix(filepath.Base(f), ".schema.json")] = s
		}
	})
	if schemaErr != nil {
		t.Fatalf("compile schemas: %v", schemaErr)
	}
	return schemaSet
}

func schemaNamed(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	s, ok := specSchemas(t)[name]
	if !ok {
		t.Fatalf("no schema spec/schemas/%s.schema.json", name)
	}
	return s
}

func decodeInstance(t *testing.T, raw []byte) any {
	t.Helper()
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode JSON: %v: %s", err, raw)
	}
	return v
}

func TestSpecSchemasCompile(t *testing.T) {
	s := specSchemas(t)
	for _, name := range []string{
		"Problem", "Discovery", "Actor", "Credential", "IssuedCredential", "Me", "Board", "Channel", "Permissions",
		"Grant", "Thread", "ThreadWithRoot", "Message", "ThreadPage", "MessagePage", "Event", "EventPage",
		"SnapshotInfo", "SnapshotPage", "AuditEvent",
		"CreateActorRequest", "CreateCredentialRequest", "CreateBoardRequest", "PatchBoardRequest", "CreateChannelRequest",
		"CreateGrantRequest", "PatchGrantRequest", "CreateThreadRequest", "PatchThreadRequest", "ReplyRequest",
		"EditMessageRequest", "DeleteMessageRequest", "LabelsRequest", "CreateSnapshotRequest",
	} {
		if s[name] == nil {
			t.Errorf("required schema %s.schema.json is missing", name)
		}
	}
}

// TestSpecExamplesValidate validates spec/examples/<Schema>.<variant>.json
// against spec/schemas/<Schema>.schema.json and requires at least one example
// per schema.
func TestSpecExamplesValidate(t *testing.T) {
	schemas := specSchemas(t)
	files, err := filepath.Glob(filepath.Join(specDir, "examples", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no examples found (%v)", err)
	}
	covered := map[string]bool{}
	for _, f := range files {
		base := filepath.Base(f)
		name, variant, ok := strings.Cut(strings.TrimSuffix(base, ".json"), ".")
		if !ok || variant == "" {
			t.Errorf("%s: example names must be <Schema>.<variant>.json", base)
			continue
		}
		s, ok := schemas[name]
		if !ok {
			t.Errorf("%s: no schema %s.schema.json", base, name)
			continue
		}
		covered[name] = true
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Validate(decodeInstance(t, raw)); err != nil {
			t.Errorf("%s does not validate:\n%v", base, err)
		}
	}
	for name := range schemas {
		if !covered[name] {
			t.Errorf("schema %s has no example in spec/examples", name)
		}
	}
}

// TestSpecSchemasRejectInvalid spot-checks that request schemas are closed
// and that nullability and discriminators are enforced.
func TestSpecSchemasRejectInvalid(t *testing.T) {
	cases := []struct{ schema, doc string }{
		{"CreateBoardRequest", `{"name":"x","bogus":1}`},
		{"CreateBoardRequest", `{"name":"   "}`},
		{"CreateChannelRequest", `{"path":"Eng"}`},
		{"CreateGrantRequest", `{"subject":{"type":"actor"},"pattern":"*","capabilities":["read"]}`},
		{"LabelsRequest", `{"operations":[{"op":"unset","key":"a","value":"b"}]}`},
		{"LabelsRequest", `{"operations":[{"op":"set","key":"abby.task.state","value":"x"}]}`},
		{"ReplyRequest", `{"text":""}`},
		{"CreateCredentialRequest", `{"scopes":["read"],"expires_in_seconds":30}`},
		{"CreateSnapshotRequest", `{"filter":{"channel":"eng"}}`},
		{"Message", `{"id":"m"}`},
		{"IssuedCredential", `{"credential":{}}`},
		{"Timestamp", `"2026-11-19T20:00:00-08:00"`},
		{"Event", `{"id":"e","cursor":"c","type":"message.created","board_id":"b","actor_id":"a","credential_id":null,"run_id":null,
			"resource":{"type":"message","id":"m","revision":1},"channel_id":"c","thread_id":"t",
			"recorded_at":"2026-11-19T20:00:00Z","effective_at":"2026-11-19T20:00:00Z","payload":{"thread":{}}}`},
	}
	for _, c := range cases {
		if err := schemaNamed(t, c.schema).Validate(decodeInstance(t, []byte(c.doc))); err == nil {
			t.Errorf("%s accepted invalid document %s", c.schema, c.doc)
		}
	}
}

// ---- OpenAPI ----

func loadOpenAPI(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(specDir, "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("openapi.yaml does not parse: %v", err)
	}
	return doc
}

// walkRefs calls fn for every "$ref" and "externalValue" string in v.
func walkRefs(v any, fn func(key, ref string)) {
	switch x := v.(type) {
	case map[string]any:
		for k, vv := range x {
			if s, ok := vv.(string); ok && (k == "$ref" || k == "externalValue") {
				fn(k, s)
				continue
			}
			walkRefs(vv, fn)
		}
	case []any:
		for _, vv := range x {
			walkRefs(vv, fn)
		}
	}
}

func jsonPointer(doc any, ptr string) (any, bool) {
	cur := doc
	for _, tok := range strings.Split(strings.TrimPrefix(ptr, "/"), "/") {
		tok = strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[tok]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func TestSpecOpenAPI(t *testing.T) {
	doc := loadOpenAPI(t)
	if v, _ := doc["openapi"].(string); !strings.HasPrefix(v, "3.1.") {
		t.Errorf("openapi version %q, want 3.1.x", v)
	}
	if v, _ := doc["jsonSchemaDialect"].(string); v != "https://json-schema.org/draft/2020-12/schema" {
		t.Errorf("jsonSchemaDialect %q", v)
	}

	// Every reference resolves: local JSON pointers into the document, and
	// relative file references into spec/.
	referenced := map[string]bool{}
	walkRefs(doc, func(key, ref string) {
		file, frag, _ := strings.Cut(ref, "#")
		if file == "" {
			if _, ok := jsonPointer(doc, frag); !ok {
				t.Errorf("unresolved local %s %q", key, ref)
			}
			return
		}
		p := filepath.Join(specDir, filepath.FromSlash(file))
		if _, err := os.Stat(p); err != nil {
			t.Errorf("unresolved file %s %q", key, ref)
			return
		}
		if strings.HasSuffix(file, ".schema.json") {
			referenced[strings.TrimSuffix(filepath.Base(file), ".schema.json")] = true
		}
	})

	// Single source of truth: every schema file is reachable from the
	// OpenAPI document, directly or through other schema files.
	queue := make([]string, 0, len(referenced))
	for n := range referenced {
		queue = append(queue, n)
	}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		raw, err := os.ReadFile(filepath.Join(specDir, "schemas", n+".schema.json"))
		if err != nil {
			continue
		}
		var s any
		_ = json.Unmarshal(raw, &s)
		walkRefs(s, func(_, ref string) {
			file, _, _ := strings.Cut(ref, "#")
			if file == "" {
				return
			}
			m := strings.TrimSuffix(filepath.Base(file), ".schema.json")
			if !referenced[m] {
				referenced[m] = true
				queue = append(queue, m)
			}
		})
	}
	for n := range specSchemas(t) {
		if !referenced[n] {
			t.Errorf("schema %s is not reachable from openapi.yaml", n)
		}
	}
	if comps, ok := doc["components"].(map[string]any); ok {
		if s, ok := comps["schemas"].(map[string]any); ok {
			for name, v := range s {
				m, _ := v.(map[string]any)
				if _, isRef := m["$ref"]; !isRef || len(m) != 1 {
					t.Errorf("components.schemas.%s must be a bare $ref to spec/schemas (single source of truth)", name)
				}
			}
		}
	}

	// Operations: unique operationId, responses, and an x-abby contract on
	// every mutating operation.
	paths, _ := doc["paths"].(map[string]any)
	if len(paths) == 0 {
		t.Fatal("no paths")
	}
	seen := map[string]string{}
	var ops []string
	for p, item := range paths {
		for method, raw := range item.(map[string]any) {
			op, ok := raw.(map[string]any)
			if !ok || method == "parameters" {
				continue
			}
			where := strings.ToUpper(method) + " " + p
			ops = append(ops, where)
			id, _ := op["operationId"].(string)
			if id == "" {
				t.Errorf("%s: missing operationId", where)
			} else if prev, dup := seen[id]; dup {
				t.Errorf("%s: operationId %s already used by %s", where, id, prev)
			}
			seen[id] = where
			if r, _ := op["responses"].(map[string]any); len(r) == 0 {
				t.Errorf("%s: no responses", where)
			}
			if method == "post" || method == "patch" || method == "put" || method == "delete" {
				x, _ := op["x-abby"].(map[string]any)
				if x == nil {
					t.Errorf("%s: missing x-abby", where)
					continue
				}
				switch x["idempotency"] {
				case "required", "not_applicable":
				default:
					t.Errorf("%s: x-abby.idempotency %v", where, x["idempotency"])
				}
				if pre, ok := x["precondition"]; ok && pre != "If-Match required" && pre != "If-Match optional" && pre != "none" {
					t.Errorf("%s: x-abby.precondition %v", where, pre)
				}
				if a, _ := x["authorization"].(string); a == "" {
					t.Errorf("%s: x-abby.authorization missing", where)
				}
				if _, ok := x["events"].([]any); !ok {
					t.Errorf("%s: x-abby.events must be a list", where)
				}
			}
		}
	}
	sort.Strings(ops)
	t.Logf("%d operations", len(ops))
}

// ---- live responses ----

// liveCheck validates r's status, content type and body against schema in a
// subtest. Timestamps must be UTC (design §5; enforced by Timestamp).
func liveCheck(t *testing.T, name, schema string, status int, r resp) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		t.Helper()
		if r.Status != status {
			t.Fatalf("want HTTP %d, got %d: %s", status, r.Status, r.Raw)
		}
		ct := r.Header.Get("Content-Type")
		wantCT := "application/json"
		if schema == "Problem" {
			wantCT = "application/problem+json"
		}
		if !strings.HasPrefix(ct, wantCT) {
			t.Errorf("Content-Type %q, want %s", ct, wantCT)
		}
		if r.Header.Get("X-Request-Id") == "" {
			t.Error("missing X-Request-Id response header")
		}
		if err := schemaNamed(t, schema).Validate(decodeInstance(t, r.Raw)); err != nil {
			t.Fatalf("%s does not match %s:\n%v\nbody: %s", name, schema, err, r.Raw)
		}
	})
}

// sendJSON validates a request body against its request schema, then sends it.
func sendJSON(t *testing.T, c *actorClient, reqSchema, method, path string, body any, opts ...opt) resp {
	t.Helper()
	if reqSchema != "" {
		raw, _ := json.Marshal(body)
		if err := schemaNamed(t, reqSchema).Validate(decodeInstance(t, raw)); err != nil {
			t.Fatalf("test request does not match %s: %v", reqSchema, err)
		}
	}
	return c.do(method, path, body, opts...)
}

func header(k, v string) opt { return func(r *http.Request) { r.Header.Set(k, v) } }

func TestSpecLiveResponses(t *testing.T) {
	a := admin(t)
	anon := &actorClient{t: t}
	str := func(m J, k string) string { s, _ := m[k].(string); return s }

	liveCheck(t, "GET /v1/server", "Discovery", 200, anon.do("GET", "/v1/server", nil))
	liveCheck(t, "GET /v1/me", "Me", 200, a.do("GET", "/v1/me", nil))
	liveCheck(t, "GET /healthz", "Health", 200, anon.do("GET", "/healthz", nil))
	liveCheck(t, "GET /readyz", "Health", 200, anon.do("GET", "/readyz", nil))

	owner := newActor(t, "owner")
	bob := newActor(t, "bob")

	// Board, grant, channel.
	br := sendJSON(t, a, "CreateBoardRequest", "POST", "/v1/boards", J{"name": "schema " + rnd(), "owner_actor_id": owner.id}, newKey())
	liveCheck(t, "POST /v1/boards", "Board", 201, br)
	f := &fixture{board: str(br.Body, "id"), owner: owner}

	gr := sendJSON(t, owner, "CreateGrantRequest", "POST", f.p("/grants"),
		J{"subject": J{"type": "any_actor"}, "pattern": "*", "capabilities": []string{"read", "post", "label", "channel_manage"}}, newKey())
	liveCheck(t, "POST /grants", "Grant", 201, gr)
	grantID := str(gr.Body, "id")
	gr2 := sendJSON(t, owner, "CreateGrantRequest", "POST", f.p("/grants"),
		J{"subject": J{"type": "actor", "actor_id": bob.id}, "pattern": "eng/backend", "capabilities": []string{"post"}, "effect": "deny"}, newKey())
	liveCheck(t, "POST /grants (actor deny)", "Grant", 201, gr2)

	chKey := newKey()
	chBody := J{"path": "eng/backend", "access_mode": "private"}
	liveCheck(t, "POST /channels", "Channel", 201, sendJSON(t, owner, "CreateChannelRequest", "POST", f.p("/channels"), chBody, chKey))
	replay := owner.do("POST", f.p("/channels"), chBody, chKey)
	liveCheck(t, "POST /channels (idempotent replay)", "Channel", 201, replay)
	if replay.Header.Get("Idempotent-Replayed") != "true" {
		t.Errorf("replay lacks Idempotent-Replayed: true")
	}

	// Thread, reply, reads.
	tr := sendJSON(t, owner, "CreateThreadRequest", "POST", f.p("/threads"), J{
		"channel": "eng", "title": "Deploy", "text": "deploy? @" + bob.handle,
		"labels": J{"Status": "open"}, "extensions": J{"acme.ticket": "OPS-1"},
	}, newKey())
	liveCheck(t, "POST /threads", "ThreadWithRoot", 201, tr)
	thread, _ := tr.Body["thread"].(J)
	rootMsg, _ := tr.Body["root_message"].(J)
	tid, rootID := str(thread, "id"), str(rootMsg, "id")
	if tid == "" || rootID == "" {
		t.Fatalf("thread creation failed: %s", tr.Raw)
	}

	rr := sendJSON(t, bob, "ReplyRequest", "POST", f.p("/threads/"+tid+"/messages"),
		J{"text": "ack", "reply_to_message_id": rootID}, newKey(), header("Abby-Run-Id", "run-schema-1"))
	liveCheck(t, "POST /threads/{id}/messages", "Message", 201, rr)
	replyID := str(rr.Body, "id")

	gm := bob.do("GET", f.p("/messages/"+replyID), nil)
	liveCheck(t, "GET /messages/{id}", "Message", 200, gm)
	if gm.Header.Get("ETag") != `"1"` {
		t.Errorf("GET message ETag %q, want \"1\"", gm.Header.Get("ETag"))
	}
	liveCheck(t, "PATCH /messages/{id}", "Message", 200,
		sendJSON(t, bob, "EditMessageRequest", "PATCH", f.p("/messages/"+replyID), J{"text": "ack v2"}, ifMatch(1)))
	liveCheck(t, "PATCH /messages/{id}/labels", "Message", 200,
		sendJSON(t, owner, "LabelsRequest", "PATCH", f.p("/messages/"+rootID+"/labels"),
			J{"operations": []J{{"op": "set", "key": "status", "value": "done", "expect": J{"value": "open"}}}}))
	liveCheck(t, "PATCH /messages/{id}/labels (failed expectation)", "Problem", 412,
		sendJSON(t, owner, "LabelsRequest", "PATCH", f.p("/messages/"+rootID+"/labels"),
			J{"operations": []J{{"op": "set", "key": "status", "value": "x", "expect": J{"absent": true}}}}))

	gt := owner.do("GET", f.p("/threads/"+tid), nil)
	liveCheck(t, "GET /threads/{id}", "ThreadWithRoot", 200, gt)
	liveCheck(t, "PATCH /threads/{id}", "Thread", 200,
		sendJSON(t, owner, "PatchThreadRequest", "PATCH", f.p("/threads/"+tid), J{"title": "Deploy v2"},
			func(r *http.Request) { r.Header.Set("If-Match", gt.Header.Get("ETag")) }))
	liveCheck(t, "POST /threads/{id}/messages (stale guard)", "Problem", 409,
		sendJSON(t, owner, "ReplyRequest", "POST", f.p("/threads/"+tid+"/messages"), J{"text": "x", "expect_latest_message_id": rootID}, newKey()))
	liveCheck(t, "DELETE /messages/{id}", "Message", 200,
		sendJSON(t, bob, "DeleteMessageRequest", "DELETE", f.p("/messages/"+replyID), J{}, ifMatch(2)))
	liveCheck(t, "GET /threads", "ThreadPage", 200, owner.do("GET", f.p("/threads?limit=1"), nil))
	// Thread listing: thread_id filters; message-level filters are rejected (EVT-005).
	other := sendJSON(t, owner, "CreateThreadRequest", "POST", f.p("/threads"), J{"channel": "eng", "title": "Other", "text": "x"}, newKey())
	liveCheck(t, "POST /threads (second)", "ThreadWithRoot", 201, other)
	byID := owner.do("GET", f.p("/threads?thread_id="+url.QueryEscape(tid)), nil)
	liveCheck(t, "GET /threads?thread_id", "ThreadPage", 200, byID)
	if ths, _ := byID.Body["threads"].([]any); len(ths) != 1 || str(ths[0].(J), "id") != tid {
		t.Errorf("GET /threads?thread_id=%s: want exactly that thread, got %s", tid, byID.Raw)
	}
	for _, q := range []string{"text=x", "mention=" + bob.id, "type=message.created"} {
		r := owner.do("GET", f.p("/threads?"+q), nil)
		liveCheck(t, "GET /threads?"+q, "Problem", 400, r)
		if r.code() != "validation_failed" {
			t.Errorf("GET /threads?%s: code %q, want validation_failed", q, r.code())
		}
	}
	liveCheck(t, "GET /threads/{id}/messages", "MessagePage", 200, owner.do("GET", f.p("/threads/"+tid+"/messages?limit=1"), nil))

	// Events: the page, then each event (and so its payload) individually.
	er := owner.do("GET", f.p("/events?limit=200"), nil)
	liveCheck(t, "GET /events", "EventPage", 200, er)
	evs, _ := er.Body["events"].([]any)
	seenTypes := map[string]bool{}
	for i, e := range evs {
		raw, _ := json.Marshal(e)
		typ := str(e.(J), "type")
		seenTypes[typ] = true
		liveCheck(t, fmt.Sprintf("event[%d] %s", i, typ), "Event", 200, resp{Status: 200, Header: er.Header, Raw: raw})
	}
	for _, typ := range []string{"channel.created", "thread.created", "message.created", "message.edited",
		"message.deleted", "message.labels_changed", "thread.view_invalidated", "thread.updated"} {
		if !seenTypes[typ] {
			t.Errorf("event type %s not observed", typ)
		}
	}
	liveCheck(t, "GET /events (long-poll timeout)", "EventPage", 200, owner.do("GET", f.p("/events?from=latest&wait_ms=50"), nil))
	liveCheck(t, "GET /events (bad cursor)", "Problem", 400, owner.do("GET", f.p("/events?cursor=bogus"), nil))

	// Snapshots: page through with limit=1 to see both page kinds.
	sr := owner.do("POST", f.p("/snapshots?channel=eng"), nil)
	liveCheck(t, "POST /snapshots", "SnapshotInfo", 201, sr)
	sid := str(sr.Body, "id")
	tok := ""
	for i := 0; i < 20; i++ {
		q := url.Values{"limit": {"1"}}
		if tok != "" {
			q.Set("page_token", tok)
		}
		pg := owner.do("GET", f.p("/snapshots/"+sid+"?"+q.Encode()), nil)
		liveCheck(t, fmt.Sprintf("GET /snapshots/{id} page %d", i), "SnapshotPage", 200, pg)
		tok = str(pg.Body, "next_page_token")
		if tok == "" {
			break
		}
	}
	if d := owner.do("DELETE", f.p("/snapshots/"+sid), nil); d.Status != 204 {
		t.Errorf("DELETE snapshot: %d %s", d.Status, d.Raw)
	}

	// Board-level reads and administration.
	liveCheck(t, "GET /permissions", "Permissions", 200, owner.do("GET", f.p("/permissions?channel=eng/backend"), nil))
	cl := owner.do("GET", f.p("/channels"), nil)
	liveCheck(t, "GET /channels", "ChannelList", 200, cl)
	if chs, _ := cl.Body["channels"].([]any); len(chs) > 0 {
		liveCheck(t, "GET /channels/{id}", "Channel", 200, owner.do("GET", f.p("/channels/"+str(chs[0].(J), "id")), nil))
	}
	liveCheck(t, "GET /boards/{id}/actors", "ActorList", 200, owner.do("GET", f.p("/actors"), nil))
	liveCheck(t, "GET /grants", "GrantList", 200, owner.do("GET", f.p("/grants?include_revoked=true"), nil))
	liveCheck(t, "GET /grants/{id}", "Grant", 200, owner.do("GET", f.p("/grants/"+grantID), nil))
	liveCheck(t, "PATCH /grants/{id}", "Grant", 200,
		sendJSON(t, owner, "PatchGrantRequest", "PATCH", f.p("/grants/"+grantID),
			J{"capabilities": []string{"read", "post", "label", "channel_manage", "react"}}, ifMatch(1)))
	liveCheck(t, "GET /boards/{id}/audit-events", "AuditEventList", 200, owner.do("GET", f.p("/audit-events"), nil))
	gb := owner.do("GET", f.p(""), nil)
	liveCheck(t, "GET /boards/{id}", "Board", 200, gb)
	liveCheck(t, "PATCH /boards/{id}", "Board", 200,
		sendJSON(t, owner, "PatchBoardRequest", "PATCH", f.p(""), J{"name": "renamed"},
			func(r *http.Request) { r.Header.Set("If-Match", gb.Header.Get("ETag")) }))
	liveCheck(t, "GET /boards", "BoardList", 200, owner.do("GET", "/v1/boards", nil))

	// Server-level identity routes.
	liveCheck(t, "POST /v1/actors", "Actor", 201,
		sendJSON(t, a, "CreateActorRequest", "POST", "/v1/actors", J{"kind": "service", "handle": "svc" + rnd()}, newKey()))
	liveCheck(t, "GET /v1/actors", "ActorList", 200, a.do("GET", "/v1/actors", nil))
	liveCheck(t, "GET /v1/actors/{id}", "Actor", 200, a.do("GET", "/v1/actors/"+owner.id, nil))
	credKey := newKey()
	credBody := J{"actor_id": owner.id, "label": "schema", "scopes": []string{"read"}, "board_ids": []string{f.board}, "expires_in_seconds": 3600}
	ic := sendJSON(t, a, "CreateCredentialRequest", "POST", "/v1/credentials", credBody, credKey)
	liveCheck(t, "POST /v1/credentials", "IssuedCredential", 201, ic)
	liveCheck(t, "POST /v1/credentials (replay)", "IssuedCredential", 201, a.do("POST", "/v1/credentials", credBody, credKey))
	liveCheck(t, "GET /v1/credentials", "CredentialList", 200, a.do("GET", "/v1/credentials?actor_id="+owner.id, nil))
	liveCheck(t, "GET /v1/audit-events", "AuditEventList", 200, a.do("GET", "/v1/audit-events", nil))
	if c, _ := ic.Body["credential"].(J); c != nil {
		liveCheck(t, "DELETE /v1/credentials/{id}", "Credential", 200, a.do("DELETE", "/v1/credentials/"+str(c, "id"), nil))
	}

	// Problems.
	liveCheck(t, "401 unauthenticated", "Problem", 401, anon.do("GET", "/v1/me", nil))
	liveCheck(t, "400 missing Idempotency-Key", "Problem", 400, a.do("POST", "/v1/boards", J{"name": "x"}))
	liveCheck(t, "400 unknown field", "Problem", 400, a.do("POST", "/v1/boards", J{"name": "x", "bogus": 1}, newKey()))
	liveCheck(t, "428 precondition_required", "Problem", 428, owner.do("PATCH", f.p(""), J{"name": "x"}))
	liveCheck(t, "404 unknown board", "Problem", 404, a.do("GET", "/v1/boards/brd_doesnotexist", nil))
	liveCheck(t, "404 no such route", "Problem", 404, a.do("GET", "/v1/nope", nil))
	na := a.do("PUT", "/v1/server", nil)
	liveCheck(t, "405 method_not_allowed", "Problem", 405, na)
	if na.code() != "method_not_allowed" {
		t.Errorf("PUT /v1/server: code %q, want method_not_allowed", na.code())
	}
	allow := false
	for _, m := range strings.Split(na.Header.Get("Allow"), ",") {
		allow = allow || strings.TrimSpace(m) == "GET"
	}
	if !allow {
		t.Errorf("PUT /v1/server: Allow %q does not contain GET", na.Header.Get("Allow"))
	}

	liveCheck(t, "DELETE /grants/{id}", "Grant", 200, owner.do("DELETE", f.p("/grants/"+grantID), nil))
}
