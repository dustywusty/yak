package yak

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"
)

func TestGateFieldSDKRoundTrip(t *testing.T) {
	rec := &recorder{}
	tc := newTasks(t, func(w http.ResponseWriter, r *http.Request) {
		s := rec.add(r)
		v := task("gate", "ready")
		v["direct_action_required"] = s.Body["direct_action_required"]
		jsonOK(w, 200, v)
	})
	ctx := context.Background()
	created, err := tc.Create(ctx, CreateTaskRequest{ThreadID: "thr_old", DirectActionRequired: true}, "")
	if err != nil || !created.DirectActionRequired {
		t.Fatalf("create: %+v %v", created, err)
	}
	no := false
	cleared, err := tc.Patch(ctx, "gate", 3, PatchTaskRequest{DirectActionRequired: &no})
	if err != nil || cleared.DirectActionRequired {
		t.Fatalf("clear: %+v %v", cleared, err)
	}
	if rec.all()[1].Body["direct_action_required"] != false {
		t.Fatal("explicit false omitted")
	}
	b, _ := json.Marshal(PatchTaskRequest{Title: strForGate("title")})
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if _, exists := m["direct_action_required"]; exists {
		t.Fatal("omitted patch cleared flag")
	}
}
func strForGate(s string) *string { return &s }

func TestDefaultGateFlagPreservesPlanID(t *testing.T) {
	legacy := []byte(`[{"ref":"a","thread":{"channel":"work","title":"gate","text":"brief"}}]`)
	sum := sha256.Sum256(legacy)
	want := "plan-" + hex.EncodeToString(sum[:8])
	items := []BatchItem{{Ref: "a", Thread: NewThread{Channel: "work", Title: "gate", Text: "brief"}}}
	if got := PlanID(items); got != want {
		t.Fatalf("existing plan ID changed: %s, want %s", got, want)
	}
	items[0].DirectActionRequired = true
	if PlanID(items) == want {
		t.Fatal("gate flag must affect plan ID when true")
	}
}
