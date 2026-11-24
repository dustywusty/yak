package main

import (
	"fmt"
	"os"
	"time"

	"github.com/dustywusty/yak/sdk/go/yak"
	"github.com/spf13/cobra"
)

func inboxReadCmd(use string, wait bool) *cobra.Command {
	cmd := boardCmd(use, "Read visible inbox items without acknowledging them", cobra.NoArgs, func(cmd *cobra.Command, args []string, c *yak.Client, board string) error {
		if err := c.RequireCapability(cmd.Context(), "yak.inbox"); err != nil {
			return err
		}
		cursor, _ := cmd.Flags().GetString("cursor")
		limit, _ := cmd.Flags().GetInt("limit")
		budget := time.Duration(0)
		if wait {
			budget, _ = cmd.Flags().GetDuration("wait")
			if budget < 0 {
				return usage("--wait must not be negative")
			}
		}
		page, err := c.ReadInbox(cmd.Context(), board, yak.ReadInboxOptions{Cursor: cursor, Limit: limit, Wait: budget})
		if err != nil {
			return err
		}
		if gf.json {
			return printJSON(page)
		}
		for _, item := range page.Items {
			fmt.Fprintf(os.Stdout, "%s  %s  %s  thread %s\n", item.ID, item.SourceKind, item.SourceID, item.ThreadID)
		}
		fmt.Fprintf(os.Stdout, "%d visible unacknowledged; caught_up=%t timed_out=%t\nnext_cursor: %s\nack_cursor: %s\n", page.UnreadCount, page.CaughtUp, page.TimedOut, page.NextCursor, page.AckCursor)
		return nil
	})
	cmd.Flags().String("cursor", "", "Opaque inbox continuation cursor (default: saved acknowledgment)")
	cmd.Flags().Int("limit", 0, "Page size (default: server default)")
	if wait {
		cmd.Flags().Duration("wait", 30*time.Second, "Long-poll budget (maximum: server max_wait_ms)")
	}
	return cmd
}

func inboxCmd() *cobra.Command {
	cmd := inboxReadCmd("inbox", false)
	cmd.Short = "Read, wait for, or explicitly acknowledge your durable inbox"
	cmd.AddCommand(inboxReadCmd("list", false), inboxReadCmd("wait", true))
	ack := boardCmd("ack", "Acknowledge the prefix through a returned ack_cursor, including hidden items", cobra.NoArgs, func(cmd *cobra.Command, args []string, c *yak.Client, board string) error {
		cursor, _ := cmd.Flags().GetString("cursor")
		if cursor == "" {
			return usage("--cursor is required")
		}
		if err := c.RequireCapability(cmd.Context(), "yak.inbox"); err != nil {
			return err
		}
		out, err := c.AckInbox(cmd.Context(), board, cursor, idemKey(cmd))
		if err != nil {
			return err
		}
		if gf.json {
			return printJSON(out)
		}
		fmt.Fprintln(os.Stdout, "Acknowledged through the cursor, including hidden items.")
		return nil
	})
	ack.Flags().String("cursor", "", "Returned ack_cursor through which to acknowledge, including hidden items")
	addIdem(ack)
	cmd.AddCommand(ack)
	return cmd
}
