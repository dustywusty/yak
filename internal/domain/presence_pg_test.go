package domain_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/dustywusty/yak/internal/domain"
	"github.com/dustywusty/yak/internal/postgres"
)

func presenceFixture(t *testing.T) (*domain.Service, *domain.Principal, string) {
	t.Helper()
	s, p, b := setup(t)
	_, err := s.CreateThread(context.Background(), p, b, &domain.Idem{Key: "presence-channel", Namespace: b, Method: "POST", Path: "/threads"}, domain.CreateThreadRequest{Channel: "presence", Title: "presence", Text: "clock fixture"})
	if err != nil {
		t.Fatal(err)
	}
	return s, p, b
}
func heartbeat(t *testing.T, s *domain.Service, p *domain.Principal, b, key, state string, ttl *int) *domain.ActorPresence {
	t.Helper()
	out, _, err := s.HeartbeatPresence(context.Background(), p, b, &domain.Idem{Key: key, Namespace: b, Method: "POST", Path: "/presence"}, domain.PresenceHeartbeatRequest{State: state, TTLSeconds: ttl})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func presenceEventCount(t *testing.T, s *domain.Service, b string) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE board_id=$1 AND type='actor.presence_changed'`, b).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPresenceReadExpiresWithoutWorkerOnceAcrossConcurrentReaders(t *testing.T) {
	s, p, b := presenceFixture(t)
	ctx := context.Background()
	clock := time.Now().UTC().Truncate(time.Microsecond)
	s.Now = func() time.Time { return clock }
	initial := heartbeat(t, s, p, b, "online", "online", nil)
	if initial.Revision != 1 || initial.ExpiresAt.Sub(clock) != 120*time.Second {
		t.Fatalf("defaultTTL: %+v", initial)
	}
	clock = clock.Add(120 * time.Second)
	expired, err := s.GetPresence(ctx, p, b, p.ActorID)
	if err != nil {
		t.Fatal(err)
	}
	if expired.State != "offline" || expired.Revision != 2 || !expired.LastSeenAt.Equal(*initial.LastSeenAt) || !expired.ExpiresAt.Equal(*initial.ExpiresAt) {
		t.Fatalf("expiry: %+v", expired)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 30)
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				_, err := s.SweepPresenceExpiry(ctx, 100)
				errs <- err
			} else {
				out, err := s.GetPresence(ctx, p, b, p.ActorID)
				if err == nil && (out.State != "offline" || out.Revision != 2) {
					err = fmt.Errorf("unexpected expiry %+v", out)
				}
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := presenceEventCount(t, s, b); n != 2 {
		t.Fatalf("expiry events duplicated: %d", n)
	}
	var effective time.Time
	if err := s.DB.QueryRow(ctx, `SELECT effective_at FROM events WHERE board_id=$1 AND resource_type='actor_presence' AND resource_revision=2`, b).Scan(&effective); err != nil {
		t.Fatal(err)
	}
	if !effective.Equal(*initial.ExpiresAt) {
		t.Fatal("expiry effective time changed")
	}
}

func TestPresenceNewHeartbeatFencesExpiredStateAndReplayDoesNotRenew(t *testing.T) {
	s, p, b := presenceFixture(t)
	ctx := context.Background()
	clock := time.Now().UTC().Truncate(time.Microsecond)
	s.Now = func() time.Time { return clock }
	n := 15
	heartbeat(t, s, p, b, "original", "online", &n)
	clock = clock.Add(15 * time.Second)
	var wg sync.WaitGroup
	errs := make(chan error, 21)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.SweepPresenceExpiry(ctx, 100); errs <- err }()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _, err := s.HeartbeatPresence(ctx, p, b, &domain.Idem{Key: "new", Namespace: b, Method: "POST", Path: "/presence"}, domain.PresenceHeartbeatRequest{State: "away", TTLSeconds: &n})
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	current, err := s.GetPresence(ctx, p, b, p.ActorID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != "away" || current.Revision != 3 || !current.ExpiresAt.Equal(clock.Add(15*time.Second)) {
		t.Fatalf("new heartbeat overwritten %+v", current)
	}
	clock = clock.Add(time.Second)
	replay, replayed, err := s.HeartbeatPresence(ctx, p, b, &domain.Idem{Key: "original", Namespace: b, Method: "POST", Path: "/presence"}, domain.PresenceHeartbeatRequest{State: "online", TTLSeconds: &n})
	if err != nil {
		t.Fatal(err)
	}
	if !replayed || replay.State != "away" || replay.Revision != 3 || !replay.ExpiresAt.Equal(*current.ExpiresAt) {
		t.Fatalf("replay changed TTL: %+v", replay)
	}
	if n := presenceEventCount(t, s, b); n != 3 {
		t.Fatalf("unexpected events %d", n)
	}
}

func TestPresenceReconnectPreservesHeartbeatAndEffectiveExpiry(t *testing.T) {
	s, p, b := presenceFixture(t)
	ctx := context.Background()
	clock := time.Now().UTC().Truncate(time.Microsecond)
	s.Now = func() time.Time { return clock }
	n := 15
	first := heartbeat(t, s, p, b, "restart", "online", &n)
	pool, err := postgres.Connect(ctx, os.Getenv("YAK_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	restarted := domain.New(pool, s.Cfg)
	restarted.Now = func() time.Time { return clock }
	same, err := restarted.GetPresence(ctx, p, b, p.ActorID)
	if err != nil {
		t.Fatal(err)
	}
	if same.State != "online" || !same.ExpiresAt.Equal(*first.ExpiresAt) {
		t.Fatal("reconnect lost heartbeat")
	}
	clock = clock.Add(15 * time.Second)
	out, err := restarted.GetPresence(ctx, p, b, p.ActorID)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "offline" || out.Revision != 2 {
		t.Fatalf("restart missed expiry %+v", out)
	}
	if _, err := s.SweepPresenceExpiry(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if n := presenceEventCount(t, s, b); n != 2 {
		t.Fatal("restart duplicated expiry")
	}
}
