package domain_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/dustywusty/yak/internal/domain"
	"github.com/dustywusty/yak/internal/postgres"
)

func inboxPost(t *testing.T, s *domain.Service, p *domain.Principal, b, key string) {
	t.Helper()
	_, err := s.CreateThread(context.Background(), p, b, &domain.Idem{Key: key, Namespace: b, Method: "POST", Path: "/threads"}, domain.CreateThreadRequest{Channel: "inbox", Title: key, Text: "durable work", Mentions: []string{p.ActorID}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestInboxRestartKeepsUnacknowledgedItemsAndPrefix(t *testing.T) {
	s, p, b := setup(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		inboxPost(t, s, p, b, fmt.Sprint("inbox", i))
	}
	first, err := s.ReadInbox(ctx, p, b, domain.ReadInboxRequest{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.UnreadCount != 3 || first.CaughtUp {
		t.Fatalf("wrong first page: %+v", first)
	}
	replay, err := s.ReadInbox(ctx, p, b, domain.ReadInboxRequest{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if replay.Items[0].ID != first.Items[0].ID {
		t.Fatal("reading implicitly acknowledged work")
	}
	// Reconnect through a new pool and Service, without in-process state.
	pool, err := postgres.Connect(ctx, os.Getenv("YAK_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	restarted := domain.New(pool, s.Cfg)
	after, err := restarted.ReadInbox(ctx, p, b, domain.ReadInboxRequest{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if after.Items[0].ID != first.Items[0].ID {
		t.Fatal("restart lost unacknowledged item")
	}
	if _, _, err := restarted.AckInbox(ctx, p, b, &domain.Idem{Key: "ack-first", Namespace: b, Method: "POST", Path: "/inbox/ack"}, domain.InboxAckRequest{Cursor: first.AckCursor}); err != nil {
		t.Fatal(err)
	}
	next, err := s.ReadInbox(ctx, p, b, domain.ReadInboxRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Items) != 2 || next.UnreadCount != 2 || next.Items[0].ID == first.Items[0].ID {
		t.Fatalf("prefix ack consumed wrong work: %+v", next)
	}
	// Reusing an old issued boundary cannot move the saved ack backwards.
	if _, _, err := s.AckInbox(ctx, p, b, &domain.Idem{Key: "ack-all", Namespace: b, Method: "POST", Path: "/inbox/ack"}, domain.InboxAckRequest{Cursor: next.AckCursor}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AckInbox(ctx, p, b, &domain.Idem{Key: "ack-old", Namespace: b, Method: "POST", Path: "/inbox/ack"}, domain.InboxAckRequest{Cursor: first.AckCursor}); err != nil {
		t.Fatal(err)
	}
	empty, err := restarted.ReadInbox(ctx, p, b, domain.ReadInboxRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Items) != 0 || empty.UnreadCount != 0 {
		t.Fatalf("old ack moved prefix backward: %+v", empty)
	}
	if _, err := postgres.RotateEpoch(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.ReadInbox(ctx, p, b, domain.ReadInboxRequest{Cursor: empty.NextCursor}); err == nil {
		t.Fatal("history reset accepted old cursor")
	}
}

func TestInboxConcurrentReadsShareIssuedBoundary(t *testing.T) {
	s, p, b := setup(t)
	for i := 0; i < 5; i++ {
		inboxPost(t, s, p, b, fmt.Sprint("concurrent", i))
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			page, err := s.ReadInbox(context.Background(), p, b, domain.ReadInboxRequest{})
			if err == nil && (len(page.Items) != 5 || page.UnreadCount != 5) {
				err = fmt.Errorf("wrong concurrent projection: %+v", page)
			}
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}
