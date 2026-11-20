package main

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/dustywusty/yak/sdk/go/yak"
)

// pageLimit returns the server's limits.max_page_size from discovery, as a
// limit query value, so the CLI takes as few round trips as the server
// allows without exceeding a smaller published limit. It returns "" (use
// the server default) when discovery does not publish the limit.
func pageLimit(ctx context.Context, c *yak.Client) string {
	var d struct {
		Limits struct {
			MaxPageSize int `json:"max_page_size"`
		} `json:"limits"`
	}
	if _, err := c.Do(ctx, yak.Request{Method: "GET", Path: "/v1/server"}, &d); err != nil || d.Limits.MaxPageSize < 1 {
		return ""
	}
	return strconv.Itoa(d.Limits.MaxPageSize)
}

// callPrintPages runs a paginated GET (PAGE-001), follows next_page_token
// until it is null, and prints one object {listField: all items,
// "next_page_token": null}, so the output has the shape of a single last page.
// Every CLI list command that follows page tokens prints through it.
func callPrintPages(cmd *cobra.Command, r yak.Request, listField string) error {
	c, _, err := client()
	if err != nil {
		return err
	}
	q := url.Values{}
	for k, v := range r.Query {
		q[k] = v
	}
	if l := pageLimit(cmd.Context(), c); l != "" {
		q.Set("limit", l)
	}
	all := []json.RawMessage{}
	for {
		var page map[string]json.RawMessage
		r.Query = q
		if _, err := c.Do(cmd.Context(), r, &page); err != nil {
			return err
		}
		var items []json.RawMessage
		if err := json.Unmarshal(page[listField], &items); err != nil {
			return err
		}
		all = append(all, items...)
		var next *string
		if raw, ok := page["next_page_token"]; ok {
			if err := json.Unmarshal(raw, &next); err != nil {
				return err
			}
		}
		if next == nil {
			return printJSON(map[string]any{listField: all, "next_page_token": nil})
		}
		q.Set("page_token", *next)
	}
}
