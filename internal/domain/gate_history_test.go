package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHistoricalTaskProjectionKeepsOriginalFields(t *testing.T) {
	old := json.RawMessage(`{"task":{"id":"task_old","state":"waiting","dependencies":[{"task_id":"hidden","state":"ready","channel_id":"secret"}],"unreadable_dependencies_pending":false},"reason":"old event"}`)
	got := projectTaskJSON(old, func(string) bool { return false })
	if strings.Contains(string(got), "direct_action_required") || strings.Contains(string(got), "hidden") || strings.Contains(string(got), "secret") {
		t.Fatalf("history changed or leaked: %s", got)
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(got, &m)
	var task map[string]any
	_ = json.Unmarshal(m["task"], &task)
	if len(task) != 4 || task["unreadable_dependencies_pending"] != true {
		t.Fatalf("historical fields changed: %s", got)
	}
}

func TestDefaultGateFlagPreservesPreUpgradeFingerprint(t *testing.T) {
	// The pre-schema-5 normalized request used this exact field order.
	type oldCreate struct {
		Thread          *TaskThreadSpec `json:"thread,omitempty"`
		ThreadID        string          `json:"thread_id,omitempty"`
		AssigneeActorID *string         `json:"assignee_actor_id"`
		DependsOn       []string        `json:"depends_on"`
	}
	thread := &TaskThreadSpec{Channel: "work", Title: "old", Text: "brief"}
	old := &Idem{Request: oldCreate{Thread: thread, DependsOn: []string{}}}
	current := &Idem{Request: CreateTaskRequest{Thread: thread, DependsOn: []string{}}}
	if string(old.fingerprint()) != string(current.fingerprint()) {
		t.Fatal("default false changed an existing creation fingerprint")
	}
	type oldItem struct {
		Ref             string            `json:"ref"`
		Thread          *TaskThreadSpec   `json:"thread"`
		AssigneeActorID *string           `json:"assignee_actor_id"`
		DependsOn       []BatchDependency `json:"depends_on"`
	}
	oldBatch := &Idem{Request: struct {
		Tasks []oldItem `json:"tasks"`
	}{[]oldItem{{Ref: "old", Thread: thread, DependsOn: []BatchDependency{}}}}}
	batch := &Idem{Request: CreateTaskBatchRequest{Tasks: []BatchTaskItem{{Ref: "old", Thread: thread, DependsOn: []BatchDependency{}}}}}
	if string(oldBatch.fingerprint()) != string(batch.fingerprint()) {
		t.Fatal("default false changed an existing batch fingerprint")
	}
}
