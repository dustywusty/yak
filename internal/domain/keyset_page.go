package domain

import (
	"slices"
	"strings"
	"time"
)

// keyRef is a candidate's position in (created_at, id) order.
type keyRef struct {
	T  time.Time
	ID string
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
func keysetPage[V any](pageToken string, limit int,
	fetch func(after *keyRef, n int) ([]keyRef, error),
	load func(k keyRef) (V, bool, error)) ([]V, *string, error) {
	t, id, ok, err := DecodeKeyset(pageToken)
	if err != nil {
		return nil, nil, err
	}
	var after *keyRef
	if ok {
		after = &keyRef{T: t, ID: id}
	}
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
				next := EncodeKeyset(last.T, last.ID)
				return out, &next, nil
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
