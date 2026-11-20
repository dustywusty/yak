package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
)

// actorArgServer answers GET /v1/me and GET /v1/boards/b/tasks, and records
// the task list query strings.
type actorArgServer struct {
	mu      sync.Mutex
	queries []url.Values
	meCalls int
}

func runTaskCLI(t *testing.T, args ...string) (*actorArgServer, error) {
	t.Helper()
	s := &actorArgServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch {
		case r.Method == "GET" && r.URL.Path == "/v1/me":
			s.meCalls++
			writeJSON(w, 200, map[string]any{"actor": map[string]any{"id": "act_me", "handle": "me-handle"}, "credential": map[string]any{}})
		case r.Method == "GET" && r.URL.Path == "/v1/boards/b/tasks":
			s.queries = append(s.queries, r.URL.Query())
			items := []any{}
			if r.URL.Query().Get("assignee") == "act_me" {
				items = append(items, taskView("ready"))
			}
			writeJSON(w, 200, map[string]any{"items": items, "next_page_token": nil})
		default:
			problem(w, 404, "not_found")
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("YAK_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("YAK_URL", srv.URL)
	t.Setenv("YAK_TOKEN", "tok")
	t.Setenv("YAK_TOKEN_FILE", "")
	t.Setenv("YAK_BOARD", "b")
	t.Setenv("YAK_PROFILE", "")
	saved := gf // addGlobalFlags resets gf to the flag defaults
	t.Cleanup(func() { gf = saved })

	root := &cobra.Command{Use: "yak", SilenceUsage: true, SilenceErrors: true}
	addGlobalFlags(root)
	root.AddCommand(taskCmd())
	root.SetArgs(args)
	return s, root.Execute()
}

func TestTaskListAssigneeMeResolvesViaMe(t *testing.T) {
	s, err := runTaskCLI(t, "task", "list", "--assignee", "me", "--owner", "me")
	if err != nil {
		t.Fatalf("task list --assignee me: %v", err)
	}
	if s.meCalls == 0 {
		t.Fatal("--assignee me did not call GET /v1/me")
	}
	if len(s.queries) != 1 {
		t.Fatalf("want 1 list request, got %d", len(s.queries))
	}
	if got := s.queries[0].Get("assignee"); got != "act_me" {
		t.Fatalf("assignee query = %q, want act_me", got)
	}
	if got := s.queries[0].Get("owner"); got != "act_me" {
		t.Fatalf("owner query = %q, want act_me", got)
	}
}

func TestTaskListAssigneeNoneAndUnassigned(t *testing.T) {
	for _, v := range []string{"none", "unassigned"} {
		s, err := runTaskCLI(t, "task", "list", "--assignee", v)
		if err != nil {
			t.Fatalf("--assignee %s: %v", v, err)
		}
		if got := s.queries[0].Get("assignee"); got != "none" {
			t.Fatalf("--assignee %s sent %q, want none", v, got)
		}
		if s.meCalls != 0 {
			t.Fatalf("--assignee %s called /v1/me", v)
		}
	}
}

func TestTaskListAssigneeActorIDPassesThrough(t *testing.T) {
	s, err := runTaskCLI(t, "task", "list", "--assignee", "act_other")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.queries[0].Get("assignee"); got != "act_other" {
		t.Fatalf("assignee query = %q, want act_other", got)
	}
}

func TestTaskListRejectsUnknownSpecialValues(t *testing.T) {
	for _, tc := range []struct{ flag, v string }{
		{"--assignee", "eligible"},
		{"--assignee", "ME"},
		{"--assignee", "@me"},
		{"--assignee", "None"},
		{"--assignee", "@abby-coordinator"},
		{"--owner", "none"},
		{"--owner", "unassigned"},
	} {
		s, err := runTaskCLI(t, "task", "list", tc.flag, tc.v)
		if err == nil {
			t.Fatalf("%s %q: want a usage error, got none", tc.flag, tc.v)
		}
		if !isUsageErr(err) {
			t.Fatalf("%s %q: want a usage error, got %v", tc.flag, tc.v, err)
		}
		if !strings.Contains(err.Error(), tc.flag) {
			t.Fatalf("%s %q: error does not name the flag: %v", tc.flag, tc.v, err)
		}
		if len(s.queries) != 0 {
			t.Fatalf("%s %q: list request sent despite the bad value", tc.flag, tc.v)
		}
	}
}
