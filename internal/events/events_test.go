package events

import (
	"net/url"
	"testing"
)

func TestCursorRoundTrip(t *testing.T) {
	c := Cursor{BoardID: "brd_x", Epoch: "ep", Position: 12345, Query: "q", Policy: "p"}
	d, err := DecodeCursor(c.Encode())
	if err != nil || d != c {
		t.Fatalf("round trip: %v %+v", err, d)
	}
	for _, bad := range []string{"", "c1.", "c2.AAAA", "c1.!!!", c.Encode() + "AA"} {
		if _, err := DecodeCursor(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestFilterBeforeAfter(t *testing.T) {
	f, err := ParseFilter(url.Values{"label": {"status:open"}, "subtree": {"eng"}})
	if err != nil {
		t.Fatal(err)
	}
	before := Facet{ChannelPath: "eng/x", Labels: map[string]string{"status": "open"}}
	after := Facet{ChannelPath: "eng/x", Labels: map[string]string{"status": "done"}}
	if !f.MatchEvent("message.labels_changed", []Facet{before, after}) {
		t.Fatal("leaving the view must match")
	}
	if f.MatchEvent("message.labels_changed", []Facet{after}) {
		t.Fatal("unrelated must not match")
	}
	root := Facet{ChannelPath: "eng", RootLabels: map[string]string{"status": "open"}}
	if !f.MatchFacet(root) {
		t.Fatal("root labels apply")
	}
	if f.MatchFacet(Facet{ChannelPath: "engineering", RootLabels: map[string]string{"status": "open"}}) {
		t.Fatal("subtree boundary")
	}
}

func TestInvalidationIgnoresContentClauses(t *testing.T) {
	f, _ := ParseFilter(url.Values{"text": {"secret"}, "author_id": {"a1"}})
	if !f.MatchFacet(Facet{Invalidation: true, ChannelPath: "x", ThreadID: "t"}) {
		t.Fatal("invalidation must reach content-filtered consumers")
	}
	if f.MatchFacet(Facet{ChannelPath: "x", AuthorID: "a1", Text: "[redacted]"}) {
		t.Fatal("text should not match")
	}
}

func TestFilterHashNormalized(t *testing.T) {
	a, _ := ParseFilter(url.Values{"type": {"b", "a"}, "label": {"k:2", "k:1"}})
	b, _ := ParseFilter(url.Values{"type": {"a", "b"}, "label": {"k:1", "k:2"}})
	if a.Hash() != b.Hash() {
		t.Fatal("hash must be order independent")
	}
}

func TestRenameReservedLabelNamespaces(t *testing.T) {
	for _, key := range []string{"yak.task.state", "abby.task.state"} {
		if err := ValidateLabelKey(key, false); err == nil {
			t.Errorf("client accepted reserved key %q", key)
		}
		if err := ValidateLabelKey(key, true); err != nil {
			t.Errorf("history filter rejected %q: %v", key, err)
		}
	}
}
