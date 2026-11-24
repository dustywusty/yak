package httpapi

import (
	"bytes"
	"encoding/json"
	"github.com/dustywusty/yak/internal/domain"
	"net/http"
)

func (a *API) presenceRoutes() {
	const B = "/v1/boards/{board_id}"
	a.mux.HandleFunc("GET "+B+"/actors/{actor_id}/presence", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		out, err := a.S.GetPresence(r.Context(), p, r.PathValue("board_id"), r.PathValue("actor_id"))
		if err != nil {
			return err
		}
		etag(w, out.Revision)
		return writeJSON(w, 200, out)
	}))
	a.mux.HandleFunc("POST "+B+"/presence", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		var wire struct {
			State string          `json:"state"`
			TTL   json.RawMessage `json:"ttl_seconds"`
		}
		if err := decode(r, &wire); err != nil {
			return err
		}
		req := domain.PresenceHeartbeatRequest{State: wire.State}
		if wire.TTL != nil {
			var n int
			if bytes.Equal(bytes.TrimSpace(wire.TTL), []byte("null")) {
				return domain.Validation("ttl_seconds must not be null")
			}
			if err := json.Unmarshal(wire.TTL, &n); err != nil {
				return domain.Validation("ttl_seconds must be an integer")
			}
			req.TTLSeconds = &n
		}
		id, err := idem(r, r.PathValue("board_id"))
		if err != nil {
			return err
		}
		out, replayed, err := a.S.HeartbeatPresence(r.Context(), p, r.PathValue("board_id"), id, req)
		if err != nil {
			return err
		}
		if replayed {
			w.Header().Set("Idempotent-Replayed", "true")
		}
		etag(w, out.Revision)
		return writeJSON(w, 200, out)
	}))
}
