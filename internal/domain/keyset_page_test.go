package domain

import (
	"fmt"
	"testing"
	"time"
)

// TestKeysetPageVisibility walks a list where most candidates are hidden,
// with every page size, and checks each visible item appears exactly once,
// in order, and that only the last page is short.
func TestKeysetPageVisibility(t *testing.T) {
	base := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	var keys []keyRef
	visible := map[string]bool{}
	var want []string
	for i := 0; i < 100; i++ {
		// Pairs share a timestamp so the id tie-breaker is exercised.
		k := keyRef{T: base.Add(time.Duration(i/2) * time.Microsecond), ID: fmt.Sprintf("x%03d", i)}
		keys = append(keys, k)
		if i%7 == 0 || i%11 == 0 {
			visible[k.ID] = true
			want = append(want, k.ID)
		}
	}
	load := func(k keyRef) (string, bool, error) { return k.ID, visible[k.ID], nil }
	for limit := 1; limit <= len(want)+1; limit++ {
		var got []string
		tok := ""
		for pages := 0; ; pages++ {
			if pages > len(want)+2 {
				t.Fatalf("limit %d: no end", limit)
			}
			page, next, err := keysetPage(tok, limit, sliceFetch(append([]keyRef(nil), keys...)), load)
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, page...)
			if next == nil {
				break
			}
			if len(page) != limit {
				t.Fatalf("limit %d: short non-final page %v", limit, page)
			}
			tok = *next
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("limit %d: got %v want %v", limit, got, want)
		}
	}
	if _, _, err := keysetPage("k1.zz", 1, sliceFetch(keys), load); err == nil {
		t.Fatal("malformed token accepted")
	}
}
