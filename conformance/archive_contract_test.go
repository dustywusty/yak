package conformance

// These checks validate the draft contract's fixture, not a running archive
// importer. CT-039/043/045 still need the planned two-server implementation.
import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

type archiveFixture map[string][]any

var archiveKinds = map[string]string{
	"board.json": "board", "actors.jsonl": "actor", "channels.jsonl": "channel",
	"grants.jsonl": "grant", "threads.jsonl": "thread", "messages.jsonl": "message",
	"task_batches.jsonl": "batch", "tasks.jsonl": "task", "task_briefs.jsonl": "brief",
	"task_dependencies.jsonl": "dependency", "claims.jsonl": "claim", "events.jsonl": "event", "audit_events.jsonl": "audit",
}

func fixtureCopy[T any](v T) T {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	var out T
	if err := json.Unmarshal(b, &out); err != nil {
		panic(err)
	}
	return out
}

// Visit typed protocol identity slots only. Opaque data with ID-like strings
// must survive remapping; neither string prefixes nor string equality select it.
func fixtureWalk(v any, kind, taskID string, visit func(map[string]any, string, string, string) error) error {
	switch x := v.(type) {
	case []any:
		for _, item := range x {
			if err := fixtureWalk(item, kind, taskID, visit); err != nil {
				return err
			}
		}
	case map[string]any:
		if kind == "event" {
			if resource, ok := x["resource"].(map[string]any); ok && resource["type"] == "task" {
				taskID, _ = resource["id"].(string)
			}
		}
		if kind == "task" {
			taskID, _ = x["id"].(string)
		}
		if kind == "resource" {
			kind, _ = x["type"].(string)
		}
		if err := visit(x, kind, taskID, ""); err != nil {
			return err
		}
		for key, value := range x {
			if key == "data" || key == "extensions" || key == "evidence" || key == "metadata" {
				continue
			}
			target := ""
			switch {
			case key == "id":
				if kind != "brief" && kind != "dependency" && kind != "outcome" {
					target = kind
				}
			case key == "execution_id":
				target = "execution"
			case strings.HasSuffix(key, "actor_id"):
				target = "actor"
			case key == "board_id":
				target = "board"
			case key == "channel_id":
				target = "channel"
			case key == "thread_id":
				target = "thread"
			case key == "task_id":
				target = "task"
			case key == "batch_id":
				target = "batch"
			case key == "claim_id" || key == "current_claim_id" || key == "last_claim_id":
				target = "claim"
			case key == "message_id" || key == "root_message_id" || key == "last_message_id" || key == "reply_to_message_id":
				target = "message"
			}
			if target != "" && value != nil {
				if err := visit(x, target, taskID, key); err != nil {
					return err
				}
			}
			child := ""
			switch key {
			case "task", "message", "thread", "claim":
				child = key
			case "current_claim", "last_claim", "revoked_claim":
				child = "claim"
			case "outcome", "previous_outcome":
				child = "outcome"
			case "resource":
				child = "resource"
			}
			if err := fixtureWalk(value, child, taskID, visit); err != nil {
				return err
			}
		}
	}
	return nil
}

func fixtureIndex(f archiveFixture) (map[string]map[string]any, error) {
	index := map[string]map[string]any{}
	for file, records := range f {
		for _, v := range records {
			m := v.(map[string]any)
			id, ok := m["id"].(string)
			if !ok {
				continue
			}
			key := archiveKinds[file] + ":" + id
			if _, ok := index[key]; ok {
				return nil, fmt.Errorf("duplicate %s", key)
			}
			index[key] = m
			if file == "claims.jsonl" {
				exe, _ := m["execution_id"].(string)
				if exe == "" {
					return nil, fmt.Errorf("missing execution declaration")
				}
				key = "execution:" + exe
				if _, ok := index[key]; ok {
					return nil, fmt.Errorf("duplicate %s", key)
				}
				index[key] = m
			}
		}
	}
	return index, nil
}

func fixtureRelations(f archiveFixture) error {
	index, err := fixtureIndex(f)
	if err != nil {
		return err
	}
	for file, records := range f {
		err = fixtureWalk(records, archiveKinds[file], "", func(m map[string]any, kind, taskID, key string) error {
			if key != "" {
				id, ok := m[key].(string)
				if !ok {
					return fmt.Errorf("non-string %s", key)
				}
				if _, ok := index[kind+":"+id]; !ok {
					return fmt.Errorf("dangling %s:%s", kind, id)
				}
				return nil
			}
			switch kind {
			case "thread":
				for _, key := range []string{"root_message_id", "last_message_id"} {
					message := index["message:"+fmt.Sprint(m[key])]
					if message == nil || message["thread_id"] != m["id"] {
						return fmt.Errorf("thread/message mismatch")
					}
				}
				task := index["task:"+fmt.Sprint(m["task_id"])]
				if task == nil || task["thread_id"] != m["id"] {
					return fmt.Errorf("thread/task mismatch")
				}
			case "task":
				for _, key := range []string{"current_claim_id", "last_claim_id"} {
					if id, ok := m[key].(string); ok {
						claim := index["claim:"+id]
						if claim == nil || claim["task_id"] != taskID {
							return fmt.Errorf("task/claim mismatch")
						}
					}
				}
			case "claim":
				declaration := index["claim:"+fmt.Sprint(m["id"])]
				if declaration == nil {
					return fmt.Errorf("missing claim")
				}
				for _, key := range []string{"task_id", "execution_id", "epoch", "actor_id"} {
					if m[key] != declaration[key] {
						return fmt.Errorf("embedded claim %s mismatch", key)
					}
				}
				if taskID != "" && m["task_id"] != taskID {
					return fmt.Errorf("embedded claim task mismatch")
				}
			case "outcome":
				if m["claim_id"] == nil && m["execution_id"] == nil && m["kind"] == "canceled" {
					return nil
				}
				claim := index["claim:"+fmt.Sprint(m["claim_id"])]
				if claim == nil || claim["execution_id"] != m["execution_id"] || claim["task_id"] != taskID {
					return fmt.Errorf("outcome pair mismatch: claim=%v execution=%v task=%s", m["claim_id"], m["execution_id"], taskID)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func TestArchiveContractFixture(t *testing.T) {
	raw, err := os.ReadFile(specDir + "/fixtures/archive/p2-cycle.json")
	if err != nil {
		t.Fatal(err)
	}
	var f archiveFixture
	if err = json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if err = fixtureRelations(f); err != nil {
		t.Fatal(err)
	}
	// Restore only fields explicitly omitted/changed by the archive contract,
	// then validate the complete public portions (including historical payloads).
	schemaByFile := map[string]string{"board.json": "Board", "actors.jsonl": "Actor", "channels.jsonl": "Channel", "grants.jsonl": "Grant", "threads.jsonl": "Thread", "messages.jsonl": "Message", "tasks.jsonl": "Task", "task_briefs.jsonl": "Brief", "claims.jsonl": "Claim", "events.jsonl": "Event"}
	index, _ := fixtureIndex(f)
	for file, name := range schemaByFile {
		for i, v := range f[file] {
			m := fixtureCopy(v).(map[string]any)
			switch name {
			case "Board":
				m["caller_is_board_admin"] = false
			case "Actor":
				m["owner_actor_id"] = nil
				m["server_admin"] = false
			case "Channel":
				m["caller_capabilities"] = []any{}
			case "Thread":
				m["channel_path"] = "work"
			case "Message":
				m["channel_id"] = "ch_cycle"
				m["channel_path"] = "work"
				m["is_root"] = m["seq"] == float64(1)
				m["attribution"].(map[string]any)["credential_id"] = "cred_fixture"
			case "Task":
				m["channel_id"] = "ch_cycle"
				m["channel_path"] = "work"
				m["title"] = "Cycle fixture"
				m["dependencies"] = []any{}
				m["unreadable_dependencies_pending"] = false
				for _, key := range []string{"current_claim", "last_claim"} {
					m[key] = nil
					if id, ok := m[key+"_id"].(string); ok {
						m[key] = index["claim:"+id]
					}
				}
			case "Event":
				m["cursor"] = "c1.fixture"
			}
			if err := schemaNamed(t, name).Validate(m); err != nil {
				t.Errorf("%s[%d]: %v", file, i, err)
			}
		}
	}
	for _, mode := range []string{"keep", "remap"} {
		t.Run(mode, func(t *testing.T) {
			mapped := fixtureCopy(f)
			mapping := map[string]string{}
			for key := range index {
				_, id, _ := strings.Cut(key, ":")
				mapping[key] = id
				if mode == "remap" {
					mapping[key] = "target-" + key
				}
			}
			for file, records := range mapped {
				err := fixtureWalk(records, archiveKinds[file], "", func(m map[string]any, kind, taskID, key string) error {
					if key != "" {
						m[key] = mapping[kind+":"+m[key].(string)]
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "keep" && !reflect.DeepEqual(f, mapped) {
				t.Fatal("keep changed fixture")
			}
			if mapping["execution:exe_first"] == mapping["execution:exe_second"] {
				t.Fatal("executions collapsed")
			}
			if mode == "remap" && (mapping["execution:exe_first"] == "exe_first" || mapping["execution:exe_second"] == "exe_second") {
				t.Fatal("execution not remapped")
			}
			if err := fixtureRelations(mapped); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"exe_first", "exe_second"} {
				if mapping["execution:"+id] == "" {
					t.Fatal("missing execution mapping")
				}
			}
			if mapping["execution:exe_first"] == mapping["claim:clm_first"] {
				t.Fatal("execution mapped as claim")
			}
			for _, v := range mapped["claims.jsonl"] {
				if v.(map[string]any)["run_id"] != "exe_first" {
					t.Fatal("opaque run_id changed")
				}
			}
			for _, v := range mapped["tasks.jsonl"] {
				o := v.(map[string]any)["outcome"].(map[string]any)
				original := f["tasks.jsonl"][0].(map[string]any)["outcome"].(map[string]any)
				for _, key := range []string{"data", "evidence"} {
					if !reflect.DeepEqual(o[key], original[key]) {
						t.Fatal("opaque content changed")
					}
				}
			}
		})
	}
	mutations := map[string]func(archiveFixture){
		"half-null outcome": func(x archiveFixture) {
			x["tasks.jsonl"][0].(map[string]any)["outcome"].(map[string]any)["claim_id"] = nil
		},
		"thread task mismatch": func(x archiveFixture) { x["tasks.jsonl"][0].(map[string]any)["thread_id"] = "missing" },
		"dangling root":        func(x archiveFixture) { x["threads.jsonl"][0].(map[string]any)["root_message_id"] = "missing" },
		"wrong-kind root":      func(x archiveFixture) { x["threads.jsonl"][0].(map[string]any)["root_message_id"] = "clm_first" },
		"dangling batch task": func(x archiveFixture) {
			x["task_batches.jsonl"][0].(map[string]any)["items"].([]any)[0].(map[string]any)["task_id"] = "missing"
		},
		"missing claim":       func(x archiveFixture) { x["claims.jsonl"] = x["claims.jsonl"][:1] },
		"duplicate execution": func(x archiveFixture) { x["claims.jsonl"][1].(map[string]any)["execution_id"] = "exe_first" },
		"wrong outcome pair": func(x archiveFixture) {
			x["tasks.jsonl"][0].(map[string]any)["outcome"].(map[string]any)["execution_id"] = "exe_first"
		},
		"dangling execution": func(x archiveFixture) {
			x["tasks.jsonl"][0].(map[string]any)["outcome"].(map[string]any)["execution_id"] = "missing"
		},
		"historical pair": func(x archiveFixture) {
			x["events.jsonl"][3].(map[string]any)["payload"].(map[string]any)["previous_outcome"].(map[string]any)["execution_id"] = "exe_second"
		},
		"embedded claim": func(x archiveFixture) {
			x["events.jsonl"][1].(map[string]any)["payload"].(map[string]any)["claim"].(map[string]any)["execution_id"] = "exe_second"
		},
		"wrong message thread": func(x archiveFixture) { x["messages.jsonl"][0].(map[string]any)["thread_id"] = "missing" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			bad := fixtureCopy(f)
			mutate(bad)
			if fixtureRelations(bad) == nil {
				t.Fatal("invalid relationship accepted")
			}
		})
	}
}
