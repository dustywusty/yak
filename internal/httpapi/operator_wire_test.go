package httpapi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/dustywusty/yak/internal/domain"
)

func TestOP17044Consumer(t *testing.T) {
	out, err := exec.Command("node", "../../examples/viewer/test/emit-operator-wire.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("producer: %v: %s", err, out)
	}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	count := 0
	for scanner.Scan() {
		var artifact struct {
			Method, Path string
			Body         json.RawMessage
			Headers      map[string]string
		}
		if err := json.Unmarshal(scanner.Bytes(), &artifact); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(artifact.Method, artifact.Path, bytes.NewReader(artifact.Body))
		for k, v := range artifact.Headers {
			r.Header.Set(k, v)
		}
		var dst any
		switch {
		case strings.HasSuffix(r.URL.Path, "/tasks"):
			dst = &domain.CreateTaskRequest{}
		case strings.HasSuffix(r.URL.Path, "/claims"):
			dst = &domain.ClaimRequest{}
		case strings.HasSuffix(r.URL.Path, "/renew"):
			dst = &domain.RenewRequest{}
		case strings.HasSuffix(r.URL.Path, "/release"), strings.HasSuffix(r.URL.Path, "/unblock"):
			dst = &domain.ReasonRequest{}
		case strings.HasSuffix(r.URL.Path, "/assignee"):
			dst = &domain.AssigneeRequest{}
		case strings.HasSuffix(r.URL.Path, "/labels"):
			dst = &domain.LabelsRequest{}
		case strings.HasSuffix(r.URL.Path, "/messages"):
			dst = &domain.ReplyRequest{}
		case r.Method == "PATCH":
			dst = &domain.PatchTaskRequest{}
		default:
			t.Fatalf("unmapped %s", r.URL.Path)
		}
		if err := decode(r, dst); err != nil {
			t.Fatalf("%s: %v", r.URL.Path, err)
		}
		if strings.HasSuffix(r.URL.Path, "/release") || strings.HasSuffix(r.URL.Path, "/renew") {
			if _, err := claimProof(r, true); err != nil {
				t.Fatal(err)
			}
			r.Header.Del("Yak-Claim-Proof")
			if _, err := claimProof(r, true); err == nil {
				t.Fatal("missing proof accepted")
			}
		}
		if _, ok := dst.(*domain.PatchTaskRequest); ok || strings.HasSuffix(r.URL.Path, "/assignee") || strings.HasSuffix(r.URL.Path, "/unblock") {
			revision, err := ifMatch(r, true)
			if err != nil || revision == nil || *revision != 3 {
				t.Fatalf("revision: %v %v", revision, err)
			}
			r.Header.Set("If-Match", "invalid")
			if _, err := ifMatch(r, true); err == nil {
				t.Fatal("bad revision accepted")
			}
		}
		if _, ok := dst.(*domain.CreateTaskRequest); ok {
			mutant := bytes.Replace(artifact.Body, []byte(`"thread":`), []byte(`"invalid_thread":`), 1)
			if err := decode(httptest.NewRequest("POST", r.URL.Path, bytes.NewReader(mutant)), &domain.CreateTaskRequest{}); err == nil {
				t.Fatal("renamed thread accepted")
			}
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 13 {
		t.Fatalf("executed %d artifacts", count)
	}
	for _, body := range []string{`{"direct_action_required":"true"}`, `{"direct_action_required":null}`} {
		if err := decode(httptest.NewRequest("PATCH", "/", strings.NewReader(body)), &domain.PatchTaskRequest{}); err == nil {
			t.Fatalf("bad gate accepted: %s", body)
		}
	}
	// Exercise the actual mux fallback without authentication or storage fixtures.
	api := New(&domain.Service{Cfg: domain.DefaultConfig()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"POST", "/v1/boards/brd_test/unknown", 404}, {"PUT", "/v1/boards/brd_test/tasks", 405}} {
		w := httptest.NewRecorder()
		api.mux.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status {
			t.Fatalf("%s %s: %d", tc.method, tc.path, w.Code)
		}
	}
	t.Logf("validated %d emitted artifacts with production HTTP helpers", count)
}
