package httpapi

import (
	"net/http"
	"strconv"

	"github.com/dustywusty/yak/internal/domain"
)

// keysetArgs parses the PAGE-001 query parameters of a keyset-paginated
// administrative list: page_token (opaque, validated here) and limit
// (1..max_page_size, default default_page_size). Every keyset list route
// (boards, channels, board participants, grants, credentials, actors, and
// both audit routes) uses it.
func keysetArgs(r *http.Request, s *domain.Service) (domain.PageRequest, error) {
	tok := r.URL.Query().Get("page_token")
	if _, _, _, err := domain.DecodeKeyset(tok); err != nil {
		return domain.PageRequest{}, err
	}
	limit := s.Cfg.DefaultPageSize
	if l := r.URL.Query().Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 || n > s.Cfg.MaxPageSize {
			return domain.PageRequest{}, domain.Validation("limit must be 1..%d", s.Cfg.MaxPageSize)
		}
		limit = n
	}
	return domain.PageRequest{Token: tok, Limit: limit}, nil
}
