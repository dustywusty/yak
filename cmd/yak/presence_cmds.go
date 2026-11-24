package main

import (
	"fmt"
	"github.com/dustywusty/yak/sdk/go/yak"
	"github.com/spf13/cobra"
	"os"
	"time"
)

func presenceOutput(out *yak.ActorPresence) error {
	if gf.json {
		return printJSON(out)
	}
	fmt.Fprintf(os.Stdout, "%s  %s", out.ActorID, out.State)
	if out.LastSeenAt != nil {
		fmt.Fprintf(os.Stdout, "  last seen %s", out.LastSeenAt.Format(time.RFC3339Nano))
	}
	if out.ExpiresAt != nil {
		fmt.Fprintf(os.Stdout, "  expires %s", out.ExpiresAt.Format(time.RFC3339Nano))
	}
	fmt.Fprintln(os.Stdout)
	return nil
}
func presenceCmd() *cobra.Command {
	cmd := boardCmd("presence [ACTOR_ID]", "Read voluntary actor presence on a board", cobra.MaximumNArgs(1), func(cmd *cobra.Command, args []string, c *yak.Client, board string) error {
		if err := c.RequireCapability(cmd.Context(), "yak.presence"); err != nil {
			return err
		}
		actor := "me"
		if len(args) > 0 {
			actor = args[0]
		}
		if actor == "me" {
			var me struct {
				Actor struct {
					ID string `json:"id"`
				} `json:"actor"`
			}
			if _, err := c.Do(cmd.Context(), yak.Request{Method: "GET", Path: "/v1/me"}, &me); err != nil {
				return err
			}
			actor = me.Actor.ID
		}
		out, err := c.GetPresence(cmd.Context(), board, actor)
		if err != nil {
			return err
		}
		return presenceOutput(out)
	})
	set := boardCmd("set online|away|offline", "Publish a self heartbeat with an optional expiry", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string, c *yak.Client, board string) error {
		req := yak.PresenceHeartbeatRequest{State: args[0]}
		switch req.State {
		case "online", "away", "offline":
		default:
			return usage("state must be online, away or offline")
		}
		if cmd.Flags().Changed("ttl") {
			if req.State == "offline" {
				return usage("offline presence must not carry --ttl")
			}
			ttl, _ := cmd.Flags().GetDuration("ttl")
			if ttl%time.Second != 0 || ttl < 15*time.Second || ttl > 300*time.Second {
				return usage("--ttl must be whole seconds in 15s..300s")
			}
			n := int(ttl / time.Second)
			req.TTLSeconds = &n
		}
		if err := c.RequireCapability(cmd.Context(), "yak.presence"); err != nil {
			return err
		}
		out, err := c.HeartbeatPresence(cmd.Context(), board, req, idemKey(cmd))
		if err != nil {
			return err
		}
		return presenceOutput(out)
	})
	set.Flags().Duration("ttl", 120*time.Second, "Expiry for online or away (whole seconds, 15s..300s; default server120s)")
	addIdem(set)
	cmd.AddCommand(set)
	return cmd
}
