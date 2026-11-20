// Package httpapi is the public HTTP transport. It validates requests,
// authenticates callers, and projects domain results; it holds no
// authorization logic of its own (CLI, SDK, and MCP share the same rules).
package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/abby-protocol/abby/internal/domain"
	"github.com/abby-protocol/abby/internal/events"
	"github.com/abby-protocol/abby/internal/ids"
	"github.com/abby-protocol/abby/internal/postgres"
)

const maxBody = 1 << 20

type API struct {
	S   *domain.Service
	Log *slog.Logger
	mux *http.ServeMux
}

type ctxKey int

const (
	principalKey ctxKey = iota
	requestIDKey
)

func New(s *domain.Service, log *slog.Logger) *API {
	a := &API{S: s, Log: log, mux: http.NewServeMux()}
	a.routes()
	return a
}

func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rid := r.Header.Get("X-Request-Id")
	if rid == "" || len(rid) > 128 {
		rid = ids.New("req")
	}
	w.Header().Set("X-Request-Id", rid)
	r = r.WithContext(context.WithValue(r.Context(), requestIDKey, rid))
	start := time.Now()
	sw := &statusWriter{ResponseWriter: w, status: 200}
	a.mux.ServeHTTP(sw, r)
	// Logs omit bodies, tokens and secrets by default (§13).
	a.Log.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.status, "ms", time.Since(start).Milliseconds(), "request_id", rid)
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(c int) { s.status = c; s.ResponseWriter.WriteHeader(c) }
func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

type handler func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error

func (a *API) authed(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			a.problem(w, r, domain.E(401, "unauthenticated", "Authorization: Bearer <token> required"))
			return
		}
		p, err := a.S.Authenticate(r.Context(), strings.TrimSpace(tok))
		if err != nil {
			a.problem(w, r, err)
			return
		}
		if run := r.Header.Get("Abby-Run-Id"); run != "" {
			if len(run) > 128 {
				a.problem(w, r, domain.Validation("Abby-Run-Id exceeds 128 bytes"))
				return
			}
			p.RunID = run
		}
		if err := h(w, r, p); err != nil {
			a.problem(w, r, err)
		}
	}
}

func (a *API) problem(w http.ResponseWriter, r *http.Request, err error) {
	var de *domain.Error
	if !errors.As(err, &de) {
		if errors.Is(err, context.Canceled) {
			return // client went away; nothing to send
		}
		a.Log.Error("internal error", "err", err, "request_id", r.Context().Value(requestIDKey))
		de = domain.E(503, "temporarily_unavailable", "the server could not complete the request; retry with the same Idempotency-Key")
	}
	body := map[string]any{
		"type":       "urn:abby:problem:" + de.Code,
		"title":      http.StatusText(de.Status),
		"status":     de.Status,
		"code":       de.Code,
		"detail":     de.Detail,
		"request_id": r.Context().Value(requestIDKey),
	}
	for k, v := range de.Extra {
		body[k] = v
	}
	if de.Status == 503 {
		w.Header().Set("Retry-After", "1")
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(de.Status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeJSON(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(v)
}

func etag(w http.ResponseWriter, rev int64) {
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(rev, 10)))
}

// decode reads a JSON body strictly: unknown fields are rejected (§2.1).
func decode(r *http.Request, v any) error {
	ct := r.Header.Get("Content-Type")
	if ct != "" && !strings.HasPrefix(ct, "application/json") {
		return domain.Validation("Content-Type must be application/json")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return err
	}
	if len(body) > maxBody {
		return domain.E(413, "limit_exceeded", "request body exceeds %d bytes", maxBody)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return domain.Validation("invalid JSON body: %s", err)
	}
	if dec.More() {
		return domain.Validation("trailing data after JSON body")
	}
	return nil
}

var idemKeyRE = regexp.MustCompile(`^[\x21-\x7e]{1,255}$`)

func idem(r *http.Request, namespace string) (*domain.Idem, error) {
	k := r.Header.Get("Idempotency-Key")
	if k == "" {
		e := domain.Validation("Idempotency-Key header is required for this operation")
		e.Extra = map[string]any{"header": "Idempotency-Key"}
		return nil, e
	}
	if !idemKeyRE.MatchString(k) {
		return nil, domain.Validation("Idempotency-Key must be 1..255 visible ASCII characters")
	}
	return &domain.Idem{Key: k, Namespace: namespace, Method: r.Method, Path: r.URL.Path}, nil
}

func ifMatch(r *http.Request, required bool) (*int64, error) {
	h := r.Header.Get("If-Match")
	if h == "" {
		if required {
			e := domain.E(428, "precondition_required", "If-Match with the current revision ETag is required")
			e.Extra = map[string]any{"header": "If-Match"}
			return nil, e
		}
		return nil, nil
	}
	h = strings.TrimPrefix(strings.TrimSpace(h), "W/")
	n, err := strconv.ParseInt(strings.Trim(h, `"`), 10, 64)
	if err != nil {
		return nil, domain.Validation("If-Match must be a revision ETag such as \"3\"")
	}
	return &n, nil
}

func pageArgs(r *http.Request, s *domain.Service) (int64, int, error) {
	after, err := domain.DecodePageToken(r.URL.Query().Get("page_token"))
	if err != nil {
		return 0, 0, err
	}
	limit := s.Cfg.DefaultPageSize
	if l := r.URL.Query().Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 || n > s.Cfg.MaxPageSize {
			return 0, 0, domain.Validation("limit must be 1..%d", s.Cfg.MaxPageSize)
		}
		limit = n
	}
	return after, limit, nil
}

// respond writes a mutation result. On idempotent replay the current,
// authorized projection of the committed resource is regenerated, so
// redactions and revocations apply (ADR-003).
func (a *API) respond(w http.ResponseWriter, r *http.Request, p *domain.Principal, boardID string, res *domain.Result) error {
	if !res.Replayed {
		return writeJSON(w, res.Status, res.Body)
	}
	w.Header().Set("Idempotent-Replayed", "true")
	ctx := r.Context()
	var body any
	var err error
	switch res.ResourceType {
	case "actor":
		body, err = a.S.GetActor(ctx, p, res.ResourceID)
	case "credential":
		var c *domain.CredentialView
		if c, err = a.S.GetCredential(ctx, p, res.ResourceID); err == nil {
			body = domain.IssuedCredential{Credential: *c, TokenUnavailable: true}
		}
	case "board":
		body, err = a.S.GetBoard(ctx, p, res.ResourceID)
	case "channel":
		body, err = a.S.GetChannel(ctx, p, boardID, res.ResourceID)
	case "grant":
		body, err = a.S.GetGrant(ctx, p, boardID, res.ResourceID)
	case "thread":
		body, err = a.S.GetThread(ctx, p, boardID, res.ResourceID)
	case "message":
		body, err = a.S.GetMessage(ctx, p, boardID, res.ResourceID)
	case "task":
		var t *domain.TaskView
		if t, err = a.S.GetTask(ctx, p, boardID, res.ResourceID); err == nil {
			etag(w, t.Revision)
			body = t
		}
	case "task_batch":
		body, err = a.S.GetTaskBatch(ctx, p, boardID, res.ResourceID)
	case "claim":
		body, err = a.S.ReplayClaim(ctx, p, boardID, res.ResourceID)
	case "claim_renewal":
		body, err = a.S.GetClaim(ctx, p, boardID, res.ResourceID)
	case "brief":
		body, err = a.S.GetBriefWithTask(ctx, p, boardID, res.ResourceID)
	default:
		err = fmt.Errorf("unknown replay resource type %q", res.ResourceType)
	}
	if err != nil {
		return err
	}
	return writeJSON(w, res.Status, body)
}

// ---- discovery ----

func (a *API) discovery(w http.ResponseWriter, r *http.Request) {
	c := a.S.Cfg
	caps := []string{
		"abby.boards", "abby.channels", "abby.channels.private", "abby.grants", "abby.actors", "abby.credentials",
		"abby.threads", "abby.messages", "abby.messages.guarded_reply", "abby.messages.tombstones",
		"abby.labels", "abby.labels.conditional", "abby.mentions",
		"abby.events.catchup", "abby.events.long_poll", "abby.snapshots", "abby.idempotency", "abby.audit",
		"abby.tasks", "abby.tasks.claims", "abby.tasks.claim_next", "abby.tasks.batches", "abby.tasks.dependencies", "abby.tasks.briefs",
	}
	// TASK-003: present only when an operator replay key is configured.
	if a.S.ClaimKeys != nil {
		caps = append(caps, "abby.tasks.claim_proof_replay")
	}
	_ = writeJSON(w, 200, map[string]any{
		"protocol": map[string]any{
			"name":     "abby",
			"versions": []string{"1"},
			"status":   "draft",
			"profiles": []string{"abby.p1.core", "abby.p2.tasks"},
		},
		"implementation": map[string]any{"name": "abby-reference", "version": c.Version, "schema_version": postgres.RequiredVersion()},
		"capabilities":   caps,
		"limits": map[string]any{
			"max_request_bytes":           maxBody,
			"max_text_bytes":              c.MaxTextBytes,
			"max_title_bytes":             c.MaxTitleBytes,
			"max_labels_per_message":      c.MaxLabelsPerMessage,
			"max_label_key_bytes":         64,
			"max_label_value_bytes":       c.MaxLabelValueBytes,
			"max_channel_path_bytes":      255,
			"max_page_size":               c.MaxPageSize,
			"max_wait_ms":                 c.MaxWait.Milliseconds(),
			"snapshot_max_items":          c.SnapshotMaxItems,
			"credential_max_ttl_seconds":  int64(c.CredentialMaxTTL / time.Second),
			"claim_lease_default_seconds": int64(c.ClaimLeaseDefault / time.Second),
			"claim_lease_min_seconds":     int64(c.ClaimLeaseMin / time.Second),
			"claim_lease_max_seconds":     int64(c.ClaimLeaseMax / time.Second),
			"max_batch_tasks":             c.MaxBatchTasks,
			"max_dependencies_per_task":   c.MaxDependenciesPerTask,
			"max_task_result_bytes":       c.MaxTaskResultBytes,
			"max_evidence_items":          c.MaxEvidenceItems,
			"max_claim_wait_ms":           c.MaxClaimWait.Milliseconds(),
		},
		"retention": map[string]any{
			"idempotency_seconds": int64(c.IdempotencyRetention / time.Second),
			"events_seconds":      int64(c.EventRetention / time.Second),
			"snapshot_seconds":    int64(c.SnapshotTTL / time.Second),
		},
		"authentication": map[string]any{"methods": []string{"bearer"}},
	})
}

func (a *API) routes() {
	m := a.mux
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _ = writeJSON(w, 200, map[string]string{"status": "ok"}) })
	m.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if _, err := postgres.CheckSchema(ctx, a.S.DB); err != nil {
			_ = writeJSON(w, 503, map[string]string{"status": "unavailable", "reason": err.Error()})
			return
		}
		_ = writeJSON(w, 200, map[string]string{"status": "ready"})
	})
	m.HandleFunc("GET /v1/server", a.discovery)
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Distinguish a known path with the wrong method (405) from an
		// unknown path (404).
		var allow []string
		for _, meth := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
			r2 := r.Clone(r.Context())
			r2.Method = meth
			if _, pat := a.mux.Handler(r2); pat != "/" {
				allow = append(allow, meth)
			}
		}
		if len(allow) > 0 {
			w.Header().Set("Allow", strings.Join(allow, ", "))
			a.problem(w, r, domain.E(405, "method_not_allowed", "%s is not supported on this path", r.Method))
			return
		}
		a.problem(w, r, domain.E(404, "not_found", "no such route"))
	})

	m.HandleFunc("GET /v1/me", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		me, err := a.S.Me(r.Context(), p)
		if err != nil {
			return err
		}
		return writeJSON(w, 200, me)
	}))

	// actors (server administration)
	m.HandleFunc("POST /v1/actors", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		var req domain.CreateActorRequest
		if err := decode(r, &req); err != nil {
			return err
		}
		i, err := idem(r, "server")
		if err != nil {
			return err
		}
		res, err := a.S.CreateActor(r.Context(), p, i, req)
		if err != nil {
			return err
		}
		return a.respond(w, r, p, "", res)
	}))
	m.HandleFunc("GET /v1/actors", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		l, err := a.S.ListAllActors(r.Context(), p)
		if err != nil {
			return err
		}
		return writeJSON(w, 200, map[string]any{"actors": l})
	}))
	m.HandleFunc("GET /v1/actors/{actor_id}", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		v, err := a.S.GetActor(r.Context(), p, r.PathValue("actor_id"))
		if err != nil {
			return err
		}
		return writeJSON(w, 200, v)
	}))

	// credentials
	m.HandleFunc("POST /v1/credentials", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		var req domain.CreateCredentialRequest
		if err := decode(r, &req); err != nil {
			return err
		}
		i, err := idem(r, "server")
		if err != nil {
			return err
		}
		res, err := a.S.CreateCredential(r.Context(), p, i, req)
		if err != nil {
			return err
		}
		return a.respond(w, r, p, "", res)
	}))
	m.HandleFunc("GET /v1/credentials", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		l, err := a.S.ListCredentials(r.Context(), p, r.URL.Query().Get("actor_id"))
		if err != nil {
			return err
		}
		return writeJSON(w, 200, map[string]any{"credentials": l})
	}))
	m.HandleFunc("DELETE /v1/credentials/{credential_id}", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		c, err := a.S.RevokeCredential(r.Context(), p, r.PathValue("credential_id"))
		if err != nil {
			return err
		}
		return writeJSON(w, 200, c)
	}))
	m.HandleFunc("GET /v1/audit-events", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		l, err := a.S.ListAudit(r.Context(), p, "", 200)
		if err != nil {
			return err
		}
		return writeJSON(w, 200, map[string]any{"audit_events": l})
	}))

	// boards
	m.HandleFunc("POST /v1/boards", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		var req domain.CreateBoardRequest
		if err := decode(r, &req); err != nil {
			return err
		}
		i, err := idem(r, "server")
		if err != nil {
			return err
		}
		res, err := a.S.CreateBoard(r.Context(), p, i, req)
		if err != nil {
			return err
		}
		return a.respond(w, r, p, "", res)
	}))
	m.HandleFunc("GET /v1/boards", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		tok, limit, err := keysetArgs(r, a.S)
		if err != nil {
			return err
		}
		l, next, err := a.S.ListBoards(r.Context(), p, tok, limit)
		if err != nil {
			return err
		}
		return writeJSON(w, 200, map[string]any{"boards": l, "next_page_token": next})
	}))
	m.HandleFunc("GET /v1/boards/{board_id}", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		b, err := a.S.GetBoard(r.Context(), p, r.PathValue("board_id"))
		if err != nil {
			return err
		}
		etag(w, b.Revision)
		return writeJSON(w, 200, b)
	}))
	m.HandleFunc("PATCH /v1/boards/{board_id}", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		var req domain.PatchBoardRequest
		if err := decode(r, &req); err != nil {
			return err
		}
		im, err := ifMatch(r, true)
		if err != nil {
			return err
		}
		b, err := a.S.PatchBoard(r.Context(), p, r.PathValue("board_id"), *im, req)
		if err != nil {
			return err
		}
		etag(w, b.Revision)
		return writeJSON(w, 200, b)
	}))

	a.boardRoutes()
	a.taskRoutes()
}

func (a *API) boardRoutes() {
	m := a.mux
	const B = "/v1/boards/{board_id}"
	bid := func(r *http.Request) string { return r.PathValue("board_id") }

	m.HandleFunc("POST "+B+"/channels", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		var req domain.CreateChannelRequest
		if err := decode(r, &req); err != nil {
			return err
		}
		i, err := idem(r, bid(r))
		if err != nil {
			return err
		}
		res, err := a.S.CreateChannel(r.Context(), p, bid(r), i, req)
		if err != nil {
			return err
		}
		return a.respond(w, r, p, bid(r), res)
	}))
	m.HandleFunc("GET "+B+"/channels", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		tok, limit, err := keysetArgs(r, a.S)
		if err != nil {
			return err
		}
		l, next, err := a.S.ListChannels(r.Context(), p, bid(r), tok, limit)
		if err != nil {
			return err
		}
		return writeJSON(w, 200, map[string]any{"channels": l, "next_page_token": next})
	}))
	m.HandleFunc("GET "+B+"/channels/{channel_id}", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		c, err := a.S.GetChannel(r.Context(), p, bid(r), r.PathValue("channel_id"))
		if err != nil {
			return err
		}
		return writeJSON(w, 200, c)
	}))
	m.HandleFunc("GET "+B+"/permissions", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		v, err := a.S.Permissions(r.Context(), p, bid(r), r.URL.Query().Get("channel"))
		if err != nil {
			return err
		}
		return writeJSON(w, 200, v)
	}))
	m.HandleFunc("GET "+B+"/actors", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		tok, limit, err := keysetArgs(r, a.S)
		if err != nil {
			return err
		}
		l, next, err := a.S.BoardActors(r.Context(), p, bid(r), tok, limit)
		if err != nil {
			return err
		}
		return writeJSON(w, 200, map[string]any{"actors": l, "next_page_token": next})
	}))
	m.HandleFunc("GET "+B+"/audit-events", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		l, err := a.S.ListAudit(r.Context(), p, bid(r), 200)
		if err != nil {
			return err
		}
		return writeJSON(w, 200, map[string]any{"audit_events": l})
	}))

	// grants
	m.HandleFunc("POST "+B+"/grants", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		var req domain.CreateGrantRequest
		if err := decode(r, &req); err != nil {
			return err
		}
		i, err := idem(r, bid(r))
		if err != nil {
			return err
		}
		res, err := a.S.CreateGrant(r.Context(), p, bid(r), i, req)
		if err != nil {
			return err
		}
		return a.respond(w, r, p, bid(r), res)
	}))
	m.HandleFunc("GET "+B+"/grants", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		l, err := a.S.ListGrants(r.Context(), p, bid(r), r.URL.Query().Get("include_revoked") == "true")
		if err != nil {
			return err
		}
		return writeJSON(w, 200, map[string]any{"grants": l})
	}))
	m.HandleFunc("GET "+B+"/grants/{grant_id}", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		g, err := a.S.GetGrant(r.Context(), p, bid(r), r.PathValue("grant_id"))
		if err != nil {
			return err
		}
		etag(w, g.Revision)
		return writeJSON(w, 200, g)
	}))
	m.HandleFunc("PATCH "+B+"/grants/{grant_id}", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		var req domain.PatchGrantRequest
		if err := decode(r, &req); err != nil {
			return err
		}
		im, err := ifMatch(r, true)
		if err != nil {
			return err
		}
		g, err := a.S.PatchGrant(r.Context(), p, bid(r), r.PathValue("grant_id"), *im, req)
		if err != nil {
			return err
		}
		etag(w, g.Revision)
		return writeJSON(w, 200, g)
	}))
	m.HandleFunc("DELETE "+B+"/grants/{grant_id}", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		g, err := a.S.RevokeGrant(r.Context(), p, bid(r), r.PathValue("grant_id"))
		if err != nil {
			return err
		}
		return writeJSON(w, 200, g)
	}))

	// threads and messages
	m.HandleFunc("POST "+B+"/threads", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		var req domain.CreateThreadRequest
		if err := decode(r, &req); err != nil {
			return err
		}
		i, err := idem(r, bid(r))
		if err != nil {
			return err
		}
		res, err := a.S.CreateThread(r.Context(), p, bid(r), i, req)
		if err != nil {
			return err
		}
		return a.respond(w, r, p, bid(r), res)
	}))
	m.HandleFunc("GET "+B+"/threads", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		f, err := events.ParseFilter(r.URL.Query())
		if err != nil {
			return domain.Validation("%s", err)
		}
		after, limit, err := pageArgs(r, a.S)
		if err != nil {
			return err
		}
		pg, err := a.S.ListThreads(r.Context(), p, bid(r), f, after, limit)
		if err != nil {
			return err
		}
		return writeJSON(w, 200, pg)
	}))
	m.HandleFunc("GET "+B+"/threads/{thread_id}", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		t, err := a.S.GetThread(r.Context(), p, bid(r), r.PathValue("thread_id"))
		if err != nil {
			return err
		}
		etag(w, t.Thread.Revision)
		return writeJSON(w, 200, t)
	}))
	m.HandleFunc("PATCH "+B+"/threads/{thread_id}", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		var req domain.PatchThreadRequest
		if err := decode(r, &req); err != nil {
			return err
		}
		im, err := ifMatch(r, true)
		if err != nil {
			return err
		}
		t, err := a.S.PatchThread(r.Context(), p, bid(r), r.PathValue("thread_id"), *im, req)
		if err != nil {
			return err
		}
		etag(w, t.Revision)
		return writeJSON(w, 200, t)
	}))
	m.HandleFunc("POST "+B+"/threads/{thread_id}/messages", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		var req domain.ReplyRequest
		if err := decode(r, &req); err != nil {
			return err
		}
		i, err := idem(r, bid(r))
		if err != nil {
			return err
		}
		res, err := a.S.Reply(r.Context(), p, bid(r), r.PathValue("thread_id"), i, req)
		if err != nil {
			return err
		}
		return a.respond(w, r, p, bid(r), res)
	}))
	m.HandleFunc("GET "+B+"/threads/{thread_id}/messages", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		after, limit, err := pageArgs(r, a.S)
		if err != nil {
			return err
		}
		pg, err := a.S.ListMessages(r.Context(), p, bid(r), r.PathValue("thread_id"), after, limit)
		if err != nil {
			return err
		}
		return writeJSON(w, 200, pg)
	}))
	m.HandleFunc("GET "+B+"/messages/{message_id}", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		msg, err := a.S.GetMessage(r.Context(), p, bid(r), r.PathValue("message_id"))
		if err != nil {
			return err
		}
		etag(w, msg.Revision)
		return writeJSON(w, 200, msg)
	}))
	m.HandleFunc("PATCH "+B+"/messages/{message_id}", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		var req domain.EditMessageRequest
		if err := decode(r, &req); err != nil {
			return err
		}
		im, err := ifMatch(r, true)
		if err != nil {
			return err
		}
		msg, err := a.S.EditMessage(r.Context(), p, bid(r), r.PathValue("message_id"), *im, req)
		if err != nil {
			return err
		}
		etag(w, msg.Revision)
		return writeJSON(w, 200, msg)
	}))
	m.HandleFunc("DELETE "+B+"/messages/{message_id}", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		var req struct {
			Reason string `json:"reason"`
		}
		if err := decode(r, &req); err != nil {
			return err
		}
		im, err := ifMatch(r, true)
		if err != nil {
			return err
		}
		msg, err := a.S.DeleteMessage(r.Context(), p, bid(r), r.PathValue("message_id"), *im, req.Reason)
		if err != nil {
			return err
		}
		etag(w, msg.Revision)
		return writeJSON(w, 200, msg)
	}))
	m.HandleFunc("PATCH "+B+"/messages/{message_id}/labels", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		var req domain.LabelsRequest
		if err := decode(r, &req); err != nil {
			return err
		}
		im, err := ifMatch(r, false)
		if err != nil {
			return err
		}
		msg, err := a.S.MutateLabels(r.Context(), p, bid(r), r.PathValue("message_id"), im, req)
		if err != nil {
			return err
		}
		etag(w, msg.Revision)
		return writeJSON(w, 200, msg)
	}))

	// events and snapshots
	m.HandleFunc("GET "+B+"/events", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		q := r.URL.Query()
		f, err := events.ParseFilter(q)
		if err != nil {
			return domain.Validation("%s", err)
		}
		req := domain.ReadEventsRequest{Filter: f, Cursor: q.Get("cursor"), From: q.Get("from"), Limit: a.S.Cfg.DefaultPageSize,
			Token: strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))}
		if req.From != "" && req.From != "earliest" && req.From != "latest" {
			return domain.Validation("from must be earliest or latest")
		}
		if req.From != "" && req.Cursor != "" {
			return domain.Validation("from and cursor are mutually exclusive")
		}
		if l := q.Get("limit"); l != "" {
			n, err := strconv.Atoi(l)
			if err != nil || n < 1 || n > a.S.Cfg.MaxPageSize {
				return domain.Validation("limit must be 1..%d", a.S.Cfg.MaxPageSize)
			}
			req.Limit = n
		}
		if wm := q.Get("wait_ms"); wm != "" {
			n, err := strconv.Atoi(wm)
			if err != nil || n < 0 || time.Duration(n)*time.Millisecond > a.S.Cfg.MaxWait {
				return domain.Validation("wait_ms must be 0..%d", a.S.Cfg.MaxWait.Milliseconds())
			}
			req.Wait = time.Duration(n) * time.Millisecond
		}
		pg, err := a.S.ReadEvents(r.Context(), p, bid(r), req)
		if err != nil {
			return err
		}
		return writeJSON(w, 200, pg)
	}))
	m.HandleFunc("POST "+B+"/snapshots", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		var req struct {
			Filter map[string]any `json:"filter"`
		}
		if err := decode(r, &req); err != nil {
			return err
		}
		f, err := events.ParseFilter(r.URL.Query())
		if err != nil {
			return domain.Validation("%s", err)
		}
		if len(req.Filter) > 0 {
			return domain.Validation("pass the filter as query parameters, identical to GET /events")
		}
		info, err := a.S.CreateSnapshot(r.Context(), p, bid(r), f)
		if err != nil {
			return err
		}
		return writeJSON(w, 201, info)
	}))
	m.HandleFunc("GET "+B+"/snapshots/{snapshot_id}", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		after, limit, err := pageArgs(r, a.S)
		if err != nil {
			return err
		}
		pg, err := a.S.SnapshotPage(r.Context(), p, bid(r), r.PathValue("snapshot_id"), after, limit)
		if err != nil {
			return err
		}
		return writeJSON(w, 200, pg)
	}))
	m.HandleFunc("DELETE "+B+"/snapshots/{snapshot_id}", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		if err := a.S.DeleteSnapshot(r.Context(), p, bid(r), r.PathValue("snapshot_id")); err != nil {
			return err
		}
		w.WriteHeader(204)
		return nil
	}))
}
