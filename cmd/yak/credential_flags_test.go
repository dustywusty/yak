package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

func TestCredentialCreateRestrictionFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want map[string]any
	}{
		{"inherit", nil, map[string]any{}},
		{"restrict", []string{"--may-manage-credentials=false", "--may-delegate=false"}, map[string]any{"may_manage_credentials": false, "may_delegate": false}},
		{"explicit true", []string{"--may-delegate=true"}, map[string]any{"may_delegate": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/v1/credentials" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				writeJSON(w, 201, map[string]any{"credential": map[string]any{}, "token_unavailable": true})
			}))
			defer srv.Close()
			t.Setenv("YAK_CONFIG", filepath.Join(t.TempDir(), "config.json"))
			t.Setenv("YAK_URL", srv.URL)
			t.Setenv("YAK_TOKEN", "test-token")
			t.Setenv("YAK_TOKEN_FILE", "")
			saved := gf
			defer func() { gf = saved }()
			root := &cobra.Command{Use: "yak", SilenceUsage: true, SilenceErrors: true}
			addGlobalFlags(root)
			root.AddCommand(clientCmds()...)
			root.SetArgs(append([]string{"credential", "create", "--scopes", "read", "--json"}, tc.args...))
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			for _, flag := range []string{"may_manage_credentials", "may_delegate"} {
				got, exists := body[flag]
				want, specified := tc.want[flag]
				if exists != specified || got != want {
					t.Errorf("%s = %v (present %v), want %v (present %v)", flag, got, exists, want, specified)
				}
			}
		})
	}
}
