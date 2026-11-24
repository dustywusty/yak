package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

func TestInboxCLIReadsWithoutAcknowledgment(t *testing.T) {
	var reads, acks int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/server":
			writeJSON(w, 200, map[string]any{"capabilities": []string{"yak.inbox"}})
		case "/v1/boards/b/inbox":
			reads++
			if r.Method != "GET" || r.URL.Query().Get("cursor") != "i1.test" || r.URL.Query().Get("limit") != "2" {
				t.Errorf("wrong inbox request: %s %s", r.Method, r.URL)
			}
			writeJSON(w, 200, map[string]any{"items": []any{}, "next_cursor": "i1.next", "ack_cursor": "i1.next", "caught_up": true, "timed_out": false, "unread_count": 0})
		default:
			acks++
			t.Errorf("unexpected request %s", r.URL)
		}
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
	root.SetArgs([]string{"inbox", "--cursor", "i1.test", "--limit", "2", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if reads != 1 || acks != 0 {
		t.Fatalf("reads=%d unexpected=%d", reads, acks)
	}
}

func TestInboxCLIAckUsesOnlyExplicitCursorAndKey(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/server" {
			writeJSON(w, 200, map[string]any{"capabilities": []string{"yak.inbox"}})
			return
		}
		if r.Method != "POST" || r.URL.Path != "/v1/boards/b/inbox/ack" || r.Header.Get("Idempotency-Key") != "retry-key" {
			t.Errorf("wrong ack: %s %s", r.Method, r.URL)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		writeJSON(w, 200, map[string]any{"board_id": "b", "actor_id": "a", "acknowledged_at": "2026-11-23T20:00:00Z"})
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
	root.SetArgs([]string{"inbox", "ack", "--cursor", "i1.test", "--idempotency-key", "retry-key", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if body["cursor"] != "i1.test" || len(body) != 1 {
		t.Fatalf("wrong ack body: %v", body)
	}
}
