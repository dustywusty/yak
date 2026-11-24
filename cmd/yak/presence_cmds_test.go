package main

import (
	"encoding/json"
	"github.com/spf13/cobra"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestPresenceCLIUsesBoardScopedRoutes(t *testing.T) {
	for _, tc := range []struct {
		args         []string
		method, path string
		state        string
		ttl          bool
	}{
		{[]string{"presence"}, "GET", "/v1/boards/b/actors/self/presence", "", false},
		{[]string{"presence", "other"}, "GET", "/v1/boards/b/actors/other/presence", "", false},
		{[]string{"presence", "set", "away", "--ttl", "15s", "--idempotency-key", "stable"}, "POST", "/v1/boards/b/presence", "away", true},
		{[]string{"presence", "set", "offline", "--idempotency-key", "stable"}, "POST", "/v1/boards/b/presence", "offline", false},
	} {
		t.Run(tc.method+tc.path+tc.state, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/server":
					writeJSON(w, 200, map[string]any{"capabilities": []string{"yak.presence"}})
					return
				case "/v1/me":
					writeJSON(w, 200, map[string]any{"actor": map[string]string{"id": "self"}})
					return
				}
				calls++
				if r.Method != tc.method || r.URL.Path != tc.path {
					t.Errorf("wrong request %s %s", r.Method, r.URL)
				}
				if r.Method == "POST" {
					var body map[string]any
					json.NewDecoder(r.Body).Decode(&body)
					if body["state"] != tc.state {
						t.Errorf("wrong body %v", body)
					}
					_, hasTTL := body["ttl_seconds"]
					if hasTTL != tc.ttl {
						t.Errorf("wrong TTL %v", body)
					}
					if tc.ttl && body["ttl_seconds"] != float64(15) {
						t.Errorf("wrong TTL value %v", body)
					}
					if r.Header.Get("Idempotency-Key") != "stable" {
						t.Error("retry key missing")
					}
				}
				writeJSON(w, 200, map[string]any{"board_id": "b", "actor_id": "self", "state": "offline", "revision": 0, "last_seen_at": nil, "expires_at": nil})
			}))
			defer srv.Close()
			t.Setenv("YAK_CONFIG", filepath.Join(t.TempDir(), "config.json"))
			t.Setenv("YAK_URL", srv.URL)
			t.Setenv("YAK_BOARD", "b")
			t.Setenv("YAK_TOKEN", "test")
			t.Setenv("YAK_TOKEN_FILE", "")
			saved := gf
			defer func() { gf = saved }()
			root := &cobra.Command{Use: "yak", SilenceUsage: true, SilenceErrors: true}
			addGlobalFlags(root)
			root.AddCommand(clientCmds()...)
			root.SetArgs(append(tc.args, "--json"))
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("calls=%d", calls)
			}
		})
	}
}
