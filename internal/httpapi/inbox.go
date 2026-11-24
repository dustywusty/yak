package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dustywusty/yak/internal/domain"
)

func (a *API) inboxRoutes() {
	const B = "/v1/boards/{board_id}"
	a.mux.HandleFunc("GET "+B+"/inbox", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		q := r.URL.Query()
		for name, values := range q {
			if (name != "cursor" && name != "limit" && name != "wait_ms") || len(values) != 1 {
				return domain.Validation("unsupported or repeated inbox query parameter %q", name)
			}
		}
		req := domain.ReadInboxRequest{Cursor: q.Get("cursor"), Limit: a.S.Cfg.DefaultPageSize, Token: strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))}
		if text, ok := q["limit"]; ok {
			n, err := strconv.Atoi(text[0])
			if err != nil || n < 1 || n > a.S.Cfg.MaxPageSize {
				return domain.Validation("limit must be 1..%d", a.S.Cfg.MaxPageSize)
			}
			req.Limit = n
		}
		if text, ok := q["wait_ms"]; ok {
			n, err := strconv.ParseInt(text[0], 10, 64)
			if err != nil || n < 0 || n > a.S.Cfg.MaxWait.Milliseconds() {
				return domain.Validation("wait_ms must be 0..%d", a.S.Cfg.MaxWait.Milliseconds())
			}
			req.Wait = time.Duration(n) * time.Millisecond
		}
		page, err := a.S.ReadInbox(r.Context(), p, r.PathValue("board_id"), req)
		if err != nil {
			return err
		}
		return writeJSON(w, 200, page)
	}))
	a.mux.HandleFunc("POST "+B+"/inbox/ack", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		var req domain.InboxAckRequest
		if err := decode(r, &req); err != nil {
			return err
		}
		id, err := idem(r, r.PathValue("board_id"))
		if err != nil {
			return err
		}
		out, replayed, err := a.S.AckInbox(r.Context(), p, r.PathValue("board_id"), id, req)
		if err != nil {
			return err
		}
		if replayed {
			w.Header().Set("Idempotent-Replayed", "true")
		}
		return writeJSON(w, 200, out)
	}))
}
