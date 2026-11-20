// Package yak is a minimal Go client for the Yak HTTP protocol. It uses
// only the public API: no server packages, no database access.
package yak

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Problem is an RFC 9457 problem response with Yak's stable code.
type Problem struct {
	Status    int            `json:"status"`
	Code      string         `json:"code"`
	Detail    string         `json:"detail"`
	RequestID string         `json:"request_id"`
	Fields    map[string]any `json:"-"`
}

func (p *Problem) Error() string {
	return fmt.Sprintf("%d %s: %s (request %s)", p.Status, p.Code, p.Detail, p.RequestID)
}

// IsCode reports whether err is a Problem with the given code.
func IsCode(err error, code string) bool {
	var p *Problem
	return errors.As(err, &p) && p.Code == code
}

type Client struct {
	BaseURL   string // e.g. http://localhost:8080
	Token     string
	RunID     string // optional Yak-Run-Id attribution (no authority)
	HTTP      *http.Client
	UserAgent string
	// MaxRetries bounds retries of 503/network failures. Retries reuse the
	// same Idempotency-Key, so a committed-but-unacknowledged write is not
	// duplicated.
	MaxRetries int
}

func New(baseURL, token string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, HTTP: &http.Client{Timeout: 60 * time.Second}, UserAgent: "yak-go-sdk/0.1", MaxRetries: 3}
}

// NewIdempotencyKey returns a random key. Keep it until the outcome is known.
func NewIdempotencyKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "k-" + hex.EncodeToString(b)
}

// Request describes one call.
type Request struct {
	Method         string
	Path           string // relative to BaseURL, e.g. /v1/boards
	Query          url.Values
	Body           any
	IdempotencyKey string
	IfMatch        string // revision, e.g. "3"
}

// Response carries headers of a successful call.
type Response struct {
	Status   int
	Header   http.Header
	Replayed bool
}

// Do performs the request and decodes a 2xx JSON body into out (if non-nil).
func (c *Client) Do(ctx context.Context, r Request, out any) (*Response, error) {
	var body []byte
	if r.Body != nil {
		var err error
		if body, err = json.Marshal(r.Body); err != nil {
			return nil, err
		}
	}
	u := c.BaseURL + r.Path
	if len(r.Query) > 0 {
		u += "?" + r.Query.Encode()
	}
	// Only GETs and keyed writes are retried. A write guarded only by
	// If-Match is not: if it committed and the response was lost, a retry
	// would report 412 for a write that succeeded. Re-read instead.
	retryable := r.Method == http.MethodGet || r.IdempotencyKey != ""
	var lastErr error
	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * 200 * time.Millisecond):
			}
		}
		req, err := http.NewRequestWithContext(ctx, r.Method, u, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Accept", "application/json")
		if c.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.Token)
		}
		if c.RunID != "" {
			req.Header.Set("Yak-Run-Id", c.RunID)
		}
		if c.UserAgent != "" {
			req.Header.Set("User-Agent", c.UserAgent)
		}
		if r.IdempotencyKey != "" {
			req.Header.Set("Idempotency-Key", r.IdempotencyKey)
		}
		if r.IfMatch != "" {
			req.Header.Set("If-Match", `"`+strings.Trim(r.IfMatch, `"`)+`"`)
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = err
			if retryable {
				continue
			}
			return nil, err
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			if retryable {
				continue
			}
			return nil, err
		}
		if resp.StatusCode >= 400 {
			p := &Problem{Status: resp.StatusCode}
			_ = json.Unmarshal(data, p)
			_ = json.Unmarshal(data, &p.Fields)
			if p.Code == "" {
				p.Code = "http_" + fmt.Sprint(resp.StatusCode)
				p.Detail = strings.TrimSpace(string(data))
			}
			if resp.StatusCode == 503 && retryable && attempt < c.MaxRetries {
				lastErr = p
				continue
			}
			return nil, p
		}
		if out != nil && len(data) > 0 {
			if err := json.Unmarshal(data, out); err != nil {
				return nil, fmt.Errorf("decode response: %w", err)
			}
		}
		return &Response{Status: resp.StatusCode, Header: resp.Header, Replayed: resp.Header.Get("Idempotent-Replayed") == "true"}, nil
	}
	return nil, lastErr
}

// BoardPath joins a board-relative path.
func BoardPath(boardID, rel string) string {
	return "/v1/boards/" + url.PathEscape(boardID) + rel
}

// EventPage mirrors GET /events.
type EventPage struct {
	Events     []Event `json:"events"`
	NextCursor string  `json:"next_cursor"`
	CaughtUp   bool    `json:"caught_up"`
	TimedOut   bool    `json:"timed_out"`
}

type Event struct {
	ID          string          `json:"id"`
	Cursor      string          `json:"cursor"`
	Type        string          `json:"type"`
	BoardID     string          `json:"board_id"`
	ActorID     string          `json:"actor_id"`
	ChannelID   *string         `json:"channel_id"`
	ThreadID    *string         `json:"thread_id"`
	RecordedAt  time.Time       `json:"recorded_at"`
	EffectiveAt time.Time       `json:"effective_at"`
	Resource    json.RawMessage `json:"resource"`
	Payload     json.RawMessage `json:"payload"`
}

// Events reads one page (optionally long-polling up to wait).
func (c *Client) Events(ctx context.Context, boardID string, filter url.Values, cursor, from string, limit int, wait time.Duration) (*EventPage, error) {
	q := url.Values{}
	for k, v := range filter {
		q[k] = v
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	} else if from != "" {
		q.Set("from", from)
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	if wait > 0 {
		q.Set("wait_ms", fmt.Sprint(wait.Milliseconds()))
	}
	var pg EventPage
	_, err := c.Do(ctx, Request{Method: "GET", Path: BoardPath(boardID, "/events"), Query: q}, &pg)
	return &pg, err
}
