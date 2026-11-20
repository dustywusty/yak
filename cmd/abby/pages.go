package main

import (
	"encoding/json"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/abby-protocol/abby/sdk/go/abby"
)

// callPrintPages runs a paginated GET (PAGE-001), follows next_page_token
// until it is null, and prints one object {listField: all items,
// "next_page_token": null}, so the output has the shape of a single last page.
func callPrintPages(cmd *cobra.Command, r abby.Request, listField string) error {
	c, _, err := client()
	if err != nil {
		return err
	}
	q := url.Values{}
	for k, v := range r.Query {
		q[k] = v
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
