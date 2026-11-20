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
		// P2 (abby.p2.tasks)
		"TaskState", "Task", "TaskPage", "Claim", "ClaimPage", "ClaimResponse", "ClaimNextResult", "Brief", "BriefWithTask",
		"BriefPage", "TaskBatch", "CreateTaskRequest", "CreateTaskBatchRequest", "PatchTaskRequest", "ClaimRequest",
		"ClaimNextRequest", "RenewClaimRequest", "ReleaseTaskRequest", "CompleteTaskRequest", "FailTaskRequest",
		"BlockTaskRequest", "UnblockTaskRequest", "CancelTaskRequest", "ReopenTaskRequest", "SetAssigneeRequest",
		"SetDependenciesRequest", "CreateBriefRequest", "RootRedactionRequest",
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
		// P2: closed requests, exactly-one-of create, proofs never in bodies, typed states.
		{"CreateTaskRequest", `{"thread_id":"t","thread":{"channel":"eng","title":"x","text":"y"}}`},
		{"CreateTaskRequest", `{}`},
		{"CreateTaskRequest", `{"thread":{"channel":"eng","title":"x"}}`},
		{"CreateTaskRequest", `{"thread":{"channel":"eng","title":"x","text":"y","labels":{"abby.task.state":"done"}}}`},
		{"CreateTaskBatchRequest", `{"tasks":[]}`},
		{"CreateTaskBatchRequest", `{"tasks":[{"ref":"bad ref","thread":{"channel":"eng","title":"x","text":"y"}}]}`},
		{"CreateTaskBatchRequest", `{"tasks":[{"ref":"a","thread":{"channel":"eng","title":"x","text":"y"},"depends_on":[{"ref":"b","task_id":"t"}]}]}`},
		{"PatchTaskRequest", `{}`},
		{"PatchTaskRequest", `{"state":"completed"}`},
		{"CompleteTaskRequest", `{"result":{},"proof":"acp1.c.s"}`},
		{"CompleteTaskRequest", `{"result":{"evidence":[{"kind":"guess","ref":"x"}]}}`},
		{"FailTaskRequest", `{}`},
		{"BlockTaskRequest", `{"reason":""}`},
		{"CancelTaskRequest", `{}`},
		{"SetAssigneeRequest", `{}`},
		{"SetDependenciesRequest", `{"task_ids":"t"}`},
		{"ClaimNextRequest", `{"assignee":"anyone"}`},
		{"ClaimNextRequest", `{"labels":{"area":"api"}}`},
		{"ClaimRequest", `{"lease_seconds":0}`},
		{"TaskState", `"done"`},
		{"Claim", `{"id":"c","task_id":"t","actor_id":"a","execution_id":"e","run_id":null,"epoch":1,"brief_revision":1,
			"status":"active","revision":1,"lease_seconds":120,"acquired_at":"2026-11-19T20:00:00Z","expires_at":"2026-11-19T20:02:00Z",
			"ended_at":"2026-11-19T20:01:00Z","end_recorded_at":null}`},
		{"ClaimResponse", `{"outcome":"claimed","claim":{},"proof":null,"proof_unavailable":false,"brief":{},"task":{}}`},
		{"ClaimNextResult", `{"outcome":"no_work"}`},
		{"Brief", `{"task_id":"t","revision":1,"text":"x","redacted":true,"source":{"kind":"root","message_id":"m","message_revision":1},
			"reason":null,"created_by_actor_id":"a","created_at":"2026-11-19T20:00:00Z"}`},
	}
	for _, c := range cases {
		if err := schemaNamed(t, c.schema).Validate(decodeInstance(t, []byte(c.doc))); err == nil {
			t.Errorf("%s accepted invalid document %s", c.schema, c.doc)
		}
	}

	// P2 task-view invariants, checked on mutated copies of valid examples:
	// state claimed <=> an active current_claim (TASK-023), no proof or
	// unknown state, and Discovery requires the P2 limits once the profile is
	// listed (TASK-002).
	mutate := func(example string, edit func(J)) string {
		raw, err := os.ReadFile(filepath.Join(specDir, "examples", example+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var m J
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		edit(m)
		out, _ := json.Marshal(m)
		return string(out)
	}
	for _, c := range []struct {
		schema, example string
		edit            func(J)
	}{
		{"Task", "Task.claimed", func(m J) { m["current_claim"] = nil }},
		{"Task", "Task.completed", func(m J) { m["current_claim"] = m["last_claim"] }},
		{"Task", "Task.claimed", func(m J) { m["state"] = "done" }},
		{"Task", "Task.claimed", func(m J) { delete(m, "unreadable_dependencies_pending") }},
		{"Task", "Task.claimed", func(m J) { m["last_claim"].(J)["status"] = "active" }},
		{"Discovery", "Discovery.p2", func(m J) { delete(m["limits"].(J), "claim_lease_max_seconds") }},
		{"Event", "Event.task_claimed", func(m J) { m["payload"].(J)["claim"] = nil }},
		{"Event", "Event.task_claim_renewed", func(m J) { m["resource"].(J)["type"] = "task" }},
	} {
		doc := mutate(c.example, c.edit)
		if err := schemaNamed(t, c.schema).Validate(decodeInstance(t, []byte(doc))); err == nil {
			t.Errorf("%s accepted invalid mutation of %s: %s", c.schema, c.example, doc)
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
				params := opParams(item.(map[string]any), op)
				switch x["idempotency"] {
				case "required":
					if !params["IdempotencyKey"] {
						t.Errorf("%s: x-abby.idempotency required but no IdempotencyKey parameter", where)
					}
				case "optional":
					if !params["IdempotencyKeyOptional"] {
						t.Errorf("%s: x-abby.idempotency optional but no IdempotencyKeyOptional parameter", where)
					}
				case "not_applicable":
				default:
					t.Errorf("%s: x-abby.idempotency %v", where, x["idempotency"])
				}
				switch pre := x["precondition"]; pre {
				case nil, "none":
				case "If-Match required":
					if !params["IfMatchRequired"] {
						t.Errorf("%s: precondition %v but no IfMatchRequired parameter", where, pre)
					}
				case "If-Match optional":
					if !params["IfMatchOptional"] {
						t.Errorf("%s: precondition %v but no IfMatchOptional parameter", where, pre)
					}
				default:
					t.Errorf("%s: x-abby.precondition %v", where, pre)
				}
				// P2: the Abby-Claim-Proof header and x-abby.claim_proof agree.
				switch cp := x["claim_proof"]; cp {
				case nil:
					if params["AbbyClaimProof"] || params["AbbyClaimProofOptional"] {
						t.Errorf("%s: takes Abby-Claim-Proof but x-abby.claim_proof is not set", where)
					}
				case "required":
					if !params["AbbyClaimProof"] {
						t.Errorf("%s: x-abby.claim_proof required but no AbbyClaimProof parameter", where)
					}
				case "optional":
					if !params["AbbyClaimProofOptional"] {
						t.Errorf("%s: x-abby.claim_proof optional but no AbbyClaimProofOptional parameter", where)
					}
				default:
					t.Errorf("%s: x-abby.claim_proof %v", where, cp)
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

// opParams returns the names of the components.parameters referenced by a
// path item and one of its operations.
func opParams(item, op map[string]any) map[string]bool {
	out := map[string]bool{}
	for _, src := range []map[string]any{item, op} {
		ps, _ := src["parameters"].([]any)
		for _, p := range ps {
			m, _ := p.(map[string]any)
			if ref, _ := m["$ref"].(string); strings.HasPrefix(ref, "#/components/parameters/") {
				out[strings.TrimPrefix(ref, "#/components/parameters/")] = true
			}
		}
	}
	return out
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

// advertisesProfile reports whether a GET /v1/server body lists profile in
// protocol.profiles.
func advertisesProfile(disc J, profile string) bool {
	proto, _ := disc["protocol"].(J)
	ps, _ := proto["profiles"].([]any)
	for _, p := range ps {
		if p == profile {
			return true
		}
	}
	return false
}

// TestSpecLiveResponsesP2 validates live P2 (abby.p2.tasks) responses against
// the schemas. It runs only against a server that advertises the profile.
func TestSpecLiveResponsesP2(t *testing.T) {
	a := admin(t)
	anon := &actorClient{t: t}
	disc := anon.do("GET", "/v1/server", nil)
	if !advertisesProfile(disc.Body, "abby.p2.tasks") {
		t.Skip("server does not list abby.p2.tasks in protocol.profiles; P2 live schema checks skipped")
	}
	liveCheck(t, "GET /v1/server (P2 limits)", "Discovery", 200, disc)

	s := func(m J, k string) string { v, _ := m[k].(string); return v }
	o := func(m J, k string) J { v, _ := m[k].(J); return v }
	withIfMatch := func(etag string) opt { return func(r *http.Request) { r.Header.Set("If-Match", etag) } }
	withProof := func(p string) opt { return header("Abby-Claim-Proof", p) }

	owner := newActor(t, "owner")
	mgr := newActor(t, "mgr")
	worker := newActor(t, "worker")
	br := a.must(201, a.do("POST", "/v1/boards", J{"name": "schema p2 " + rnd(), "owner_actor_id": owner.id}, newKey()))
	f := &fixture{board: s(br.Body, "id"), owner: owner, grants: map[string]string{}}
	f.grant(t, mgr, "*", "allow", "read", "post", "label", "task_manage", "channel_manage")
	f.grant(t, worker, "*", "allow", "read", "post", "claim")

	// Create (L1) and read.
	cr := sendJSON(t, mgr, "CreateTaskRequest", "POST", f.p("/tasks"),
		J{"thread": J{"channel": "eng", "title": "Task one", "text": "Do the first thing.", "labels": J{"area": "api"}}}, newKey())
	liveCheck(t, "POST /tasks", "Task", 201, cr)
	t1, thread1 := s(cr.Body, "id"), s(cr.Body, "thread_id")
	if t1 == "" {
		t.Fatalf("task creation failed: %s", cr.Raw)
	}
	gt := worker.do("GET", f.p("/tasks/"+t1), nil)
	liveCheck(t, "GET /tasks/{id}", "Task", 200, gt)
	if want := fmt.Sprintf(`"%v"`, gt.Body["revision"]); gt.Header.Get("ETag") != want {
		t.Errorf("GET task ETag %q, want %s", gt.Header.Get("ETag"), want)
	}
	liveCheck(t, "GET /tasks", "TaskPage", 200, worker.do("GET", f.p("/tasks?state=ready&limit=10"), nil))
	th := worker.do("GET", f.p("/threads/"+thread1), nil)
	liveCheck(t, "GET /threads/{id} (task thread)", "ThreadWithRoot", 200, th)
	if got := o(th.Body, "thread")["task_id"]; got != t1 {
		t.Errorf("thread.task_id = %v, want %s (TASK-024)", got, t1)
	}
	liveCheck(t, "GET /tasks/{id}/briefs", "BriefPage", 200, worker.do("GET", f.p("/tasks/"+t1+"/briefs"), nil))
	liveCheck(t, "GET /tasks/{id}/briefs/1", "Brief", 200, worker.do("GET", f.p("/tasks/"+t1+"/briefs/1"), nil))

	// Claim, renew, complete (L3, L4, L7) and claimant problems.
	cl := sendJSON(t, worker, "ClaimRequest", "POST", f.p("/tasks/"+t1+"/claims"), J{"expect_brief_revision": 1}, newKey())
	liveCheck(t, "POST /tasks/{id}/claims", "ClaimResponse", 201, cl)
	pr, claimID := s(cl.Body, "proof"), s(o(cl.Body, "claim"), "id")
	liveCheck(t, "POST /tasks/{id}/claims (task_claimed)", "Problem", 409,
		sendJSON(t, worker, "ClaimRequest", "POST", f.p("/tasks/"+t1+"/claims"), J{}, newKey()))
	liveCheck(t, "POST /claims/{id}/renew", "Claim", 200,
		sendJSON(t, worker, "RenewClaimRequest", "POST", f.p("/tasks/"+t1+"/claims/"+claimID+"/renew"), J{}, newKey(), withProof(pr)))
	missing := sendJSON(t, worker, "CompleteTaskRequest", "POST", f.p("/tasks/"+t1+"/complete"), J{"result": J{}}, newKey())
	liveCheck(t, "POST /tasks/{id}/complete (no proof)", "Problem", 428, missing)
	if missing.code() != "precondition_required" {
		t.Errorf("complete without Abby-Claim-Proof: code %q, want precondition_required (CLAIM-013)", missing.code())
	}
	liveCheck(t, "POST /tasks/{id}/complete", "Task", 200,
		sendJSON(t, worker, "CompleteTaskRequest", "POST", f.p("/tasks/"+t1+"/complete"), J{"result": J{
			"summary": "done", "evidence": []J{{"kind": "commit", "ref": "abc123"}}, "data": J{"ok": true},
		}}, newKey(), withProof(pr)))
	lost := sendJSON(t, worker, "CompleteTaskRequest", "POST", f.p("/tasks/"+t1+"/complete"), J{"result": J{}}, newKey(), withProof(pr))
	liveCheck(t, "POST /tasks/{id}/complete (claim_lost)", "Problem", 409, lost)
	if lost.code() != "claim_lost" {
		t.Errorf("settled proof: code %q, want claim_lost (CLAIM-014)", lost.code())
	}
	liveCheck(t, "GET /tasks/{id}/claims", "ClaimPage", 200, worker.do("GET", f.p("/tasks/"+t1+"/claims"), nil))
	if bytes.Contains(worker.do("GET", f.p("/tasks/"+t1+"/claims"), nil).Raw, []byte(pr)) {
		t.Error("claim history contains the proof (CLAIM-011)")
	}

	// Claim-next: both outcomes, then release (SEL-007, L5).
	cr2 := sendJSON(t, mgr, "CreateTaskRequest", "POST", f.p("/tasks"),
		J{"thread": J{"channel": "eng/next", "title": "Task two", "text": "Do the second thing."}, "depends_on": []string{t1}}, newKey())
	liveCheck(t, "POST /tasks (with completed prerequisite)", "Task", 201, cr2)
	t2 := s(cr2.Body, "id")
	cn := sendJSON(t, worker, "ClaimNextRequest", "POST", f.p("/tasks/claim-next"), J{"channel": "eng/next", "assignee": "eligible"}, newKey())
	liveCheck(t, "POST /tasks/claim-next (claimed)", "ClaimNextResult", 201, cn)
	if cn.Body["outcome"] != "claimed" || s(o(cn.Body, "task"), "id") != t2 {
		t.Errorf("claim-next: want outcome claimed on %s, got %s", t2, cn.Raw)
	}
	nw := sendJSON(t, worker, "ClaimNextRequest", "POST", f.p("/tasks/claim-next"), J{"channel": "eng/next"}, newKey())
	liveCheck(t, "POST /tasks/claim-next (no_work)", "ClaimNextResult", 200, nw)
	if nw.Body["outcome"] != "no_work" {
		t.Errorf("claim-next with nothing ready: want outcome no_work, got %s", nw.Raw)
	}
	rel := sendJSON(t, worker, "ReleaseTaskRequest", "POST", f.p("/tasks/"+t2+"/release"), J{"reason": "schema check"}, newKey(), withProof(s(cn.Body, "proof")))
	liveCheck(t, "POST /tasks/{id}/release", "Task", 200, rel)

	// Manager commands (L9, L11, L12, L13, L15, L16, L17, L19).
	etag := rel.Header.Get("ETag")
	if etag == "" {
		etag = fmt.Sprintf(`"%v"`, rel.Body["revision"])
	}
	step := func(name, reqSchema, method, path string, body J, status int, respSchema string, opts ...opt) resp {
		t.Helper()
		r := sendJSON(t, mgr, reqSchema, method, f.p("/tasks/"+t2+path), body, opts...)
		liveCheck(t, name, respSchema, status, r)
		if e := r.Header.Get("ETag"); e != "" {
			etag = e
		} else if tk := o(r.Body, "task"); tk != nil {
			etag = fmt.Sprintf(`"%v"`, tk["revision"])
		}
		return r
	}
	step("PUT /tasks/{id}/assignee", "SetAssigneeRequest", "PUT", "/assignee", J{"assignee_actor_id": worker.id}, 200, "Task", withIfMatch(etag))
	step("PUT /tasks/{id}/dependencies", "SetDependenciesRequest", "PUT", "/dependencies", J{"task_ids": []string{t1}}, 200, "Task", withIfMatch(etag))
	step("PATCH /tasks/{id}", "PatchTaskRequest", "PATCH", "", J{"title": "Task two (renamed)"}, 200, "Task", withIfMatch(etag))
	step("POST /tasks/{id}/block", "BlockTaskRequest", "POST", "/block", J{"reason": "schema check"}, 200, "Task", newKey())
	step("POST /tasks/{id}/unblock", "UnblockTaskRequest", "POST", "/unblock", J{}, 200, "Task", newKey())
	step("POST /tasks/{id}/briefs", "CreateBriefRequest", "POST", "/briefs", J{"text": "Do the second thing, v2.", "reason": "scope"}, 201, "BriefWithTask",
		newKey(), withIfMatch(etag))
	step("POST /tasks/{id}/cancel", "CancelTaskRequest", "POST", "/cancel", J{"reason": "schema check"}, 200, "Task", newKey())
	step("POST /tasks/{id}/reopen", "ReopenTaskRequest", "POST", "/reopen", J{}, 200, "Task", newKey())
	inv := sendJSON(t, mgr, "UnblockTaskRequest", "POST", f.p("/tasks/"+t2+"/unblock"), J{}, newKey())
	liveCheck(t, "POST /tasks/{id}/unblock (invalid_transition)", "Problem", 409, inv)

	// Batch and P1 root protection.
	liveCheck(t, "POST /tasks/batch", "TaskBatch", 201,
		sendJSON(t, mgr, "CreateTaskBatchRequest", "POST", f.p("/tasks/batch"), J{"tasks": []J{
			{"ref": "a", "thread": J{"channel": "eng", "title": "Batch A", "text": "a"}},
			{"ref": "b", "thread": J{"channel": "eng", "title": "Batch B", "text": "b"}, "depends_on": []J{{"ref": "a"}, {"task_id": t1}}},
		}}, newKey()))
	prot := sendJSON(t, mgr, "PatchThreadRequest", "PATCH", f.p("/threads/"+thread1), J{"title": "nope"}, withIfMatch(th.Header.Get("ETag")))
	liveCheck(t, "PATCH /threads/{id} (task_root_protected)", "Problem", 409, prot)
	if prot.code() != "task_root_protected" {
		t.Errorf("PATCH task thread: code %q, want task_root_protected (BRIEF-010)", prot.code())
	}

	// Events: every event (and so every task payload) validates.
	er := mgr.do("GET", f.p("/events?limit=200"), nil)
	liveCheck(t, "GET /events (P2)", "EventPage", 200, er)
	evs, _ := er.Body["events"].([]any)
	seen := map[string]bool{}
	for i, e := range evs {
		raw, _ := json.Marshal(e)
		typ := s(e.(J), "type")
		seen[typ] = true
		if typ == "thread.view_invalidated" && o(e.(J), "payload")["reason"] == "task_state_changed" {
			seen["thread.view_invalidated/task_state_changed"] = true
		}
		if bytes.Contains(raw, []byte(pr)) {
			t.Errorf("event[%d] %s contains the claim proof (TASK-084)", i, typ)
		}
		liveCheck(t, fmt.Sprintf("event[%d] %s", i, typ), "Event", 200, resp{Status: 200, Header: er.Header, Raw: raw})
	}
	for _, typ := range []string{"task.created", "task.claimed", "task.claim_renewed", "task.completed", "task.released",
		"task.assigned", "task.dependencies_changed", "task.updated", "task.blocked", "task.unblocked", "task.brief_updated",
		"task.canceled", "task.reopened", "thread.view_invalidated/task_state_changed"} {
		if !seen[typ] {
			t.Errorf("event %s not observed", typ)
		}
	}

	// Snapshot with task items (TASK-092).
	sr := mgr.do("POST", f.p("/snapshots?subtree=eng"), nil)
	liveCheck(t, "POST /snapshots (P2)", "SnapshotInfo", 201, sr)
	tok, sawTask := "", false
	for i := 0; i < 20; i++ {
		q := url.Values{"limit": {"200"}}
		if tok != "" {
			q.Set("page_token", tok)
		}
		pg := mgr.do("GET", f.p("/snapshots/"+s(sr.Body, "id")+"?"+q.Encode()), nil)
		liveCheck(t, fmt.Sprintf("GET /snapshots/{id} (P2) page %d", i), "SnapshotPage", 200, pg)
		items, _ := pg.Body["items"].([]any)
		for _, it := range items {
			sawTask = sawTask || s(it.(J), "type") == "task"
		}
		if tok = s(pg.Body, "next_page_token"); tok == "" {
			break
		}
	}
	if !sawTask {
		t.Error("snapshot of task threads has no task items (TASK-092)")
	}
}
