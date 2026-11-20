package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/abby-protocol/abby/sdk/go/abby"
)

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	if !gf.json {
		enc.SetIndent("", "  ")
	}
	return enc.Encode(v)
}

// call runs one request and prints the JSON result.
func call(cmd *cobra.Command, r abby.Request) (map[string]any, error) {
	c, _, err := client()
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if _, err := c.Do(cmd.Context(), r, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func callPrint(cmd *cobra.Command, r abby.Request) error {
	out, err := call(cmd, r)
	if err != nil {
		return err
	}
	return printJSON(out)
}

// callPrintAll follows next_page_token across a keyset-paginated list and
// prints {field: [every item]} as one object. r.Query may be nil.
func callPrintAll(cmd *cobra.Command, r abby.Request, field string) error {
	c, _, err := client()
	if err != nil {
		return err
	}
	q := url.Values{}
	for k, v := range r.Query {
		q[k] = v
	}
	q.Set("limit", "200")
	all := []any{}
	for {
		var page map[string]json.RawMessage
		r.Query = q
		if _, err := c.Do(cmd.Context(), r, &page); err != nil {
			return err
		}
		var items []any
		if err := json.Unmarshal(page[field], &items); err != nil {
			return fmt.Errorf("decode %s: %w", field, err)
		}
		all = append(all, items...)
		var next *string
		if raw, ok := page["next_page_token"]; ok {
			if err := json.Unmarshal(raw, &next); err != nil {
				return fmt.Errorf("decode next_page_token: %w", err)
			}
		}
		if next == nil || *next == "" {
			return printJSON(map[string]any{field: all})
		}
		q.Set("page_token", *next)
	}
}

func idemKey(cmd *cobra.Command) string {
	if k, _ := cmd.Flags().GetString("idempotency-key"); k != "" {
		return k
	}
	return abby.NewIdempotencyKey()
}

func addIdem(cmd *cobra.Command) {
	cmd.Flags().String("idempotency-key", "", "Idempotency-Key to use (default: random). Reuse it to retry safely.")
}

func boardCmd(use, short string, args cobra.PositionalArgs, run func(cmd *cobra.Command, args []string, c *abby.Client, board string) error) *cobra.Command {
	return &cobra.Command{Use: use, Short: short, Args: args, RunE: func(cmd *cobra.Command, a []string) error {
		c, p, err := client()
		if err != nil {
			return err
		}
		b, err := boardID(p)
		if err != nil {
			return err
		}
		return run(cmd, a, c, b)
	}}
}

func splitList(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func parseKV(items []string) (map[string]string, error) {
	m := map[string]string{}
	for _, it := range items {
		k, v, ok := strings.Cut(it, "=")
		if !ok {
			return nil, usage("expected key=value, got %q", it)
		}
		m[k] = v
	}
	return m, nil
}

func textArg(cmd *cobra.Command) (string, error) {
	t, _ := cmd.Flags().GetString("text")
	if t == "-" || (t == "" && !cmd.Flags().Changed("text")) {
		if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice == 0 {
			b, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
			return strings.TrimRight(string(b), "\n"), err
		}
		if t == "-" {
			return "", usage("--text - given but stdin is a terminal")
		}
	}
	return t, nil
}

// filterFlags registers the shared filter vocabulary.
func filterFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String("channel", "", "exact channel path")
	f.String("subtree", "", "channel path and its descendants")
	f.String("thread", "", "thread ID")
	f.String("author", "", "author actor ID")
	f.String("mention", "", "mentioned actor ID")
	f.StringArray("label", nil, "label filter key or key:value (repeatable)")
	f.StringArray("type", nil, "event type (repeatable)")
	f.String("text", "", "case-insensitive text substring")
}

func filterValues(cmd *cobra.Command) url.Values {
	q := url.Values{}
	f := cmd.Flags()
	for flag, param := range map[string]string{"channel": "channel", "subtree": "subtree", "thread": "thread_id", "author": "author_id", "mention": "mention", "text": "text"} {
		if v, _ := f.GetString(flag); v != "" {
			q.Set(param, v)
		}
	}
	if v, _ := f.GetStringArray("label"); len(v) > 0 {
		q["label"] = v
	}
	if v, _ := f.GetStringArray("type"); len(v) > 0 {
		q["type"] = v
	}
	return q
}

func clientCmds() []*cobra.Command {
	var cmds []*cobra.Command

	cmds = append(cmds, &cobra.Command{
		Use: "server", Short: "Show server discovery (protocol, capabilities, limits)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return callPrint(cmd, abby.Request{Method: "GET", Path: "/v1/server"})
		},
	})

	login := &cobra.Command{
		Use: "login", Short: "Save an endpoint and token (read from stdin) to a profile",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name := profileName(cfg)
			p := cfg.Profiles[name]
			if v := os.Getenv("ABBY_URL"); v != "" {
				p.URL = v
			}
			if gf.url != "" {
				p.URL = gf.url
			}
			if gf.board != "" {
				p.Board = gf.board
			}
			if p.URL == "" {
				return usage("--url (or ABBY_URL) is required")
			}
			tok, err := readSecret(os.Stdin)
			if err != nil {
				return err
			}
			if tok == "" {
				return usage("pipe the token on stdin, e.g. `abby login --url URL < token.txt`")
			}
			p.Token = tok
			c := abby.New(p.URL, p.Token)
			var me map[string]any
			if _, err := c.Do(cmd.Context(), abby.Request{Method: "GET", Path: "/v1/me"}, &me); err != nil {
				return err
			}
			cfg.Profiles[name] = p
			cfg.Current = name
			if err := saveConfig(cfg); err != nil {
				return err
			}
			a, _ := me["actor"].(map[string]any)
			fmt.Fprintf(os.Stderr, "logged in to %s as %v (profile %q, config %s)\n", p.URL, a["handle"], name, configPath())
			return nil
		},
	}
	cmds = append(cmds, login)

	cmds = append(cmds, &cobra.Command{Use: "me", Short: "Show the authenticated actor and credential",
		RunE: func(cmd *cobra.Command, args []string) error {
			return callPrint(cmd, abby.Request{Method: "GET", Path: "/v1/me"})
		}})

	// actors
	actor := &cobra.Command{Use: "actor", Short: "Manage actors (server administration)"}
	actorCreate := &cobra.Command{Use: "create", Short: "Create an actor", RunE: func(cmd *cobra.Command, args []string) error {
		kind, _ := cmd.Flags().GetString("kind")
		handle, _ := cmd.Flags().GetString("handle")
		dn, _ := cmd.Flags().GetString("display-name")
		return callPrint(cmd, abby.Request{Method: "POST", Path: "/v1/actors", IdempotencyKey: idemKey(cmd),
			Body: map[string]any{"kind": kind, "handle": handle, "display_name": dn}})
	}}
	actorCreate.Flags().String("kind", "agent", "human | agent | service")
	actorCreate.Flags().String("handle", "", "unique handle")
	actorCreate.Flags().String("display-name", "", "display name")
	addIdem(actorCreate)
	actor.AddCommand(actorCreate, &cobra.Command{Use: "list", Short: "List all actors", RunE: func(cmd *cobra.Command, args []string) error {
		return callPrintAll(cmd, abby.Request{Method: "GET", Path: "/v1/actors"}, "actors")
	}})
	cmds = append(cmds, actor)

	// credentials
	cred := &cobra.Command{Use: "credential", Aliases: []string{"cred"}, Short: "Issue, list, and revoke credentials"}
	credCreate := &cobra.Command{Use: "create", Short: "Issue a credential; prints the token once on stdout", RunE: func(cmd *cobra.Command, args []string) error {
		actorID, _ := cmd.Flags().GetString("actor")
		scopes, _ := cmd.Flags().GetString("scopes")
		boards, _ := cmd.Flags().GetString("boards")
		label, _ := cmd.Flags().GetString("label")
		exp, _ := cmd.Flags().GetDuration("expires")
		body := map[string]any{"actor_id": actorID, "scopes": splitList(scopes), "label": label, "expires_in_seconds": int64(exp / time.Second)}
		if boards != "" {
			body["board_ids"] = splitList(boards)
		}
		out, err := call(cmd, abby.Request{Method: "POST", Path: "/v1/credentials", IdempotencyKey: idemKey(cmd), Body: body})
		if err != nil {
			return err
		}
		if gf.json {
			return printJSON(out)
		}
		c, _ := out["credential"].(map[string]any)
		fmt.Fprintf(os.Stderr, "credential %v for actor %v, scopes %v, expires %v\n", c["id"], c["actor_id"], c["scopes"], c["expires_at"])
		if tok, ok := out["token"].(string); ok && tok != "" {
			fmt.Println(tok)
		} else {
			fmt.Fprintln(os.Stderr, "token unavailable (idempotent replay); revoke this credential and issue a new one")
		}
		return nil
	}}
	credCreate.Flags().String("actor", "", "actor ID (default: yourself)")
	credCreate.Flags().String("scopes", "", "comma-separated scopes, e.g. read,post or *")
	credCreate.Flags().String("boards", "", "comma-separated board IDs to restrict to")
	credCreate.Flags().String("label", "", "label")
	credCreate.Flags().Duration("expires", 30*24*time.Hour, "lifetime")
	addIdem(credCreate)
	credList := &cobra.Command{Use: "list", Short: "List credentials", RunE: func(cmd *cobra.Command, args []string) error {
		a, _ := cmd.Flags().GetString("actor")
		q := url.Values{}
		if a != "" {
			q.Set("actor_id", a)
		}
		return callPrintAll(cmd, abby.Request{Method: "GET", Path: "/v1/credentials", Query: q}, "credentials")
	}}
	credList.Flags().String("actor", "", "actor ID (server administrators)")
	cred.AddCommand(credCreate, credList, &cobra.Command{Use: "revoke ID", Short: "Revoke a credential", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return callPrint(cmd, abby.Request{Method: "DELETE", Path: "/v1/credentials/" + url.PathEscape(args[0])})
		}})
	cmds = append(cmds, cred)

	// boards
	board := &cobra.Command{Use: "board", Short: "Manage boards"}
	boardCreate := &cobra.Command{Use: "create NAME", Short: "Create a board (server administration)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			owner, _ := cmd.Flags().GetString("owner")
			return callPrint(cmd, abby.Request{Method: "POST", Path: "/v1/boards", IdempotencyKey: idemKey(cmd),
				Body: map[string]any{"name": args[0], "owner_actor_id": owner}})
		}}
	boardCreate.Flags().String("owner", "", "owner actor ID (default: yourself)")
	addIdem(boardCreate)
	board.AddCommand(boardCreate,
		&cobra.Command{Use: "list", Short: "List visible boards", RunE: func(cmd *cobra.Command, args []string) error {
			return callPrint(cmd, abby.Request{Method: "GET", Path: "/v1/boards"})
		}},
		boardCmd("show", "Show the selected board", cobra.NoArgs, func(cmd *cobra.Command, args []string, c *abby.Client, b string) error {
			return callPrint(cmd, abby.Request{Method: "GET", Path: abby.BoardPath(b, "")})
		}),
		&cobra.Command{Use: "use ID", Short: "Select the default board for the current profile", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				cfg, err := loadConfig()
				if err != nil {
					return err
				}
				name := profileName(cfg)
				p := cfg.Profiles[name]
				p.Board = args[0]
				cfg.Profiles[name] = p
				return saveConfig(cfg)
			}})
	cmds = append(cmds, board)

	// channels
	channel := &cobra.Command{Use: "channel", Short: "Manage channels"}
	chCreate := boardCmd("create PATH", "Create a channel", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string, c *abby.Client, b string) error {
		priv, _ := cmd.Flags().GetBool("private")
		mode := "inherited"
		if priv {
			mode = "private"
		}
		return callPrint(cmd, abby.Request{Method: "POST", Path: abby.BoardPath(b, "/channels"), IdempotencyKey: idemKey(cmd),
			Body: map[string]any{"path": args[0], "access_mode": mode}})
	})
	chCreate.Flags().Bool("private", false, "create a private channel (allow-inheritance boundary)")
	addIdem(chCreate)
	channel.AddCommand(chCreate, boardCmd("list", "List channels", cobra.NoArgs, func(cmd *cobra.Command, args []string, c *abby.Client, b string) error {
		return callPrint(cmd, abby.Request{Method: "GET", Path: abby.BoardPath(b, "/channels")})
	}))
	cmds = append(cmds, channel)

	// grants
	grant := &cobra.Command{Use: "grant", Short: "Manage channel grants (board_admin)"}
	grCreate := boardCmd("create", "Create a grant", cobra.NoArgs, func(cmd *cobra.Command, args []string, c *abby.Client, b string) error {
		actorID, _ := cmd.Flags().GetString("actor")
		anyActor, _ := cmd.Flags().GetBool("any")
		pattern, _ := cmd.Flags().GetString("pattern")
		caps, _ := cmd.Flags().GetString("caps")
		deny, _ := cmd.Flags().GetBool("deny")
		if (actorID == "") == !anyActor {
			return usage("pass exactly one of --actor ID or --any")
		}
		subject := map[string]any{"type": "any_actor"}
		if actorID != "" {
			subject = map[string]any{"type": "actor", "actor_id": actorID}
		}
		effect := "allow"
		if deny {
			effect = "deny"
		}
		return callPrint(cmd, abby.Request{Method: "POST", Path: abby.BoardPath(b, "/grants"), IdempotencyKey: idemKey(cmd),
			Body: map[string]any{"subject": subject, "pattern": pattern, "capabilities": splitList(caps), "effect": effect}})
	})
	grCreate.Flags().String("actor", "", "subject actor ID")
	grCreate.Flags().Bool("any", false, "subject is any authenticated actor")
	grCreate.Flags().String("pattern", "*", `channel pattern: "*", "path", or "path/*"`)
	grCreate.Flags().String("caps", "", "comma-separated capabilities")
	grCreate.Flags().Bool("deny", false, "create a deny grant")
	addIdem(grCreate)
	grant.AddCommand(grCreate,
		boardCmd("list", "List grants", cobra.NoArgs, func(cmd *cobra.Command, args []string, c *abby.Client, b string) error {
			return callPrintAll(cmd, abby.Request{Method: "GET", Path: abby.BoardPath(b, "/grants")}, "grants")
		}),
		boardCmd("revoke ID", "Revoke a grant", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string, c *abby.Client, b string) error {
			return callPrint(cmd, abby.Request{Method: "DELETE", Path: abby.BoardPath(b, "/grants/"+url.PathEscape(args[0]))})
		}))
	cmds = append(cmds, grant)

	cmds = append(cmds,
		boardCmd("permissions CHANNEL", "Show your effective capabilities on a channel path", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string, c *abby.Client, b string) error {
			return callPrint(cmd, abby.Request{Method: "GET", Path: abby.BoardPath(b, "/permissions"), Query: url.Values{"channel": {args[0]}}})
		}),
		boardCmd("actors", "List board participants visible to you", cobra.NoArgs, func(cmd *cobra.Command, args []string, c *abby.Client, b string) error {
			return callPrint(cmd, abby.Request{Method: "GET", Path: abby.BoardPath(b, "/actors")})
		}),
		boardCmd("audit", "Show board audit events (board_admin)", cobra.NoArgs, func(cmd *cobra.Command, args []string, c *abby.Client, b string) error {
			return callPrintAll(cmd, abby.Request{Method: "GET", Path: abby.BoardPath(b, "/audit-events")}, "audit_events")
		}),
	)

	// messaging
	post := boardCmd("post CHANNEL", "Start a thread (text from --text or stdin)", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string, c *abby.Client, b string) error {
		title, _ := cmd.Flags().GetString("title")
		text, err := textArg(cmd)
		if err != nil {
			return err
		}
		labels, _ := cmd.Flags().GetStringArray("set-label")
		lm, err := parseKV(labels)
		if err != nil {
			return err
		}
		mentions, _ := cmd.Flags().GetStringArray("mention")
		out, err := call(cmd, abby.Request{Method: "POST", Path: abby.BoardPath(b, "/threads"), IdempotencyKey: idemKey(cmd),
			Body: map[string]any{"channel": args[0], "title": title, "text": text, "labels": lm, "mentions": mentions}})
		if err != nil {
			return err
		}
		if gf.json {
			return printJSON(out)
		}
		t, _ := out["thread"].(map[string]any)
		fmt.Printf("%v\n", t["id"])
		return nil
	})
	post.Flags().String("title", "", "thread title")
	post.Flags().String("text", "", "root message text (- or omit to read stdin)")
	post.Flags().StringArray("set-label", nil, "label key=value (repeatable)")
	post.Flags().StringArray("mention", nil, "mentioned actor ID (repeatable)")
	addIdem(post)

	reply := boardCmd("reply THREAD", "Reply to a thread", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string, c *abby.Client, b string) error {
		text, err := textArg(cmd)
		if err != nil {
			return err
		}
		to, _ := cmd.Flags().GetString("to")
		ifLatest, _ := cmd.Flags().GetString("if-latest")
		labels, _ := cmd.Flags().GetStringArray("set-label")
		lm, err := parseKV(labels)
		if err != nil {
			return err
		}
		mentions, _ := cmd.Flags().GetStringArray("mention")
		body := map[string]any{"text": text, "labels": lm, "mentions": mentions}
		if to != "" {
			body["reply_to_message_id"] = to
		}
		if ifLatest != "" {
			body["expect_latest_message_id"] = ifLatest
		}
		out, err := call(cmd, abby.Request{Method: "POST", Path: abby.BoardPath(b, "/threads/"+url.PathEscape(args[0])+"/messages"), IdempotencyKey: idemKey(cmd), Body: body})
		if err != nil {
			return err
		}
		if gf.json {
			return printJSON(out)
		}
		fmt.Printf("%v\n", out["id"])
		return nil
	})
	reply.Flags().String("text", "", "message text (- or omit to read stdin)")
	reply.Flags().String("to", "", "message ID this reply answers")
	reply.Flags().String("if-latest", "", "only post if this message is still the thread's latest (guarded reply)")
	reply.Flags().StringArray("set-label", nil, "label key=value (repeatable)")
	reply.Flags().StringArray("mention", nil, "mentioned actor ID (repeatable)")
	addIdem(reply)

	threads := boardCmd("threads", "List threads", cobra.NoArgs, func(cmd *cobra.Command, args []string, c *abby.Client, b string) error {
		q := filterValues(cmd)
		var page struct {
			Threads []map[string]any `json:"threads"`
			Next    *string          `json:"next_page_token"`
		}
		for {
			if _, err := c.Do(cmd.Context(), abby.Request{Method: "GET", Path: abby.BoardPath(b, "/threads"), Query: q}, &page); err != nil {
				return err
			}
			for _, t := range page.Threads {
				if gf.json {
					_ = json.NewEncoder(os.Stdout).Encode(t)
				} else {
					fmt.Printf("%v  %-20v  %3v msgs  %v\n", t["id"], t["channel_path"], t["message_count"], t["title"])
				}
			}
			if page.Next == nil {
				return nil
			}
			q.Set("page_token", *page.Next)
		}
	})
	filterFlags(threads)

	read := boardCmd("read THREAD", "Print a thread and all its messages", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string, c *abby.Client, b string) error {
		var tw map[string]any
		if _, err := c.Do(cmd.Context(), abby.Request{Method: "GET", Path: abby.BoardPath(b, "/threads/"+url.PathEscape(args[0]))}, &tw); err != nil {
			return err
		}
		var all []map[string]any
		q := url.Values{"limit": {"200"}}
		for {
			var page struct {
				Messages []map[string]any `json:"messages"`
				Next     *string          `json:"next_page_token"`
			}
			if _, err := c.Do(cmd.Context(), abby.Request{Method: "GET", Path: abby.BoardPath(b, "/threads/"+url.PathEscape(args[0])+"/messages"), Query: q}, &page); err != nil {
				return err
			}
			all = append(all, page.Messages...)
			if page.Next == nil {
				break
			}
			q.Set("page_token", *page.Next)
		}
		if gf.json {
			return printJSON(map[string]any{"thread": tw["thread"], "messages": all})
		}
		t, _ := tw["thread"].(map[string]any)
		fmt.Printf("# %v  [%v]  %v\n", t["title"], t["channel_path"], t["id"])
		for _, m := range all {
			text := "(deleted)"
			if s, ok := m["text"].(string); ok {
				text = s
			}
			lbl := ""
			if l, ok := m["labels"].(map[string]any); ok && len(l) > 0 {
				b, _ := json.Marshal(l)
				lbl = " " + string(b)
			}
			fmt.Printf("\n[%v] %v  rev %v%s  %v\n%s\n", m["seq"], m["author_actor_id"], m["revision"], lbl, m["id"], text)
		}
		return nil
	})

	msg := &cobra.Command{Use: "message", Aliases: []string{"msg"}, Short: "Show, edit, or delete a message"}
	msgEdit := boardCmd("edit ID", "Edit your message (requires --if-revision)", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string, c *abby.Client, b string) error {
		text, err := textArg(cmd)
		if err != nil {
			return err
		}
		rev, _ := cmd.Flags().GetString("if-revision")
		return callPrint(cmd, abby.Request{Method: "PATCH", Path: abby.BoardPath(b, "/messages/"+url.PathEscape(args[0])), IfMatch: rev, Body: map[string]any{"text": text}})
	})
	msgEdit.Flags().String("text", "", "new text")
	msgEdit.Flags().String("if-revision", "", "expected current revision (required)")
	msgDel := boardCmd("delete ID", "Tombstone a message (requires --if-revision)", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string, c *abby.Client, b string) error {
		rev, _ := cmd.Flags().GetString("if-revision")
		reason, _ := cmd.Flags().GetString("reason")
		return callPrint(cmd, abby.Request{Method: "DELETE", Path: abby.BoardPath(b, "/messages/"+url.PathEscape(args[0])), IfMatch: rev, Body: map[string]any{"reason": reason}})
	})
	msgDel.Flags().String("if-revision", "", "expected current revision (required)")
	msgDel.Flags().String("reason", "", "reason (required to redact another actor's message)")
	msg.AddCommand(boardCmd("show ID", "Show a message", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string, c *abby.Client, b string) error {
		return callPrint(cmd, abby.Request{Method: "GET", Path: abby.BoardPath(b, "/messages/"+url.PathEscape(args[0]))})
	}), msgEdit, msgDel)

	label := boardCmd("label MESSAGE", "Set or unset labels atomically, with optional expectations", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string, c *abby.Client, b string) error {
		sets, _ := cmd.Flags().GetStringArray("set")
		unsets, _ := cmd.Flags().GetStringArray("unset")
		absent, _ := cmd.Flags().GetStringArray("expect-absent")
		expect, _ := cmd.Flags().GetStringArray("expect")
		rev, _ := cmd.Flags().GetString("if-revision")
		expMap, err := parseKV(expect)
		if err != nil {
			return err
		}
		expFor := func(k string) map[string]any {
			for _, a := range absent {
				if a == k {
					return map[string]any{"absent": true}
				}
			}
			if v, ok := expMap[k]; ok {
				return map[string]any{"value": v}
			}
			return nil
		}
		var ops []map[string]any
		for _, s := range sets {
			k, v, ok := strings.Cut(s, "=")
			if !ok {
				return usage("--set expects key=value")
			}
			op := map[string]any{"op": "set", "key": k, "value": v}
			if e := expFor(k); e != nil {
				op["expect"] = e
			}
			ops = append(ops, op)
		}
		for _, k := range unsets {
			op := map[string]any{"op": "unset", "key": k}
			if e := expFor(k); e != nil {
				op["expect"] = e
			}
			ops = append(ops, op)
		}
		return callPrint(cmd, abby.Request{Method: "PATCH", Path: abby.BoardPath(b, "/messages/"+url.PathEscape(args[0])+"/labels"), IfMatch: rev,
			Body: map[string]any{"operations": ops}})
	})
	label.Flags().StringArray("set", nil, "key=value to set (repeatable)")
	label.Flags().StringArray("unset", nil, "key to remove (repeatable)")
	label.Flags().StringArray("expect-absent", nil, "require key to be absent before the change")
	label.Flags().StringArray("expect", nil, "require key=value before the change")
	label.Flags().String("if-revision", "", "optional expected message revision")

	cmds = append(cmds, post, reply, threads, read, msg, label)
	cmds = append(cmds, eventCmds()...)
	return cmds
}

// ---- events ----

func readCursorFile(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return strings.TrimSpace(string(b)), err
}

func writeCursorFile(path, cursor string) error {
	if path == "" || cursor == "" {
		return nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(cursor+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func printEvent(e abby.Event) {
	if gf.json {
		_ = json.NewEncoder(os.Stdout).Encode(e)
		return
	}
	var p struct {
		Message *struct {
			ID     string  `json:"id"`
			Text   *string `json:"text"`
			Author string  `json:"author_actor_id"`
		} `json:"message"`
		Thread *struct {
			Title string `json:"title"`
		} `json:"thread"`
	}
	_ = json.Unmarshal(e.Payload, &p)
	line := fmt.Sprintf("%s  %-24s  %s", e.RecordedAt.Format(time.RFC3339), e.Type, e.ActorID)
	if p.Thread != nil && e.Type == "thread.created" {
		line += fmt.Sprintf("  %q", p.Thread.Title)
	}
	if p.Message != nil && p.Message.Text != nil {
		t := *p.Message.Text
		if len(t) > 100 {
			t = t[:100] + "…"
		}
		line += "  " + strings.ReplaceAll(t, "\n", " ")
	}
	if e.ThreadID != nil {
		line += "  thread=" + *e.ThreadID
	}
	fmt.Println(line)
}

func eventCmds() []*cobra.Command {
	events := boardCmd("events", "Read one page of committed events", cobra.NoArgs, func(cmd *cobra.Command, args []string, c *abby.Client, b string) error {
		cursor, _ := cmd.Flags().GetString("cursor")
		from, _ := cmd.Flags().GetString("from")
		limit, _ := cmd.Flags().GetInt("limit")
		pg, err := c.Events(cmd.Context(), b, filterValues(cmd), cursor, from, limit, 0)
		if err != nil {
			return err
		}
		if gf.json {
			return printJSON(pg)
		}
		for _, e := range pg.Events {
			printEvent(e)
		}
		fmt.Fprintf(os.Stderr, "next_cursor: %s (caught_up=%v)\n", pg.NextCursor, pg.CaughtUp)
		return nil
	})
	events.Flags().String("cursor", "", "resume after this cursor")
	events.Flags().String("from", "", "earliest | latest (without --cursor)")
	events.Flags().Int("limit", 50, "max events")
	filterFlags(events)

	wait := boardCmd("wait", "Wait for one matching event; exit 7 on timeout", cobra.NoArgs, func(cmd *cobra.Command, args []string, c *abby.Client, b string) error {
		cf, _ := cmd.Flags().GetString("cursor-file")
		cursor, _ := cmd.Flags().GetString("cursor")
		timeout, _ := cmd.Flags().GetDuration("timeout")
		if cursor == "" {
			var err error
			if cursor, err = readCursorFile(cf); err != nil {
				return err
			}
		}
		from := ""
		if cursor == "" {
			from = "latest"
		}
		deadline := time.Now().Add(timeout)
		for {
			w := min(time.Until(deadline), 30*time.Second)
			if w < 0 {
				w = 0
			}
			pg, err := c.Events(cmd.Context(), b, filterValues(cmd), cursor, from, 1, w)
			if err != nil {
				return err
			}
			from = ""
			if len(pg.Events) > 0 {
				printEvent(pg.Events[0])
				return writeCursorFile(cf, pg.Events[0].Cursor)
			}
			cursor = pg.NextCursor
			if err := writeCursorFile(cf, cursor); err != nil {
				return err
			}
			if time.Now().After(deadline) || (timeout == 0) {
				return &exitErr{exitTimeout, errors.New("no matching event before timeout")}
			}
		}
	})
	wait.Flags().String("cursor", "", "resume after this cursor (default: --cursor-file, else from now)")
	wait.Flags().String("cursor-file", "", "file holding the read position; updated on return")
	wait.Flags().Duration("timeout", 30*time.Second, "maximum total wait")
	filterFlags(wait)

	tail := boardCmd("tail", "Follow matching events until interrupted", cobra.NoArgs, func(cmd *cobra.Command, args []string, c *abby.Client, b string) error {
		cf, _ := cmd.Flags().GetString("cursor-file")
		from, _ := cmd.Flags().GetString("from")
		cursor, err := readCursorFile(cf)
		if err != nil {
			return err
		}
		if cursor != "" {
			from = ""
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		backoff := time.Second
		for {
			pg, err := c.Events(ctx, b, filterValues(cmd), cursor, from, 100, 30*time.Second)
			if ctx.Err() != nil {
				return nil
			}
			if err != nil {
				var p *abby.Problem
				if errors.As(err, &p) {
					return err // auth, resync and cursor errors need the operator
				}
				fmt.Fprintf(os.Stderr, "abby: %v; reconnecting in %s\n", err, backoff)
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(backoff):
				}
				backoff = min(backoff*2, 30*time.Second)
				continue
			}
			backoff = time.Second
			from = ""
			for _, e := range pg.Events {
				printEvent(e)
			}
			cursor = pg.NextCursor
			if err := writeCursorFile(cf, cursor); err != nil {
				return err
			}
		}
	})
	tail.Flags().String("cursor-file", "", "file holding the read position (resumes and updates it)")
	tail.Flags().String("from", "latest", "earliest | latest when no cursor is stored")
	filterFlags(tail)

	snap := boardCmd("snapshot", "Capture a consistent snapshot; prints items and the continuation cursor", cobra.NoArgs, func(cmd *cobra.Command, args []string, c *abby.Client, b string) error {
		cf, _ := cmd.Flags().GetString("cursor-file")
		q := filterValues(cmd)
		var info struct {
			ID string `json:"id"`
		}
		if _, err := c.Do(cmd.Context(), abby.Request{Method: "POST", Path: abby.BoardPath(b, "/snapshots"), Query: q}, &info); err != nil {
			return err
		}
		pq := url.Values{"limit": {"200"}}
		var items []json.RawMessage
		for {
			var pg struct {
				Items        []json.RawMessage `json:"items"`
				Next         *string           `json:"next_page_token"`
				Continuation *string           `json:"continuation_cursor"`
			}
			if _, err := c.Do(context.Background(), abby.Request{Method: "GET", Path: abby.BoardPath(b, "/snapshots/"+info.ID), Query: pq}, &pg); err != nil {
				return err
			}
			items = append(items, pg.Items...)
			if pg.Next == nil {
				if err := writeCursorFile(cf, *pg.Continuation); err != nil {
					return err
				}
				_, _ = c.Do(cmd.Context(), abby.Request{Method: "DELETE", Path: abby.BoardPath(b, "/snapshots/"+info.ID)}, nil)
				return printJSON(map[string]any{"snapshot_id": info.ID, "items": items, "continuation_cursor": *pg.Continuation})
			}
			pq.Set("page_token", *pg.Next)
		}
	})
	snap.Flags().String("cursor-file", "", "write the continuation cursor here")
	filterFlags(snap)
	return []*cobra.Command{events, wait, tail, snap}
}
