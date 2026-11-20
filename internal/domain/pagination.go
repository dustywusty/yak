package domain

import (
	"encoding/base64"
	"encoding/json"
	"time"
)

// Keyset pagination convention for administrative lists (boards, channels,
// participants, grants, credentials, actors, audit events). Decided by the
// coordinator on abby-dev, 2026-11-20:
//
//   - query: page_token (opaque), limit (1..MaxPageSize, default DefaultPageSize)
//   - response: the existing list field plus "next_page_token" (string|null;
//     null on the last page)
//   - order: (created_at, id) ascending; audit events (recorded_at, id)
//     descending. Ties are broken by id, so a page boundary never skips or
//     repeats an existing item under concurrent inserts.
//   - token: "k1." + base64url(JSON {"t": RFC3339Nano, "id": id}) of the last
//     item returned. A malformed token is 400 validation_failed.

type keyset struct {
	T  time.Time `json:"t"`
	ID string    `json:"id"`
}

// EncodeKeyset returns the page token that resumes after (t, id).
func EncodeKeyset(t time.Time, id string) string {
	b, _ := json.Marshal(keyset{T: t.UTC(), ID: id})
	return "k1." + base64.RawURLEncoding.EncodeToString(b)
}

// DecodeKeyset parses a page token; ok=false for the empty (first page) token.
func DecodeKeyset(tok string) (t time.Time, id string, ok bool, err error) {
	if tok == "" {
		return time.Time{}, "", false, nil
	}
	raw, found := cutPrefix(tok, "k1.")
	if !found {
		return time.Time{}, "", false, Validation("malformed page_token")
	}
	b, e := base64.RawURLEncoding.DecodeString(raw)
	var k keyset
	if e != nil || json.Unmarshal(b, &k) != nil || k.ID == "" {
		return time.Time{}, "", false, Validation("malformed page_token")
	}
	return k.T, k.ID, true, nil
}
