package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/dustywusty/yak/internal/domain"
)

func TestOP17044(t *testing.T) {
	out, err := exec.Command("node", "test/emit-operator-wire.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("producer: %v: %s", err, out)
	}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	count, creates := 0, 0
	expected := []struct{ method, path string }{
		{"POST", "/tasks"}, {"POST", "/tasks"}, {"POST", "/tasks/t/unblock"}, {"PATCH", "/messages/root/labels"},
		{"POST", "/threads/th/messages"}, {"PATCH", "/messages/root/labels"}, {"PATCH", "/tasks/t"}, {"PATCH", "/tasks/t"},
		{"PUT", "/tasks/t/assignee"}, {"PUT", "/tasks/t/assignee"}, {"POST", "/tasks/t/claims"}, {"POST", "/tasks/t/claims/q/renew"}, {"POST", "/tasks/t/release"},
	}
	for scanner.Scan() {
		var r struct {
			Method, Path string
			Body         json.RawMessage
			Headers      map[string]string
		}
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if count >= len(expected) || r.Method != expected[count].method || r.Path != "/v1/boards/brd_test"+expected[count].path {
			t.Fatalf("unexpected artifact %d: %s %s", count, r.Method, r.Path)
		}
		var dst any
		switch {
		case strings.HasSuffix(r.Path, "/tasks"):
			dst = &domain.CreateTaskRequest{}
		case strings.HasSuffix(r.Path, "/claims"):
			dst = &domain.ClaimRequest{}
		case strings.HasSuffix(r.Path, "/renew"):
			dst = &domain.RenewRequest{}
		case strings.HasSuffix(r.Path, "/release"), strings.HasSuffix(r.Path, "/unblock"):
			dst = &domain.ReasonRequest{}
		case strings.HasSuffix(r.Path, "/assignee"):
			dst = &domain.AssigneeRequest{}
		case strings.HasSuffix(r.Path, "/labels"):
			dst = &domain.LabelsRequest{}
		case strings.HasSuffix(r.Path, "/messages"):
			dst = &domain.ReplyRequest{}
		case r.Method == "PATCH":
			dst = &domain.PatchTaskRequest{}
		default:
			t.Fatalf("unmapped request: %s", scanner.Text())
		}
		dec := json.NewDecoder(bytes.NewReader(r.Body))
		dec.DisallowUnknownFields()
		if err := dec.Decode(dst); err != nil {
			t.Fatalf("%s %s: %v", r.Method, r.Path, err)
		}
		if r.Headers["Idempotency-Key"] == "" || r.Headers["Yak-Run-Id"] == "" {
			t.Fatal("missing intent headers")
		}
		switch v := dst.(type) {
		case *domain.CreateTaskRequest:
			if v.Thread == nil || v.Thread.Channel != "ops" || v.Thread.Title != "Wire task" || v.Thread.Text != "Wire brief" || v.ThreadID != "" || len(v.DependsOn) != 0 {
				t.Fatalf("create: %+v", v)
			}
			if v.DirectActionRequired != (creates == 1) || (creates == 0 && v.AssigneeActorID != nil) || (creates == 1 && (v.AssigneeActorID == nil || *v.AssigneeActorID != "a")) {
				t.Fatalf("gate/assignee: %+v", v)
			}
			creates++
		case *domain.ClaimRequest:
			if v.LeaseSeconds == nil || *v.LeaseSeconds != 300 || v.ExpectBriefRevision == nil || *v.ExpectBriefRevision != 1 {
				t.Fatal("claim semantics")
			}
		case *domain.RenewRequest:
			if v.LeaseSeconds == nil || *v.LeaseSeconds != 300 {
				t.Fatal("renew semantics")
			}
		case *domain.ReasonRequest:
			want := "released by operator"
			if count == 2 {
				want = "gate approved: reviewed"
			}
			if v.Reason == nil || *v.Reason != want {
				t.Fatal("missing reason")
			}
		case *domain.ReplyRequest:
			if v.Text != "Revise it" {
				t.Fatal("reply text")
			}
		case *domain.LabelsRequest:
			want := "changes_requested"
			if count == 3 {
				want = "approved"
			}
			if len(v.Operations) != 1 || v.Operations[0].Op != "set" || v.Operations[0].Value == nil || *v.Operations[0].Value != want || v.Operations[0].Key != "gate" || v.Operations[0].Expect == nil || v.Operations[0].Expect.Value == nil || *v.Operations[0].Expect.Value != "pending" {
				t.Fatal("label semantics")
			}
		case *domain.AssigneeRequest:
			if (count == 8 && (v.AssigneeActorID == nil || *v.AssigneeActorID != "a")) || (count == 9 && v.AssigneeActorID != nil) {
				t.Fatal("assign/unassign semantics")
			}
		case *domain.PatchTaskRequest:
			if v.DirectActionRequired == nil || *v.DirectActionRequired != (count == 6) || v.Title != nil || v.OwnerActorID != nil {
				t.Fatal("gate-only patch")
			}
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 13 || creates != 2 {
		t.Fatalf("executed %d requests, %d creates", count, creates)
	}
	t.Logf("validated %d artifacts", count)
}
