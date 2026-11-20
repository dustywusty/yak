package domain

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// Hub fans out Postgres NOTIFY wakeups to in-process long-poll waiters. It is
// a latency hint only: waiters also re-query durable state on a timer, and no
// waiter holds a database connection while it sleeps (design §12.3).
type Hub struct {
	mu      sync.Mutex
	waiters map[string]map[chan struct{}]struct{}
}

func NewHub() *Hub { return &Hub{waiters: map[string]map[chan struct{}]struct{}{}} }

// Subscribe returns a channel that receives a value when boardID changes.
func (h *Hub) Subscribe(boardID string) (chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	if h.waiters[boardID] == nil {
		h.waiters[boardID] = map[chan struct{}]struct{}{}
	}
	h.waiters[boardID][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.waiters[boardID], ch)
		if len(h.waiters[boardID]) == 0 {
			delete(h.waiters, boardID)
		}
		h.mu.Unlock()
	}
}

func (h *Hub) Notify(boardID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.waiters[boardID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (h *Hub) notifyAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, m := range h.waiters {
		for ch := range m {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
}

// Listen keeps one LISTEN connection per process and reconnects on failure.
func (h *Hub) Listen(ctx context.Context, url string, log *slog.Logger) {
	for ctx.Err() == nil {
		err := h.listenOnce(ctx, url)
		if ctx.Err() != nil {
			return
		}
		log.Warn("event listener disconnected; retrying", "err", err)
		h.notifyAll() // wake waiters so they re-query durable state
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

func (h *Hub) listenOnce(ctx context.Context, url string) error {
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(ctx, `LISTEN yak_events`); err != nil {
		return err
	}
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		h.Notify(n.Payload)
	}
}
