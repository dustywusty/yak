package httpapi

import (
	"github.com/dustywusty/yak/internal/domain"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGateFlagMustBeBoolean(t *testing.T) {
	for _, value := range []string{"true", "false", "null", "1", `"true"`} {
		for _, kind := range []string{"create", "patch", "batch"} {
			t.Run(kind+value, func(t *testing.T) {
				body := `{"direct_action_required":` + value + `}`
				var dst any
				switch kind {
				case "create":
					dst = &domain.CreateTaskRequest{}
				case "patch":
					dst = &domain.PatchTaskRequest{}
				case "batch":
					dst = &domain.CreateTaskBatchRequest{}
					body = `{"tasks":[` + body + `]}`
				}
				err := decode(httptest.NewRequest("POST", "/", strings.NewReader(body)), dst)
				valid := value == "true" || value == "false"
				if (err == nil) != valid {
					t.Fatalf("%s: error=%v", body, err)
				}
			})
		}
	}
}

func TestGateBooleanDecoderSpellingsAndDuplicates(t *testing.T) {
	cases := []struct {
		name, fields string
		valid, want  bool
	}{
		{"omitted", ``, true, false},
		{"lower false", `"direct_action_required":false`, true, false},
		{"upper true", `"DIRECT_ACTION_REQUIRED":true`, true, true},
		{"mixed false", `"Direct_Action_Required":false`, true, false},
		{"escaped true", `"\u0064irect_action_required":true`, true, true},
		{"upper null", `"DIRECT_ACTION_REQUIRED":null`, false, false},
		{"mixed null", `"Direct_Action_Required":null`, false, false},
		{"escaped null", `"\u0064irect_action_required":null`, false, false},
		{"duplicate true false", `"direct_action_required":true,"direct_action_required":false`, true, false},
		{"duplicate mixed true", `"direct_action_required":false,"DIRECT_ACTION_REQUIRED":true`, true, true},
		{"duplicate null first", `"direct_action_required":null,"direct_action_required":true`, false, false},
		{"duplicate null last", `"direct_action_required":true,"direct_action_required":null`, false, false},
		{"duplicate mixed null first", `"DIRECT_ACTION_REQUIRED":null,"direct_action_required":true`, false, false},
		{"duplicate mixed null last", `"direct_action_required":true,"DIRECT_ACTION_REQUIRED":null`, false, false},
		{"duplicate mixed null middle", `"direct_action_required":true,"Direct_Action_Required":null,"direct_action_required":false`, false, false},
		{"wrong type before valid", `"DIRECT_ACTION_REQUIRED":1,"direct_action_required":true`, false, false},
		{"unknown field", `"direct_action_required":true,"unknown":false`, false, false},
	}
	for _, tc := range cases {
		for _, kind := range []string{"create", "convert", "patch", "batch", "mixed batch"} {
			t.Run(tc.name+"/"+kind, func(t *testing.T) {
				fields := tc.fields
				prefix := `"thread":{"channel":"work","title":"gate","text":"brief"}`
				var dst any = &domain.CreateTaskRequest{}
				if kind == "convert" {
					prefix = `"thread_id":"thr_old"`
				}
				if kind == "patch" {
					prefix = `"title":"updated"`
					dst = &domain.PatchTaskRequest{}
				}
				if fields != "" {
					prefix += "," + fields
				}
				body := "{" + prefix + "}"
				if kind == "batch" || kind == "mixed batch" {
					dst = &domain.CreateTaskBatchRequest{}
					key := "tasks"
					if kind == "mixed batch" {
						key = "TaſKs"
					}
					body = `{"` + key + `":[{"ref":"gate",` + prefix + `}]}`
				}
				err := decode(httptest.NewRequest("POST", "/", strings.NewReader(body)), dst)
				if (err == nil) != tc.valid {
					t.Fatalf("%s: err=%v, valid=%v", body, err, tc.valid)
				}
				if err != nil {
					return
				}
				var got bool
				switch req := dst.(type) {
				case *domain.CreateTaskRequest:
					got = req.DirectActionRequired
				case *domain.PatchTaskRequest:
					if req.DirectActionRequired != nil {
						got = *req.DirectActionRequired
					}
				case *domain.CreateTaskBatchRequest:
					got = req.Tasks[0].DirectActionRequired
				}
				if got != tc.want {
					t.Fatalf("%s decoded flag=%v, want %v", body, got, tc.want)
				}
			})
		}
	}
}

func TestGateBooleanDuplicateBatchContainers(t *testing.T) {
	for _, body := range []string{
		`{"tasks":[{"DIRECT_ACTION_REQUIRED":null}],"tasks":[{"direct_action_required":true}]}`,
		`{"TASKS":[{"DIRECT_ACTION_REQUIRED":null}],"tasks":[{"direct_action_required":true}]}`,
		`{"tasks":[{"direct_action_required":true}],"TaſKs":[{"Direct_Action_Required":null}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			var req domain.CreateTaskBatchRequest
			if err := decode(httptest.NewRequest("POST", "/", strings.NewReader(body)), &req); err == nil {
				t.Fatalf("accepted null in duplicate batch container: %s", body)
			}
		})
	}
}
