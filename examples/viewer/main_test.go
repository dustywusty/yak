package main

import (
	"bytes"
	"io/fs"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestProxyBoundary(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, k := range []string{"Authorization", "Idempotency-Key", "If-Match", "Yak-Claim-Proof", "Yak-Run-Id"} {
			if r.Header.Get(k) != "test-"+k {
				t.Errorf("lost %s", k)
			}
		}
		if r.Header.Get("Cookie") != "" {
			t.Error("cookie forwarded")
		}
		w.Header().Set("Set-Cookie", "secret=bad")
		w.WriteHeader(204)
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	for _, tc := range []struct {
		operator     bool
		method, path string
		want         int
	}{
		{false, "POST", "/v1/boards/b/tasks", 405}, {false, "GET", "/v1/me", 204}, {false, "POST", "/v1/boards/b/snapshots", 204},
		{false, "PATCH", "/v1/boards/b/snapshots/s", 405}, {true, "POST", "/v1/boards/b/tasks", 204},
		{true, "PATCH", "/v1/boards/b/tasks/t", 204}, {true, "DELETE", "/v1/credentials/self", 204},
		{true, "GET", "/v1/credentials", 403}, {true, "GET", "/v1/actors", 403}, {true, "POST", "/v1/boards/b/tasks/t/complete", 403},
		{true, "DELETE", "/v1/boards/b/messages/m", 403}, {true, "POST", "/v1/delegations", 403},
	} {
		r := httptest.NewRequest(tc.method, "http://localhost"+tc.path, nil)
		for _, k := range []string{"Authorization", "Idempotency-Key", "If-Match", "Yak-Claim-Proof", "Yak-Run-Id"} {
			r.Header.Set(k, "test-"+k)
		}
		r.Header.Set("Cookie", "x=y")
		w := httptest.NewRecorder()
		viewerHandler(target, tc.operator).ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%v %s %s: %d", tc.operator, tc.method, tc.path, w.Code)
		}
		if w.Header().Get("Set-Cookie") != "" {
			t.Error("response cookie")
		}
	}
	for _, headers := range []map[string]string{{"Origin": "https://evil.test"}, {"Sec-Fetch-Site": "same-site"}} {
		r := httptest.NewRequest("POST", "http://localhost/v1/boards/b/tasks", nil)
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		viewerHandler(target, true).ServeHTTP(w, r)
		if w.Code != 403 {
			t.Error("foreign write accepted")
		}
	}
	w := httptest.NewRecorder()
	viewerHandler(target, true).ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/", nil))
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "require-trusted-types-for 'script'") || w.Header().Get("X-Frame-Options") != "DENY" {
		t.Error("page security headers missing")
	}
	for _, addr := range []string{"0.0.0.0:8099", ":8099", "localhost:8099", "[::1]:8099"} {
		if validOperatorListen(addr) {
			t.Errorf("accepted %s", addr)
		}
	}
	if !validOperatorListen("127.0.0.1:8099") {
		t.Error("loopback rejected")
	}
}

func TestOperatorTargetRequiresTLSOffLoopback(t *testing.T) {
	for _, raw := range []string{"http://example.com:8090", "http://localhost:8090", "https://user:password@example.com", "http://127.0.0.1:8090/?secret=x"} {
		u, _ := url.Parse(raw)
		if validOperatorTarget(u) {
			t.Errorf("unsafe target accepted")
		}
	}
	for _, raw := range []string{"http://127.0.0.1:8090", "http://[::1]:8090", "https://example.com"} {
		u, _ := url.Parse(raw)
		if !validOperatorTarget(u) {
			t.Errorf("safe target refused")
		}
	}
}

func TestOperatorAllowlist(t *testing.T) {
	for method, paths := range map[string][]string{
		"GET":    {"/v1/server", "/v1/me", "/v1/boards", "/v1/boards/b/actors", "/v1/boards/b/tasks/t", "/v1/boards/b/threads/t/messages"},
		"POST":   {"/v1/boards/b/threads", "/v1/boards/b/threads/t/messages", "/v1/boards/b/tasks", "/v1/boards/b/tasks/t/unblock", "/v1/boards/b/tasks/t/claims", "/v1/boards/b/tasks/t/claims/c/renew", "/v1/boards/b/tasks/t/release"},
		"PATCH":  {"/v1/boards/b/messages/m", "/v1/boards/b/messages/m/labels", "/v1/boards/b/tasks/t"},
		"PUT":    {"/v1/boards/b/tasks/t/assignee", "/v1/boards/b/messages/m/reactions/like"},
		"DELETE": {"/v1/credentials/c", "/v1/boards/b/messages/m/reactions/like"},
	} {
		for _, path := range paths {
			if !allowedRoute(method, path, true) {
				t.Errorf("refused %s %s", method, path)
			}
		}
	}
	for _, path := range []string{"/v1/actors", "/v1/audit-events", "/v1/credentials", "/v1/delegations", "/v1/boards/b/grants", "/v1/boards/b/audit-events"} {
		for _, method := range []string{"GET", "POST", "PATCH", "DELETE"} {
			if allowedRoute(method, path, true) {
				t.Errorf("allowed %s %s", method, path)
			}
		}
	}
	for _, path := range []string{"/v1/boards", "/v1/boards/b/channels", "/v1/boards/b/tasks/t/block", "/v1/boards/b/tasks/t/cancel", "/v1/boards/b/tasks/t/reopen", "/v1/boards/b/tasks/t/complete", "/v1/boards/b/tasks/t/fail", "/v1/boards/b/tasks/t/briefs", "/v1/boards/b/tasks/t/root-redaction", "/v1/boards/b/tasks/batch"} {
		if allowedRoute("POST", path, true) {
			t.Errorf("allowed forbidden write %s", path)
		}
	}
}

func TestNoSecretLogsOrHTMLSinks(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	target, _ := url.Parse(upstream.URL)
	upstream.Close()
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	r := httptest.NewRequest("POST", "http://localhost/v1/boards/b/tasks/t/release", nil)
	r.Header.Set("Authorization", "Bearer NEVER-LOG-TOKEN")
	r.Header.Set("Yak-Claim-Proof", "NEVER-LOG-PROOF")
	w := httptest.NewRecorder()
	viewerHandler(target, true).ServeHTTP(w, r)
	if w.Code != 502 || strings.Contains(logs.String(), "NEVER-LOG") || strings.Contains(logs.String(), upstream.URL) {
		t.Fatal("unsafe upstream error handling")
	}
	err := fs.WalkDir(staticFiles, "static", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(path, ".js") {
			b, e := staticFiles.ReadFile(path)
			if e != nil {
				return e
			}
			for _, sink := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write(", "eval("} {
				if bytes.Contains(b, []byte(sink)) {
					t.Errorf("HTML/script sink in %s", path)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOP17001(t *testing.T) {
	const base = "/v1/boards/b/messages/m/reactions/"
	keys := []string{"like", "+1", "%2B1", "%F0%9F%91%8D"}
	rejected := []string{"", "%", "%2", "%GG", "%2f", "%2F", "%5c", "%5C", "%252f", "%252B1", ".", "..", "%2e", "%2E%2e", "a%2e%2eb", "%00", "%1f", "%7f", "%C2%85", "%FF", "%C0%AF", "like/extra", "like?x=y", "like?", "like#fragment", "a\\b"}
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		if r.RequestURI != r.Header.Get("X-Expected-URI") {
			t.Errorf("escaped path changed: got %q want %q", r.RequestURI, r.Header.Get("X-Expected-URI"))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	handler := viewerHandler(target, true)
	for _, method := range []string{"PUT", "DELETE"} {
		for _, key := range keys {
			t.Run(method+"/accept/"+key, func(t *testing.T) {
				path := base + key
				if !allowedRoute(method, path, true) {
					t.Error("guard refused valid key")
				}
				if allowedRoute(method, path, false) {
					t.Error("read-only guard allowed reaction")
				}
				r := httptest.NewRequest(method, path, nil)
				r.Header.Set("X-Expected-URI", path)
				w := httptest.NewRecorder()
				before := upstreamCalls
				handler.ServeHTTP(w, r)
				if w.Code != 204 || upstreamCalls != before+1 {
					t.Errorf("valid reaction did not reach upstream: status=%d calls=%d", w.Code, upstreamCalls-before)
				}
			})
		}
		for _, key := range rejected {
			t.Run(method+"/refuse/"+key, func(t *testing.T) {
				path := base + key
				if allowedRoute(method, path, true) {
					t.Errorf("guard accepted unsafe key %q", key)
				}
				// Malformed escapes are refused by net/http's URL parser before the handler.
				if _, err := url.ParseRequestURI(path); err != nil {
					return
				}
				r := httptest.NewRequest(method, path, nil)
				w := httptest.NewRecorder()
				before := upstreamCalls
				handler.ServeHTTP(w, r)
				if w.Code == 204 || upstreamCalls != before {
					t.Errorf("unsafe key reached upstream: %q", key)
				}
			})
		}
	}
	for _, method := range []string{"GET", "POST", "PATCH", "HEAD", "OPTIONS"} {
		for _, key := range keys {
			if allowedRoute(method, base+key, true) {
				t.Errorf("unexpected method allowed: %s %s", method, key)
			}
		}
	}
	for _, path := range []string{"/v1/boards/b%2fx/messages/m/reactions/like", "/v1/boards/b/messages/m%2Fx/reactions/like", "/v1/boards/b%41/messages/m/reactions/like", "/v1/boards/b/tasks/t%41/assignee", "/v1/boards/b/snapshots/s%41", "/v1/boards/b/messages/m%41"} {
		for _, method := range []string{"GET", "POST", "PATCH", "PUT", "DELETE"} {
			if allowedRoute(method, path, true) {
				t.Errorf("encoded non-key segment allowed: %s %s", method, path)
			}
		}
	}
}

// TestOP17002 covers response-wide headers on local, proxy and error branches.
func TestOP17002(t *testing.T) {
	required := map[string]string{
		"X-Frame-Options":        "DENY",
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			t.Error("request cookie forwarded")
		}
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("X-Content-Type-Options", "unsafe")
		w.Header().Set("Referrer-Policy", "unsafe-url")
		w.Header().Set("Set-Cookie", "synthetic=refused")
		w.Header().Set("Cache-Control", "private, max-age=17")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	unavailable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadTarget, _ := url.Parse(unavailable.URL)
	unavailable.Close()
	cases := []struct {
		name, method, path string
		status             int
		page, dead         bool
	}{
		{"config", "GET", "/viewer-config", 200, false, false},
		{"upstream-hostile-headers", "GET", "/v1/me", 200, false, false},
		{"denied-write", "POST", "/v1/delegations", 0, false, false},
		{"foreign-write", "POST", "/v1/boards/b/snapshots", 403, false, false},
		{"missing-static", "GET", "/missing-op17002.txt", 404, true, false},
		{"static-non-get", "POST", "/", 405, false, false},
		{"unreachable", "GET", "/v1/me", 502, false, true},
		{"page", "GET", "/", 200, true, false},
		{"static", "GET", "/app.js", 200, true, false},
	}
	assertions := 0
	for _, operator := range []bool{false, true} {
		mode := "readonly"
		if operator {
			mode = "operator"
		}
		for _, tc := range cases {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				selectedTarget := target
				if tc.dead {
					selectedTarget = deadTarget
				}
				r := httptest.NewRequest(tc.method, "http://localhost"+tc.path, nil)
				r.Header.Set("Cookie", "synthetic=refused")
				if tc.name == "foreign-write" {
					r.Header.Set("Origin", "https://synthetic.invalid")
				}
				w := httptest.NewRecorder()
				viewerHandler(selectedTarget, operator).ServeHTTP(w, r)
				result := w.Result()
				defer result.Body.Close()
				status := tc.status
				if status == 0 {
					status = 405
					if operator {
						status = 403
					}
				}
				assertions++
				if result.StatusCode != status {
					t.Errorf("status = %d, want %d", result.StatusCode, status)
				}
				for name, want := range required {
					assertions++
					values := result.Header.Values(name)
					if len(values) != 1 || values[0] != want {
						t.Errorf("%s = %q, want exactly [%q]", name, values, want)
					}
				}
				assertions++
				if result.Header.Get("Set-Cookie") != "" {
					t.Error("response cookie accepted")
				}
				if tc.page {
					assertions++
					if got := result.Header.Get("Content-Security-Policy"); got != csp {
						t.Errorf("CSP = %q, want %q", got, csp)
					}
				}
				if tc.name == "upstream-hostile-headers" {
					assertions++
					if got := result.Header.Get("Cache-Control"); got != "private, max-age=17" {
						t.Errorf("API caching changed: %q", got)
					}
				}
			})
		}
	}
	if assertions == 0 {
		t.Fatal("no header assertions executed")
	}
	t.Logf("OP17-002 executed %d response assertions across %d cases", assertions, len(cases)*2)
}

// TestOP17038 checks route refusal precedence without weakening write-origin checks.
func TestOP17038(t *testing.T) {
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	routes := []struct {
		name, method, path string
		readonly, operator bool
	}{
		{"snapshot-create", "POST", "/v1/boards/b/snapshots", true, true},
		{"snapshot-delete", "DELETE", "/v1/boards/b/snapshots/s", true, true},
		{"read", "GET", "/v1/me", true, true},
		{"task-create", "POST", "/v1/boards/b/tasks", false, true},
		{"unsupported-route", "POST", "/v1/delegations", false, false},
		{"unsupported-method", "PATCH", "/v1/boards/b/snapshots/s", false, false},
	}
	origins := []struct{ name, value string }{
		{"absent", ""}, {"same", "http://localhost"}, {"foreign", "https://foreign.invalid"},
	}
	fetchSites := []struct{ name, value string }{
		{"absent", ""}, {"same-origin", "same-origin"}, {"same-site", "same-site"}, {"cross-site", "cross-site"},
	}
	cases, assertions := 0, 0
	for _, operator := range []bool{false, true} {
		mode := "readonly"
		if operator {
			mode = "operator"
		}
		handler := viewerHandler(target, operator)
		for _, route := range routes {
			for _, origin := range origins {
				for _, fetchSite := range fetchSites {
					t.Run(mode+"/"+route.name+"/origin-"+origin.name+"/fetch-"+fetchSite.name, func(t *testing.T) {
						cases++
						supported := route.readonly
						if operator {
							supported = route.operator
						}
						wantStatus, wantCalls := http.StatusNoContent, int64(1)
						if !supported {
							wantStatus, wantCalls = http.StatusMethodNotAllowed, 0
							if operator {
								wantStatus = http.StatusForbidden
							}
						} else if route.method != "GET" && (origin.name == "foreign" || fetchSite.name == "same-site" || fetchSite.name == "cross-site") {
							wantStatus, wantCalls = http.StatusForbidden, 0
						}
						r := httptest.NewRequest(route.method, "http://localhost"+route.path, nil)
						if origin.value != "" {
							r.Header.Set("Origin", origin.value)
						}
						if fetchSite.value != "" {
							r.Header.Set("Sec-Fetch-Site", fetchSite.value)
						}
						w := httptest.NewRecorder()
						before := upstreamCalls.Load()
						handler.ServeHTTP(w, r)
						assertions++
						if w.Code != wantStatus {
							t.Errorf("status = %d, want %d", w.Code, wantStatus)
						}
						assertions++
						if calls := upstreamCalls.Load() - before; calls != wantCalls {
							t.Errorf("upstream calls = %d, want %d", calls, wantCalls)
						}
						if wantCalls == 0 {
							wantCode := "forbidden"
							if wantStatus == http.StatusMethodNotAllowed {
								wantCode = "method_not_allowed"
							}
							assertions++
							if !strings.Contains(w.Body.String(), `"code":"`+wantCode+`"`) {
								t.Errorf("problem body = %s, want code %q", w.Body.String(), wantCode)
							}
						}
					})
				}
			}
		}
	}
	if cases != 144 || assertions == 0 {
		t.Fatalf("incomplete matrix: cases=%d assertions=%d", cases, assertions)
	}
	t.Logf("OP17-038 executed %d cases and %d assertions", cases, assertions)
}

// TestOP17045 checks method-and-route pairs at both the guard and proxy boundary.
func TestOP17045(t *testing.T) {
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	handler := viewerHandler(target, true)
	cases, assertions := 0, 0
	check := func(kind, method, path string, allowed bool) {
		t.Helper()
		t.Run(kind+"/"+method+path, func(t *testing.T) {
			cases++
			assertions++
			if got := allowedRoute(method, path, true); got != allowed {
				t.Errorf("allowedRoute(%q, %q) = %v, want %v", method, path, got, allowed)
			}
			before := upstreamCalls.Load()
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(method, path, nil))
			wantStatus, wantCalls := http.StatusForbidden, int64(0)
			if allowed {
				wantStatus, wantCalls = http.StatusNoContent, 1
			}
			assertions++
			if w.Code != wantStatus {
				t.Errorf("status = %d, want %d", w.Code, wantStatus)
			}
			assertions++
			if calls := upstreamCalls.Load() - before; calls != wantCalls {
				t.Errorf("upstream calls = %d, want %d", calls, wantCalls)
			}
			if !allowed {
				assertions++
				if !strings.Contains(w.Body.String(), `"code":"forbidden"`) || !strings.Contains(w.Body.String(), `"reason":"operator_route_not_allowed"`) {
					t.Errorf("missing allowlist problem: %s", w.Body.String())
				}
			}
		})
	}
	// Keep explicit positives: rejecting every PUT must not satisfy this test.
	for method, paths := range map[string][]string{
		"GET":    {"/v1/server", "/v1/me", "/v1/boards", "/v1/boards/b", "/v1/boards/b/actors", "/v1/boards/b/channels", "/v1/boards/b/permissions", "/v1/boards/b/events", "/v1/boards/b/threads", "/v1/boards/b/threads/t", "/v1/boards/b/threads/t/messages", "/v1/boards/b/messages/m", "/v1/boards/b/tasks", "/v1/boards/b/tasks/t", "/v1/boards/b/tasks/t/briefs", "/v1/boards/b/tasks/t/briefs/f", "/v1/boards/b/snapshots/s"},
		"POST":   {"/v1/boards/b/snapshots", "/v1/boards/b/threads", "/v1/boards/b/threads/t/messages", "/v1/boards/b/tasks", "/v1/boards/b/tasks/t/unblock", "/v1/boards/b/tasks/t/claims", "/v1/boards/b/tasks/t/claims/c/renew", "/v1/boards/b/tasks/t/release"},
		"PATCH":  {"/v1/boards/b/messages/m", "/v1/boards/b/messages/m/labels", "/v1/boards/b/tasks/t"},
		"PUT":    {"/v1/boards/b/tasks/t/assignee", "/v1/boards/b/messages/m/reactions/like"},
		"DELETE": {"/v1/credentials/c", "/v1/boards/b/messages/m/reactions/like", "/v1/boards/b/snapshots/s"},
	} {
		for _, path := range paths {
			check("allowed", method, path, true)
		}
	}
	methods := []string{"GET", "POST", "PATCH", "PUT", "DELETE", "HEAD", "OPTIONS", "CONNECT", "TRACE"}
	for _, path := range []string{
		"/v1/actors", "/v1/actors/a", "/v1/audit-events", "/v1/credentials",
		"/v1/delegations", "/v1/delegations/d", "/v1/delegations/d/mint",
		"/v1/boards/b/grants", "/v1/boards/b/grants/g", "/v1/boards/b/audit-events",
	} {
		for _, method := range methods {
			check("forbidden", method, path, false)
		}
	}
	// These routes allow some verbs, but must reject every other listed verb.
	for _, route := range []struct{ path, verbs string }{
		{"/v1/credentials/c", " DELETE "},
		{"/v1/server", " GET "},
		{"/v1/boards", " GET "},
		{"/v1/boards/b", " GET "},
		{"/v1/boards/b/channels", " GET "},
		{"/v1/boards/b/tasks", " GET POST "},
		{"/v1/boards/b/tasks/t", " GET PATCH "},
		{"/v1/boards/b/tasks/t/assignee", " PUT "},
		{"/v1/boards/b/messages/m/reactions/like", " PUT DELETE "},
		{"/v1/boards/b/messages/m", " GET PATCH "},
		{"/v1/boards/b/messages/m/labels", " PATCH "},
		{"/v1/boards/b/tasks/t/briefs", " GET "},
		{"/v1/boards/b/tasks/t/root-redaction", " "},
		{"/v1/boards/b/schemas", " "},
	} {
		for _, method := range methods {
			if !strings.Contains(route.verbs, " "+method+" ") {
				check("wrong-method", method, route.path, false)
			}
		}
	}
	t.Logf("OP17-045 executed cases=%d assertions=%d upstream_calls=%d", cases, assertions, upstreamCalls.Load())
	if cases == 0 || assertions == 0 {
		t.Fatal("allowlist matrix did not execute")
	}
}
