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
