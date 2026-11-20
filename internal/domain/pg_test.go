package domain_test

// Reference-implementation tests against real Postgres. They are separate
// from the black-box conformance suite because they inspect storage and
// drive operator actions (retention sweep, epoch rotation) directly.
// Set ABBY_TEST_DATABASE_URL to a disposable database.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/abby-protocol/abby/internal/domain"
	"github.com/abby-protocol/abby/internal/events"
	"github.com/abby-protocol/abby/internal/postgres"
	"github.com/abby-protocol/abby/internal/workers"
)

func setup(t *testing.T) (*domain.Service, *domain.Principal, string) {
	t.Helper()
	url := os.Getenv("ABBY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ABBY_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	if _, err := postgres.Migrate(ctx, url); err != nil {
		t.Fatal(err)
	}
	if applied, err := postgres.Migrate(ctx, url); err != nil || len(applied) != 0 {
		t.Fatalf("second migrate must be a no-op: %v %v", applied, err)
	}
	pool, err := postgres.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := postgres.CheckSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	svc := domain.New(pool, domain.DefaultConfig())
	// A test-local administrator, created like any actor by an existing admin
	// would be; bootstrap may already have run in this database.
	var adminID, credID string
	h := fmt.Sprintf("pgt%d", time.Now().UnixNano())
	if err := pool.QueryRow(ctx, `INSERT INTO actors(id, kind, handle, display_name, server_admin) VALUES ('act_'||$1,'human',$1,$1,true) RETURNING id`, h).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO credentials(id, actor_id, label, scopes, verifier, expires_at) VALUES ('cred_'||$1,$2,'t','{*}','\x00', now()+interval '1 day') RETURNING id`, h, adminID).Scan(&credID); err != nil {
		t.Fatal(err)
	}
	p := &domain.Principal{ActorID: adminID, CredentialID: credID, ServerAdmin: true, Scopes: []string{"*"}, ExpiresAt: time.Now().Add(time.Hour)}
	res, err := svc.CreateBoard(ctx, p, &domain.Idem{Key: h, Namespace: "server", Method: "POST", Path: "/v1/boards"}, domain.CreateBoardRequest{Name: h})
	if err != nil {
		t.Fatal(err)
	}
	board := res.ResourceID
	g := &domain.Idem{Key: h + "g", Namespace: board, Method: "POST", Path: "/g"}
	aid := adminID
	if _, err := svc.CreateGrant(ctx, p, board, g, domain.CreateGrantRequest{Subject: domain.GrantSubject{Type: "actor", ActorID: &aid}, Pattern: "*",
		Capabilities: []string{"read", "post", "channel_manage"}}); err != nil {
		t.Fatal(err)
	}
	return svc, p, board
}

func TestEventPositionsContiguousUnderConcurrency(t *testing.T) {
	svc, p, board := setup(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := svc.CreateThread(ctx, p, board, &domain.Idem{Key: fmt.Sprint("k", i), Namespace: board, Method: "POST", Path: "/t"},
				domain.CreateThreadRequest{Channel: fmt.Sprintf("c%d", i%4), Title: "t", Text: "x"})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var n, min, max, head int64
	if err := svc.DB.QueryRow(ctx, `SELECT count(*), min(position), max(position) FROM events WHERE board_id=$1`, board).Scan(&n, &min, &max); err != nil {
		t.Fatal(err)
	}
	if err := svc.DB.QueryRow(ctx, `SELECT head_position FROM board_heads WHERE board_id=$1`, board).Scan(&head); err != nil {
		t.Fatal(err)
	}
	if min != 1 || max != n || head != n {
		t.Fatalf("positions must be gapless 1..N: n=%d min=%d max=%d head=%d", n, min, max, head)
	}
}

func TestRetentionAndEpoch(t *testing.T) {
	svc, p, board := setup(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := svc.CreateThread(ctx, p, board, &domain.Idem{Key: fmt.Sprint("r", i), Namespace: board, Method: "POST", Path: "/t"},
			domain.CreateThreadRequest{Channel: "c", Title: "t", Text: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := svc.ReadEvents(ctx, p, board, domain.ReadEventsRequest{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	old := page.Events[0].Cursor
	// Expire everything older than now.
	svc.Cfg.EventRetention = -time.Second
	if err := workers.Sweep(ctx, svc); err != nil {
		t.Fatal(err)
	}
	_, err = svc.ReadEvents(ctx, p, board, domain.ReadEventsRequest{Cursor: old, Limit: 10})
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != "cursor_expired" || de.Status != 410 {
		t.Fatalf("want 410 cursor_expired, got %v", err)
	}
	latest, err := svc.ReadEvents(ctx, p, board, domain.ReadEventsRequest{From: "latest", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := postgres.RotateEpoch(ctx, svc.DB); err != nil {
		t.Fatal(err)
	}
	_, err = svc.ReadEvents(ctx, p, board, domain.ReadEventsRequest{Cursor: latest.NextCursor, Limit: 10})
	if !errors.As(err, &de) || de.Code != "resync_required" || de.Extra["reason"] != "history_reset" {
		t.Fatalf("want resync_required/history_reset after epoch rotation, got %v", err)
	}
	c, _ := events.DecodeCursor(latest.NextCursor)
	if c.BoardID != board {
		t.Fatal("cursor board binding")
	}
}
