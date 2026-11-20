package domain_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/dustywusty/yak/internal/events"
	"github.com/dustywusty/yak/internal/postgres"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dustywusty/yak/internal/domain"
)

func gateValue(t *testing.T, v any) bool {
	t.Helper()
	b, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	flag, ok := m["direct_action_required"].(bool)
	if !ok {
		t.Fatalf("missing explicit boolean in %s", b)
	}
	return flag
}

func TestGateFlagRoundTripAndReopen(t *testing.T) {
	x := setupP2(t)
	ctx := context.Background()
	if gateValue(t, x.task("work", "default")) {
		t.Fatal("default true")
	}
	var req domain.CreateTaskRequest
	_ = json.Unmarshal([]byte(`{"thread":{"channel":"work","title":"gate","text":"review"},"direct_action_required":true}`), &req)
	res, err := x.svc.CreateTask(ctx, x.admin, x.board, x.key(), req)
	if err != nil {
		t.Fatal(err)
	}
	tk := res.Body.(domain.TaskView)
	if !gateValue(t, x.get(tk.ID)) {
		t.Fatal("flag lost")
	}
	for _, actor := range []*domain.Principal{x.admin, x.actor("*", "read", "task_admin", "claim")} {
		reason := "cancel"
		if _, err := x.svc.CancelTask(ctx, actor, x.board, tk.ID, nil, x.key(), domain.ReasonRequest{Reason: &reason}); err != nil {
			t.Fatal(err)
		}
		head := x.head()
		res, err := x.svc.ReopenTask(ctx, actor, x.board, tk.ID, nil, x.key(), domain.ReasonRequest{})
		if err != nil {
			t.Fatal(err)
		}
		tk = res.Body.(domain.TaskView)
		if tk.State != "blocked" || !gateValue(t, tk) {
			t.Fatalf("reopen bypass: %+v", tk)
		}
		var block domain.BlockInfo
		_ = json.Unmarshal(tk.Block, &block)
		if block.Reason != "direct action required" || block.Cause != "manual" || block.ByActorID != actor.ActorID {
			t.Fatalf("block: %+v", block)
		}
		rows, err := x.svc.DB.Query(ctx, `SELECT type FROM events WHERE board_id=$1 AND position>$2 ORDER BY position`, x.board, head)
		if err != nil {
			t.Fatal(err)
		}
		var types []string
		for rows.Next() {
			var typ string
			_ = rows.Scan(&typ)
			types = append(types, typ)
		}
		rows.Close()
		if stringJSON(types) != `["task.reopened","task.blocked","thread.view_invalidated"]` {
			t.Fatalf("events: %v", types)
		}
		if _, err := x.claim(actor, tk.ID, 60); code(err) != "invalid_transition" {
			t.Fatalf("blocked claim: %v", err)
		}
		if _, err := x.svc.UnblockTask(ctx, actor, x.board, tk.ID, nil, x.key(), domain.ReasonRequest{}); err != nil {
			t.Fatal(err)
		}
	}
	var patch domain.PatchTaskRequest
	_ = json.Unmarshal([]byte(`{"direct_action_required":false}`), &patch)
	before := x.get(tk.ID)
	outsider := x.actor("*", "read", "task_manage")
	if _, err := x.svc.PatchTask(ctx, outsider, x.board, tk.ID, before.Revision, patch); code(err) != "forbidden" {
		t.Fatalf("unauthorized clear: %v", err)
	}
	if _, err := x.svc.PatchTask(ctx, x.admin, x.board, tk.ID, before.Revision-1, patch); code(err) != "revision_conflict" {
		t.Fatalf("stale clear: %v", err)
	}
	cleared, err := x.svc.PatchTask(ctx, x.admin, x.board, tk.ID, before.Revision, patch)
	if err != nil {
		t.Fatal(err)
	}
	if gateValue(t, cleared) || gateValue(t, x.get(tk.ID)) {
		t.Fatal("false not persisted")
	}
}

func stringJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestGateBatchConversionViewsAndHistory(t *testing.T) {
	x := setupP2(t)
	ctx := context.Background()
	hidden := x.task("secret", "hidden")
	var req domain.CreateTaskBatchRequest
	if err := json.Unmarshal([]byte(`{"tasks":[{"ref":"gate","thread":{"channel":"public","title":"gate","text":"brief"},"direct_action_required":true},{"ref":"plain","thread":{"channel":"public","title":"plain","text":"brief"},"direct_action_required":false}]}`), &req); err != nil {
		t.Fatal(err)
	}
	res, err := x.svc.CreateTaskBatch(ctx, x.admin, x.board, x.key(), req)
	if err != nil {
		t.Fatal(err)
	}
	batch := res.Body.(domain.TaskBatchView)
	if !gateValue(t, batch.Tasks[0].Task) || gateValue(t, batch.Tasks[1].Task) {
		t.Fatal("batch flags lost")
	}
	gate := batch.Tasks[0].Task
	_, err = x.svc.SetDependencies(ctx, x.admin, x.board, gate.ID, gate.Revision, x.key(), domain.DependenciesRequest{TaskIDs: []string{hidden.ID}})
	if err != nil {
		t.Fatal(err)
	}
	reader := x.actor("public", "read")
	v, err := x.svc.GetTask(ctx, reader, x.board, gate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !gateValue(t, v) || len(v.Dependencies) != 0 || !v.UnreadableDependenciesPending {
		t.Fatalf("projection: %+v", v)
	}
	page, err := x.svc.ReadEvents(ctx, reader, x.board, domain.ReadEventsRequest{Limit: 200, Filter: events.Filter{Types: []string{"task.created"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range page.Events {
		var pl struct {
			Task domain.TaskView `json:"task"`
		}
		_ = json.Unmarshal(ev.Payload, &pl)
		if pl.Task.ID == gate.ID && !gateValue(t, pl.Task) {
			t.Fatal("event flag lost")
		}
		if strings.Contains(string(ev.Payload), hidden.ID) {
			t.Fatal("hidden dependency leaked")
		}
	}
	th, err := x.svc.CreateThread(ctx, x.admin, x.board, x.key(), domain.CreateThreadRequest{Channel: "public", Title: "convert", Text: "brief"})
	if err != nil {
		t.Fatal(err)
	}
	var convert domain.CreateTaskRequest
	_ = json.Unmarshal([]byte(`{"thread_id":"`+th.ResourceID+`","direct_action_required":true}`), &convert)
	converted, err := x.svc.CreateTask(ctx, x.admin, x.board, x.key(), convert)
	if err != nil {
		t.Fatal(err)
	}
	if !gateValue(t, converted.Body) {
		t.Fatal("conversion lost flag")
	}
	// A pending prerequisite must not turn a reopened gate into waiting.
	reason := "cancel"
	_, err = x.svc.CancelTask(ctx, x.admin, x.board, gate.ID, nil, x.key(), domain.ReasonRequest{Reason: &reason})
	if err != nil {
		t.Fatal(err)
	}
	reason = "new approval needed"
	reopened, err := x.svc.ReopenTask(ctx, x.admin, x.board, gate.ID, nil, x.key(), domain.ReasonRequest{Reason: &reason})
	if err != nil {
		t.Fatal(err)
	}
	rv := reopened.Body.(domain.TaskView)
	var block domain.BlockInfo
	_ = json.Unmarshal(rv.Block, &block)
	if rv.State != "blocked" || block.Reason != reason {
		t.Fatalf("pending reopen: %+v", rv)
	}
}

func TestGateMigrationDefaultsOldRowsWithoutChangingEvents(t *testing.T) {
	base := os.Getenv("YAK_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set YAK_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	admin, err := postgres.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := fmt.Sprintf("gate_upgrade_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, `CREATE DATABASE `+name); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.Exec(ctx, `DROP DATABASE `+name+` WITH (FORCE)`) }()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	t.Setenv("YAK_TEST_DATABASE_URL", u.String())
	x := setupP2(t)
	defer x.svc.DB.Close()
	old := x.task("work", "old row")
	var before []byte
	if err := x.svc.DB.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(e) ORDER BY position) FROM events e WHERE board_id=$1`, x.board).Scan(&before); err != nil {
		t.Fatal(err)
	}
	// Model schema 4 storage. Migration files 1..4 remain unchanged.
	if _, err = x.svc.DB.Exec(ctx, `ALTER TABLE tasks DROP COLUMN direct_action_required; DELETE FROM schema_migrations WHERE version=5`); err != nil {
		t.Fatal(err)
	}
	applied, err := postgres.Migrate(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || !strings.HasPrefix(applied[0], "0005_") {
		t.Fatalf("applied: %v", applied)
	}
	if gateValue(t, x.get(old.ID)) {
		t.Fatal("old row default true")
	}
	var after []byte
	if err := x.svc.DB.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(e) ORDER BY position) FROM events e WHERE board_id=$1`, x.board).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("migration changed immutable event history")
	}
}

func TestGateFailedReopenAndConcurrentClaimCannotBypass(t *testing.T) {
	x := setupP2(t)
	ctx := context.Background()
	req := domain.CreateTaskRequest{Thread: &domain.TaskThreadSpec{Channel: "work", Title: "failed gate", Text: "brief"}}
	_ = json.Unmarshal([]byte(`{"direct_action_required":true}`), &req)
	res, err := x.svc.CreateTask(ctx, x.admin, x.board, x.key(), req)
	if err != nil {
		t.Fatal(err)
	}
	gate := res.Body.(domain.TaskView)
	cr, err := x.claim(x.admin, gate.ID, 60)
	if err != nil {
		t.Fatal(err)
	}
	reason := "failed"
	_, err = x.svc.FailTask(ctx, x.admin, x.board, gate.ID, proofOf(t, cr), x.key(), domain.FailRequest{Reason: &reason})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := x.svc.ReopenTask(ctx, x.admin, x.board, gate.ID, nil, x.key(), domain.ReasonRequest{})
			if err != nil && code(err) != "invalid_transition" {
				errs <- err
			}
		}()
		go func() {
			defer wg.Done()
			_, err := x.claim(x.admin, gate.ID, 60)
			if code(err) != "invalid_transition" {
				errs <- fmt.Errorf("claim escaped gate: %v", err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	v := x.get(gate.ID)
	if v.State != "blocked" || v.CurrentClaim != nil || v.LastClaim == nil || v.LastClaim.Status != "failed" || string(v.Outcome) != "null" || v.RetryCount != 1 {
		t.Fatalf("failed reopen: %+v", v)
	}
	if !gateValue(t, v) {
		t.Fatal("flag lost")
	}
}
