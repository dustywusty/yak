package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRenameEnvironmentPrecedence(t *testing.T) {
	t.Setenv("ABBY_CONFIG", filepath.Join(t.TempDir(), "old.json"))
	t.Setenv("YAK_CONFIG", filepath.Join(t.TempDir(), "new.json"))
	t.Setenv("ABBY_URL", "http://old.test")
	t.Setenv("YAK_URL", "http://new.test")
	t.Setenv("ABBY_BOARD", "old-board")
	t.Setenv("YAK_BOARD", "new-board")
	t.Setenv("ABBY_TOKEN", "old-token")
	t.Setenv("YAK_TOKEN", "new-token")
	t.Setenv("ABBY_TOKEN_FILE", "")
	t.Setenv("YAK_TOKEN_FILE", "")
	p, err := resolved()
	if err != nil || p.URL != "http://new.test" || p.Board != "new-board" || p.Token != "new-token" {
		t.Fatalf("new environment did not win: %+v %v", p, err)
	}
	t.Setenv("YAK_URL", "")
	t.Setenv("YAK_BOARD", "")
	t.Setenv("YAK_TOKEN", "")
	p, err = resolved()
	if err != nil || p.URL != "http://old.test" || p.Board != "old-board" || p.Token != "old-token" {
		t.Fatalf("legacy fallback failed: %+v %v", p, err)
	}
}

func TestRenameTokenFamilyPrecedence(t *testing.T) {
	t.Setenv("YAK_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("ABBY_TOKEN_FILE", "/missing/legacy/token")
	t.Setenv("YAK_TOKEN_FILE", "")
	t.Setenv("YAK_TOKEN", "new-token")
	p, err := resolved()
	if err != nil || p.Token != "new-token" {
		t.Fatalf("new token lost to legacy file: %v", err)
	}
}

func TestRenameEnvironmentAliases(t *testing.T) {
	for _, suffix := range []string{"CONFIG", "PROFILE", "URL", "BOARD", "TOKEN", "TOKEN_FILE", "RUN_ID", "CLAIM_PROOF", "DATABASE_URL", "LISTEN", "ROLE", "CLAIM_LEASE_MIN_SECONDS", "CLAIM_REPLAY_KEYS", "CLAIM_PROOF_KEY"} {
		t.Run(suffix, func(t *testing.T) {
			t.Setenv("ABBY_"+suffix, "legacy")
			t.Setenv("YAK_"+suffix, "")
			if got := yakEnv("YAK_" + suffix); got != "legacy" {
				t.Fatalf("legacy fallback = %q", got)
			}
			t.Setenv("YAK_"+suffix, "new")
			if got := yakEnv("YAK_" + suffix); got != "new" {
				t.Fatalf("new precedence = %q", got)
			}
		})
	}
}

func TestRenameRunnerEnvironment(t *testing.T) {
	t.Setenv("ABBY_CLAIM_PROOF", "legacy-secret")
	t.Setenv("YAK_CLAIM_PROOF", "new-secret")
	t.Setenv("ABBY_TASK_ID", "stale")
	t.Setenv("YAK_TASK_ID", "stale")
	f := newFakeServer(t, 30)
	r := testRunner(t, f, sh(`test -z "${YAK_CLAIM_PROOF:-}" && test -z "${ABBY_CLAIM_PROOF:-}" && test "$YAK_TASK_ID" = "$ABBY_TASK_ID" && test "$YAK_TASK_ID" != stale && test "$YAK_RESULT_FILE" = "$ABBY_RESULT_FILE" && printf '{"summary":"legacy child works"}' > "$ABBY_RESULT_FILE"`), nil)
	rep, err := r.run(context.Background())
	if err != nil || rep.Outcome != "completed" {
		t.Fatalf("legacy runner environment failed: %v", err)
	}
}

func TestRenameDefaultConfigFallback(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("YAK_CONFIG", "")
	t.Setenv("ABBY_CONFIG", "")
	if err := os.MkdirAll(filepath.Join(dir, "abby"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "abby", "config.json"), []byte(`{"current":"old","profiles":{"old":{"url":"http://old.test"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := loadConfig()
	if err != nil || c.Profiles["old"].URL != "http://old.test" {
		t.Fatalf("old config unreadable: %v", err)
	}
	if err := saveConfig(c); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "yak", "config.json")); err != nil {
		t.Fatal(err)
	}
}
