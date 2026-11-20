package domain_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dustywusty/abby/internal/domain"
)

// Timestamp ties cannot be forced through the HTTP API, so this reference
// test writes rows that share one timestamp and checks the id tie-breaker:
// every row appears exactly once at every page size (PAGE-001).
func TestKeysetPagingBreaksTimestampTies(t *testing.T) {
	svc, p, board := setup(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	tag := fmt.Sprint(time.Now().UnixNano())
	for i := 0; i < 5; i++ {
		if _, err := svc.DB.Exec(ctx, `INSERT INTO audit_events(id, board_id, actor_id, action, target_type, target_id, recorded_at)
			VALUES ($1, $2, $3, 'test.tie', 'test', $1, $4)`, fmt.Sprintf("aud_tie%s_%d", tag, i), board, p.ActorID, at.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.DB.Exec(ctx, `INSERT INTO credentials(id, actor_id, label, scopes, verifier, expires_at, created_at)
			VALUES ($1, $2, 't', '{read}', '\x00', now()+interval '1 day', $3)`, fmt.Sprintf("cred_tie%s_%d", tag, i), p.ActorID, at); err != nil {
			t.Fatal(err)
		}
	}
	for _, limit := range []int{1, 2, 3, 200} {
		// Audit: newest first; the five tied rows lead, ordered by id descending.
		var audit []domain.AuditView
		pg := domain.PageRequest{Limit: limit}
		for {
			l, next, err := svc.ListAudit(ctx, p, board, pg)
			if err != nil {
				t.Fatal(err)
			}
			audit = append(audit, l...)
			if next == nil {
				break
			}
			pg.Token = *next
		}
		seen := map[string]bool{}
		for i, a := range audit {
			if seen[a.ID] {
				t.Fatalf("limit %d: audit %s repeated", limit, a.ID)
			}
			seen[a.ID] = true
			if i < 5 && a.ID != fmt.Sprintf("aud_tie%s_%d", tag, 4-i) {
				t.Fatalf("limit %d: audit position %d is %s, want tied rows newest first by id desc", limit, i, a.ID)
			}
		}
		// Credentials: ascending (created_at, id).
		var creds []domain.CredentialView
		pg = domain.PageRequest{Limit: limit}
		for {
			l, next, err := svc.ListCredentials(ctx, p, "", pg)
			if err != nil {
				t.Fatal(err)
			}
			creds = append(creds, l...)
			if next == nil {
				break
			}
			pg.Token = *next
		}
		n := 0
		for _, c := range creds {
			if c.CreatedAt.Equal(at) {
				if c.ID != fmt.Sprintf("cred_tie%s_%d", tag, n) {
					t.Fatalf("limit %d: tied credential %d is %s", limit, n, c.ID)
				}
				n++
			}
		}
		if n != 5 {
			t.Fatalf("limit %d: saw %d of 5 tied credentials", limit, n)
		}
	}
	if _, _, err := svc.ListGrants(ctx, p, board, false, domain.PageRequest{Token: "k1.e30", Limit: 1}); err == nil {
		t.Fatal("malformed page token must be rejected")
	}
}
