package domain

import "time"

// PageRequest is a decoded keyset page request (see pagination.go).
type PageRequest struct {
	Token string // opaque page_token; "" for the first page
	Limit int    // 1..MaxPageSize, validated by the HTTP layer
}

// pageAfter decodes the request's token into SQL arguments: has reports
// whether a keyset bound applies, and t/id are the bound.
func (r PageRequest) pageAfter() (has bool, t time.Time, id string, err error) {
	t, id, has, err = DecodeKeyset(r.Token)
	return has, t, id, err
}

// trimPage cuts rows fetched with LIMIT limit+1 down to limit and returns the
// token that resumes after the last returned row, or nil on the last page.
func trimPage[T any](rows []T, limit int, key func(T) (time.Time, string)) ([]T, *string) {
	if len(rows) <= limit {
		return rows, nil
	}
	rows = rows[:limit]
	t, id := key(rows[len(rows)-1])
	tok := EncodeKeyset(t, id)
	return rows, &tok
}
