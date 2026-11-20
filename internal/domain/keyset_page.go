package domain

import (
	"slices"
	"strings"
	"time"
)

// PageRequest is a keyset page request (PAGE-001, pagination.go): the
// opaque page_token ("" for the first page) and a limit already checked
// against 1..MaxPageSize by the HTTP layer. Every keyset list takes one.
type PageRequest struct {
	Token string
	Limit int
}

// after decodes the token into the position to resume after, or nil for
// the first page. A malformed token is 400 validation_failed.
func (r PageRequest) after() (*keyRef, error) {
	t, id, ok, err := DecodeKeyset(r.Token)
	if err != nil || !ok {
		return nil, err
	}
	return &keyRef{T: t, ID: id}, nil
}

// keyRef is a position in keyset order: (created_at, id), or (recorded_at,
// id) for audit events.
type keyRef struct {
	T  time.Time
	ID string
}

// sqlArgs returns the keyset bound as query arguments: whether a bound
// applies, and its time and id. It is safe on a nil position.
func (k *keyRef) sqlArgs() (has bool, t time.Time, id string) {
	if k == nil {
		return false, time.Time{}, ""
	}
	return true, k.T, k.ID
}

// token returns the page token that resumes after k.
func (k keyRef) token() *string {
	s := EncodeKeyset(k.T, k.ID)
	return &s
}

func (k keyRef) compare(o keyRef) int {
	if c := k.T.Compare(o.T); c != 0 {
		return c
	}
	return strings.Compare(k.ID, o.ID)
}

// keysetPage returns one page of a list in (created_at, id) ascending order.
//
// fetch returns up to n candidates strictly after the position "after" (nil
// for the start), in ascending order. load turns a candidate into an item and
// reports whether the caller may see it. Hidden candidates are skipped, and
// the next page token is the position of the last item returned, so a
// per-caller visibility filter never causes a gap or a repeat. The token is
// nil when no visible item follows the page.
func keysetPage[V any](pg PageRequest,
	fetch func(after *keyRef, n int) ([]keyRef, error),
	load func(k keyRef) (V, bool, error)) ([]V, *string, error) {
	after, err := pg.after()
	if err != nil {
		return nil, nil, err
	}
	limit := pg.Limit
	out := []V{}
	var last keyRef
	// Fetch a little more than one page of candidates per round trip, so a
	// run of hidden candidates does not cost one query each.
	n := max(limit+1, 32)
	for {
		batch, err := fetch(after, n)
		if err != nil {
			return nil, nil, err
		}
		for _, k := range batch {
			v, visible, err := load(k)
			if err != nil {
				return nil, nil, err
			}
			if !visible {
				continue
			}
			if len(out) == limit {
				return out, last.token(), nil
			}
			out = append(out, v)
			last = k
		}
		if len(batch) < n {
			return out, nil, nil
		}
		b := batch[len(batch)-1]
		after = &b
	}
}

// trimPage is keysetPage for lists without a per-caller visibility filter:
// rows were fetched in keyset order with LIMIT limit+1. It cuts them to
// limit and returns the token after the last returned row, or nil on the
// last page.
func trimPage[T any](rows []T, limit int, key func(T) keyRef) ([]T, *string) {
	if len(rows) <= limit {
		return rows, nil
	}
	rows = rows[:limit]
	return rows, key(rows[len(rows)-1]).token()
}

// sliceFetch adapts an in-memory candidate list to keysetPage's fetch.
func sliceFetch(keys []keyRef) func(after *keyRef, n int) ([]keyRef, error) {
	slices.SortFunc(keys, keyRef.compare)
	return func(after *keyRef, n int) ([]keyRef, error) {
		i := 0
		if after != nil {
			i, _ = slices.BinarySearchFunc(keys, *after, keyRef.compare)
			for i < len(keys) && keys[i].compare(*after) <= 0 {
				i++
			}
		}
		return keys[i:min(i+n, len(keys))], nil
	}
}
