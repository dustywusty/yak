// Package events holds the board event stream's pure pieces: opaque cursors
// bound to board/query/history epoch/policy, the shared filter vocabulary,
// and before/after match projections ("facets") (ADR-006).
package events

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/dustywusty/abby/internal/authz"
)

// Facet is a redaction-safe projection of an affected resource, used only to
// decide whether an event matches a filter. An event carries its before and
// after facets; it matches if either matches.
type Facet struct {
	// Invalidation facets (root-label view invalidation, redaction) match on
	// channel and thread clauses only, never on content clauses.
	Invalidation bool `json:"invalidation,omitempty"`
	// ThreadScope facets describe a whole thread (root-label view
	// invalidation): label clauses use RootLabels; author, mention and text
	// clauses are ignored because any message in the thread may be affected.
	ThreadScope bool              `json:"thread_scope,omitempty"`
	ChannelPath string            `json:"channel_path"`
	ThreadID    string            `json:"thread_id,omitempty"`
	AuthorID    string            `json:"author_id,omitempty"`
	Mentions    []string          `json:"mentions,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	RootLabels  map[string]string `json:"root_labels,omitempty"`
	Text        string            `json:"text,omitempty"`
}

// Filter is the shared vocabulary for lists, waits, streams and snapshots.
type Filter struct {
	Channel  string              `json:"channel,omitempty"` // exact path
	Subtree  string              `json:"subtree,omitempty"` // path and descendants
	ThreadID string              `json:"thread_id,omitempty"`
	AuthorID string              `json:"author_id,omitempty"`
	Mention  string              `json:"mention,omitempty"` // actor ID
	Labels   map[string][]string `json:"labels,omitempty"`  // key -> ORed values; empty slice = key present
	Types    []string            `json:"types,omitempty"`
	Text     string              `json:"text,omitempty"` // case-insensitive substring
}

// ParseFilter reads the filter query parameters.
//
//	channel=a/b  subtree=a  thread_id=..  author_id=..  mention=..
//	label=key  label=key:value (repeatable)  type=message.created (repeatable)
//	text=substring
func ParseFilter(q url.Values) (Filter, error) {
	f := Filter{
		Channel:  q.Get("channel"),
		Subtree:  q.Get("subtree"),
		ThreadID: q.Get("thread_id"),
		AuthorID: q.Get("author_id"),
		Mention:  q.Get("mention"),
		Text:     q.Get("text"),
	}
	if f.Channel != "" {
		if err := authz.ValidatePath(f.Channel); err != nil {
			return f, err
		}
	}
	if f.Subtree != "" {
		if err := authz.ValidatePath(f.Subtree); err != nil {
			return f, err
		}
	}
	for _, l := range q["label"] {
		k, v, hasV := strings.Cut(l, ":")
		k = strings.ToLower(k)
		if err := ValidateLabelKey(k, true); err != nil {
			return f, err
		}
		if f.Labels == nil {
			f.Labels = map[string][]string{}
		}
		if _, ok := f.Labels[k]; !ok {
			f.Labels[k] = []string{}
		}
		if hasV {
			f.Labels[k] = append(f.Labels[k], v)
		}
	}
	for k := range f.Labels {
		sort.Strings(f.Labels[k])
	}
	f.Types = slices.Clone(q["type"])
	sort.Strings(f.Types)
	return f, nil
}

// Hash binds cursors to a normalized query definition.
func (f Filter) Hash() string {
	b, _ := json.Marshal(f) // map keys are sorted by encoding/json
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}

func (f Filter) channelOK(path string) bool {
	if f.Channel != "" && f.Channel != path {
		return false
	}
	if f.Subtree != "" && !authz.InSubtree(f.Subtree, path) {
		return false
	}
	return true
}

// MatchFacet evaluates the content filter (not authorization) on one facet.
func (f Filter) MatchFacet(x Facet) bool {
	if !f.channelOK(x.ChannelPath) {
		return false
	}
	if f.ThreadID != "" && f.ThreadID != x.ThreadID {
		return false
	}
	if x.Invalidation {
		return true
	}
	if !x.ThreadScope && f.AuthorID != "" && f.AuthorID != x.AuthorID {
		return false
	}
	if !x.ThreadScope && f.Mention != "" && !slices.Contains(x.Mentions, f.Mention) {
		return false
	}
	for k, vals := range f.Labels {
		mv, mok := x.Labels[k]
		rv, rok := x.RootLabels[k]
		if !mok && !rok {
			return false
		}
		if len(vals) > 0 && !(mok && slices.Contains(vals, mv)) && !(rok && slices.Contains(vals, rv)) {
			return false
		}
	}
	if !x.ThreadScope && f.Text != "" && !strings.Contains(strings.ToLower(x.Text), strings.ToLower(f.Text)) {
		return false
	}
	return true
}

// MatchEvent applies type and facet clauses; the event matches if the type
// passes and any of its facets (before or after) matches.
func (f Filter) MatchEvent(typ string, facets []Facet) bool {
	if len(f.Types) > 0 && !slices.Contains(f.Types, typ) {
		return false
	}
	for _, x := range facets {
		if f.MatchFacet(x) {
			return true
		}
	}
	return false
}

// ValidateLabelKey checks a normalized (lowercase) label key. Keys are 1..64
// bytes of [a-z0-9._-], starting with [a-z0-9]. The "abby." prefix is
// reserved for server-maintained labels unless allowReserved (filters).
func ValidateLabelKey(k string, allowReserved bool) error {
	if len(k) == 0 || len(k) > 64 {
		return errors.New("label key must be 1..64 bytes")
	}
	for i, r := range k {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (i > 0 && (r == '.' || r == '_' || r == '-'))
		if !ok {
			return errors.New("label key must match [a-z0-9][a-z0-9._-]*")
		}
	}
	if !allowReserved && strings.HasPrefix(k, "abby.") {
		return errors.New("label keys starting with \"abby.\" are reserved for the server")
	}
	return nil
}

// Cursor is an opaque read position bound to a board, history epoch, query
// and effective policy. Wire form: "c1." + base64url(binary).
type Cursor struct {
	BoardID  string
	Epoch    string
	Position int64 // last examined position; next read starts at Position+1
	Query    string
	Policy   string
}

var ErrBadCursor = errors.New("malformed cursor")

func (c Cursor) Encode() string {
	var b []byte
	b = binary.AppendUvarint(b, uint64(c.Position))
	for _, s := range []string{c.BoardID, c.Epoch, c.Query, c.Policy} {
		b = binary.AppendUvarint(b, uint64(len(s)))
		b = append(b, s...)
	}
	return "c1." + base64.RawURLEncoding.EncodeToString(b)
}

func DecodeCursor(s string) (Cursor, error) {
	raw, ok := strings.CutPrefix(s, "c1.")
	if !ok {
		return Cursor{}, ErrBadCursor
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return Cursor{}, ErrBadCursor
	}
	pos, n := binary.Uvarint(b)
	if n <= 0 {
		return Cursor{}, ErrBadCursor
	}
	b = b[n:]
	var fields [4]string
	for i := range fields {
		l, n := binary.Uvarint(b)
		if n <= 0 || uint64(len(b)-n) < l {
			return Cursor{}, ErrBadCursor
		}
		fields[i] = string(b[n : n+int(l)])
		b = b[n+int(l):]
	}
	if len(b) != 0 {
		return Cursor{}, ErrBadCursor
	}
	return Cursor{Position: int64(pos), BoardID: fields[0], Epoch: fields[1], Query: fields[2], Policy: fields[3]}, nil
}
