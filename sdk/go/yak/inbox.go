package yak

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"time"
)

type InboxItem struct {
	ID         string    `json:"id"`
	BoardID    string    `json:"board_id"`
	ActorID    string    `json:"actor_id"`
	SourceKind string    `json:"source_kind"`
	SourceID   string    `json:"source_id"`
	MessageID  string    `json:"message_id"`
	ThreadID   string    `json:"thread_id"`
	CreatedAt  time.Time `json:"created_at"`
}

type InboxPage struct {
	Items       []InboxItem `json:"items"`
	NextCursor  string      `json:"next_cursor"`
	AckCursor   string      `json:"ack_cursor"`
	CaughtUp    bool        `json:"caught_up"`
	TimedOut    bool        `json:"timed_out"`
	UnreadCount int64       `json:"unread_count"`
}

type InboxAck struct {
	BoardID        string    `json:"board_id"`
	ActorID        string    `json:"actor_id"`
	AcknowledgedAt time.Time `json:"acknowledged_at"`
}

type ReadInboxOptions struct {
	Cursor string
	Limit  int
	Wait   time.Duration
}

// RequireCapability checks discovery before a client uses an optional surface.
func (c *Client) RequireCapability(ctx context.Context, capability string) error {
	var server struct {
		Capabilities []string `json:"capabilities"`
	}
	if _, err := c.Do(ctx, Request{Method: "GET", Path: "/v1/server"}, &server); err != nil {
		return err
	}
	if !slices.Contains(server.Capabilities, capability) {
		return fmt.Errorf("server does not advertise %s", capability)
	}
	return nil
}

// ReadInbox never acknowledges an item. Save AckCursor only after durable work.
func (c *Client) ReadInbox(ctx context.Context, board string, opts ReadInboxOptions) (*InboxPage, error) {
	q := url.Values{}
	if opts.Cursor != "" {
		q.Set("cursor", opts.Cursor)
	}
	if opts.Limit != 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Wait < 0 {
		return nil, fmt.Errorf("inbox wait must not be negative")
	}
	if opts.Wait != 0 {
		q.Set("wait_ms", strconv.FormatInt(opts.Wait.Milliseconds(), 10))
	}
	var page InboxPage
	_, err := c.Do(ctx, Request{Method: "GET", Path: "/v1/boards/" + url.PathEscape(board) + "/inbox", Query: q}, &page)
	return &page, err
}

// AckInbox explicitly consumes the prefix, including currently hidden items.
// Reuse key for an identical retry. A new read supplies a current-policy cursor.
func (c *Client) AckInbox(ctx context.Context, board, cursor, key string) (*InboxAck, error) {
	if cursor == "" {
		return nil, fmt.Errorf("ack cursor is required")
	}
	if key == "" {
		return nil, fmt.Errorf("ack idempotency key is required")
	}
	var ack InboxAck
	_, err := c.Do(ctx, Request{Method: "POST", Path: "/v1/boards/" + url.PathEscape(board) + "/inbox/ack", Body: map[string]string{"cursor": cursor}, IdempotencyKey: key}, &ack)
	return &ack, err
}
