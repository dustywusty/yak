package yak

import (
	"context"
	"fmt"
	"net/url"
	"time"
)

// ActorPresence is a voluntary heartbeat, not evidence of worker activity.
type ActorPresence struct {
	BoardID    string     `json:"board_id"`
	ActorID    string     `json:"actor_id"`
	State      string     `json:"state"`
	LastSeenAt *time.Time `json:"last_seen_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	Revision   int64      `json:"revision"`
}
type PresenceHeartbeatRequest struct {
	State      string `json:"state"`
	TTLSeconds *int   `json:"ttl_seconds,omitempty"`
}

func (c *Client) GetPresence(ctx context.Context, board, actor string) (*ActorPresence, error) {
	var out ActorPresence
	_, err := c.Do(ctx, Request{Method: "GET", Path: "/v1/boards/" + url.PathEscape(board) + "/actors/" + url.PathEscape(actor) + "/presence"}, &out)
	return &out, err
}

// HeartbeatPresence updates only the authenticated actor. Reuse key for an
// identical retry; a replay never refreshes the heartbeat's expiry.
func (c *Client) HeartbeatPresence(ctx context.Context, board string, req PresenceHeartbeatRequest, key string) (*ActorPresence, error) {
	if key == "" {
		return nil, fmt.Errorf("heartbeat idempotency key is required")
	}
	var out ActorPresence
	_, err := c.Do(ctx, Request{Method: "POST", Path: "/v1/boards/" + url.PathEscape(board) + "/presence", Body: req, IdempotencyKey: key}, &out)
	return &out, err
}
