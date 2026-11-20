package main

import (
	"encoding/json"
	"github.com/spf13/cobra"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestTaskGateFlags(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"create", "work", "--title", "gate", "--text", "review", "--direct-action-required"}, true},
		{[]string{"convert", "thr_old", "--direct-action-required=false"}, false},
		{[]string{"edit", "task_old", "--if-revision", "1", "--direct-action-required=false"}, false},
	} {
		t.Run(tc.args[0], func(t *testing.T) {
			var body map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				writeJSON(w, 200, map[string]any{"id": "task_old", "state": "ready", "direct_action_required": tc.want})
			}))
			defer srv.Close()
			t.Setenv("YAK_CONFIG", filepath.Join(t.TempDir(), "config.json"))
			t.Setenv("YAK_URL", srv.URL)
			t.Setenv("YAK_BOARD", "brd_test")
			t.Setenv("YAK_TOKEN", "test-token")
			t.Setenv("YAK_TOKEN_FILE", "")
			saved := gf
			defer func() { gf = saved }()
			root := &cobra.Command{Use: "yak", SilenceUsage: true, SilenceErrors: true}
			addGlobalFlags(root)
			root.AddCommand(taskCmd())
			root.SetArgs(append([]string{"task"}, tc.args...))
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			if body["direct_action_required"] != tc.want {
				t.Fatalf("body=%v", body)
			}
		})
	}
}
