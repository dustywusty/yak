package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/dustywusty/abby/sdk/go/abby"
)

// actorArgKeywords are the words the CLI gives a meaning to in actor
// arguments. IDs are opaque (WIRE-003), so any other value is sent as an
// actor ID. But a near miss of a keyword ("ME", "@me") or a keyword from
// another command ("eligible") is almost always a mistake, and the server
// would silently match nothing. The CLI rejects those.
var actorArgKeywords = []string{"me", "none", "unassigned", "eligible"}

// actorArg resolves an actor argument for flag. "me" becomes the caller's
// actor ID (GET /v1/me). When allowNone is true, "none" (and "unassigned",
// the word `task next` uses) become "none". Empty stays empty.
func actorArg(ctx context.Context, c *abby.Client, flag, v string, allowNone bool) (string, error) {
	accepted := "me or an actor ID"
	if allowNone {
		accepted = "me, none, or an actor ID"
	}
	switch v {
	case "":
		return "", nil
	case "me":
		return meActorID(ctx, c)
	case "none", "unassigned":
		if allowNone {
			return "none", nil
		}
		return "", usage("%s %s is not accepted here; pass %s", flag, v, accepted)
	case "eligible":
		return "", usage("%s eligible applies only to `task next`; pass %s", flag, accepted)
	}
	w := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(v), "@"))
	for _, k := range actorArgKeywords {
		if w == k {
			return "", usage("%s %q is not a known value; pass %s", flag, v, accepted)
		}
	}
	if strings.HasPrefix(v, "@") {
		return "", usage("%s %q looks like a handle; pass %s (see `abby actors`)", flag, v, accepted)
	}
	return v, nil
}

// meActorID returns the authenticated actor's ID.
func meActorID(ctx context.Context, c *abby.Client) (string, error) {
	var me struct {
		Actor struct {
			ID string `json:"id"`
		} `json:"actor"`
	}
	if _, err := c.Do(ctx, abby.Request{Method: "GET", Path: "/v1/me"}, &me); err != nil {
		return "", fmt.Errorf("resolve me: %w", err)
	}
	if me.Actor.ID == "" {
		return "", fmt.Errorf("resolve me: GET /v1/me returned no actor id")
	}
	return me.Actor.ID, nil
}
