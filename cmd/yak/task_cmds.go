package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/dustywusty/yak/sdk/go/yak"
)

// ---- shared helpers ----

// proofArg reads the claim proof from --proof-file or YAK_CLAIM_PROOF.
// Proofs are never accepted as command-line values (like tokens).
func proofArg(cmd *cobra.Command) (string, error) {
	if f, _ := cmd.Flags().GetString("proof-file"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	if v := yakEnv("YAK_CLAIM_PROOF"); v != "" {
		return strings.TrimSpace(v), nil
	}
	return "", usage("this command needs the claim proof: pass --proof-file F or set YAK_CLAIM_PROOF")
}

func addProofFlag(cmd *cobra.Command) {
	cmd.Flags().String("proof-file", "", "file holding the claim proof (env YAK_CLAIM_PROOF)")
}

func writeSecretFile(path, s string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(s+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func addIfRevision(cmd *cobra.Command, required bool) {
	h := "expected task revision (If-Match)"
	if required {
		h += " (required)"
	} else {
		h += " (optional)"
	}
	cmd.Flags().Int64("if-revision", 0, h)
}

func ifRevision(cmd *cobra.Command, required bool) (int64, error) {
	r, _ := cmd.Flags().GetInt64("if-revision")
	if required && r <= 0 {
		return 0, usage("--if-revision is required (read it with `yak task show ID`)")
	}
	return r, nil
}

func leaseSeconds(cmd *cobra.Command) (int, error) {
	d, _ := cmd.Flags().GetDuration("lease")
	if d == 0 {
		return 0, nil
	}
	if d < time.Second || d%time.Second != 0 {
		return 0, usage("--lease must be a whole number of seconds, e.g. 120s")
	}
	return int(d / time.Second), nil
}

func addLease(cmd *cobra.Command) {
	cmd.Flags().Duration("lease", 0, "lease length, e.g. 120s (default: server claim_lease_default_seconds)")
}

// parseEvidence parses kind:ref[:note].
func parseEvidence(items []string) ([]yak.Evidence, error) {
	var out []yak.Evidence
	for _, it := range items {
		kind, rest, ok := strings.Cut(it, ":")
		if !ok || rest == "" {
			return nil, usage("--evidence expects kind:ref[:note], got %q", it)
		}
		if !validEvidenceKind(kind) {
			return nil, usage("--evidence kind must be %s; got %q (use other:REF:NOTE for anything else)", evidenceKindList, kind)
		}
		ev := yak.Evidence{Kind: kind, Ref: rest}
		// URLs contain ':'; only split a note off other kinds.
		if kind != "url" {
			if ref, note, ok := strings.Cut(rest, ":"); ok {
				ev.Ref, ev.Note = ref, note
			}
		}
		if strings.TrimSpace(ev.Ref) == "" {
			return nil, usage("--evidence ref must not be empty, got %q", it)
		}
		out = append(out, ev)
	}
	return out, nil
}

// Evidence kinds the protocol accepts (spec/p2-tasks.md CLAIM-031).
const evidenceKindList = "message, commit, url, artifact, or other"

func validEvidenceKind(kind string) bool {
	switch kind {
	case "message", "commit", "url", "artifact", "other":
		return true
	}
	return false
}

// commitEvidence turns --commit values into evidence, refusing empty IDs.
func commitEvidence(commits []string) ([]yak.Evidence, error) {
	var out []yak.Evidence
	for _, c := range commits {
		if strings.TrimSpace(c) == "" {
			return nil, usage("--commit must not be empty")
		}
		out = append(out, yak.Evidence{Kind: "commit", Ref: c})
	}
	return out, nil
}

func addEvidenceFlags(cmd *cobra.Command) {
	cmd.Flags().StringArray("evidence", nil, "evidence kind:ref[:note]; kind is message|commit|url|artifact|other (repeatable)")
	cmd.Flags().StringArray("commit", nil, "commit ID to record as evidence (repeatable)")
	cmd.Flags().String("data", "", "result data as a JSON object")
}

func evidenceFlags(cmd *cobra.Command) ([]yak.Evidence, json.RawMessage, error) {
	evs, _ := cmd.Flags().GetStringArray("evidence")
	ev, err := parseEvidence(evs)
	if err != nil {
		return nil, nil, err
	}
	commits, _ := cmd.Flags().GetStringArray("commit")
	cev, err := commitEvidence(commits)
	if err != nil {
		return nil, nil, err
	}
	ev = append(ev, cev...)
	var data json.RawMessage
	if d, _ := cmd.Flags().GetString("data"); d != "" {
		var m map[string]any
		if err := json.Unmarshal([]byte(d), &m); err != nil {
			return nil, nil, usage("--data must be a JSON object: %v", err)
		}
		data = json.RawMessage(d)
	}
	return ev, data, nil
}

// labelFilter turns --label k / k:v (repeatable) into claim-next labels.
func labelFilter(items []string) map[string][]string {
	if len(items) == 0 {
		return nil
	}
	m := map[string][]string{}
	for _, it := range items {
		k, v, ok := strings.Cut(it, ":")
		if _, seen := m[k]; !seen {
			m[k] = []string{}
		}
		if ok {
			m[k] = append(m[k], v)
		}
	}
	return m
}

func strPtr(s string) *string { return &s }

func printTask(t *yak.Task) error {
	if gf.json {
		return printJSON(t)
	}
	fmt.Println(taskLine(t))
	return nil
}

func taskLine(t *yak.Task) string {
	extra := ""
	if t.DirectActionRequired {
		extra = "  direct_action_required=true"
	}
	if t.CurrentClaim != nil {
		extra += fmt.Sprintf("  claim=%s by %s until %s", t.CurrentClaim.ID, t.CurrentClaim.ActorID, t.CurrentClaim.ExpiresAt.Format(time.RFC3339))
	}
	if t.AssigneeActorID != nil {
		extra += "  assignee=" + *t.AssigneeActorID
	}
	return fmt.Sprintf("%s  %-9s  rev %-3d epoch %-3d %-20s %s%s", t.ID, t.State, t.Revision, t.Epoch, t.ChannelPath, t.Title, extra)
}

func printTaskDetail(t *yak.Task) error {
	if gf.json {
		return printJSON(t)
	}
	fmt.Printf("%s  %s\n", t.ID, t.Title)
	fmt.Printf("  state %s  revision %d  epoch %d  retries %d  brief %d\n", t.State, t.Revision, t.Epoch, t.RetryCount, t.BriefRevision)
	fmt.Printf("  direct action required %t\n", t.DirectActionRequired)
	fmt.Printf("  channel %s  thread %s  origin %s\n", t.ChannelPath, t.ThreadID, t.Origin)
	a := "none"
	if t.AssigneeActorID != nil {
		a = *t.AssigneeActorID
	}
	fmt.Printf("  owner %s  assignee %s  created by %s\n", t.OwnerActorID, a, t.CreatedByActorID)
	for _, d := range t.Dependencies {
		fmt.Printf("  depends on %s (%s)\n", d.TaskID, d.State)
	}
	if t.UnreadableDependenciesPending {
		fmt.Println("  waiting on prerequisites you cannot read")
	}
	if c := t.CurrentClaim; c != nil {
		fmt.Printf("  current claim %s by %s, execution %s, expires %s\n", c.ID, c.ActorID, c.ExecutionID, c.ExpiresAt.Format(time.RFC3339Nano))
	}
	if c := t.LastClaim; c != nil {
		fmt.Printf("  last claim %s by %s, %s\n", c.ID, c.ActorID, c.Status)
	}
	if b := t.Block; b != nil {
		fmt.Printf("  blocked (%s) by %s: %s\n", b.Cause, b.ByActorID, b.Reason)
	}
	if o := t.Outcome; o != nil {
		b, _ := json.Marshal(o)
		fmt.Printf("  outcome %s\n", b)
	}
	if t.RootRedacted {
		fmt.Println("  root redacted")
	}
	return nil
}

// printClaim prints a claim response. The proof goes to proofFile if set,
// else to stdout (human) or in the JSON document (--json).
func printClaim(cr *yak.ClaimResponse, proofFile string) error {
	proof := cr.ProofValue()
	if proofFile != "" && proof != "" {
		if err := writeSecretFile(proofFile, proof); err != nil {
			return err
		}
		cp := *cr
		cp.Proof = nil
		cr = &cp
	}
	if gf.json {
		return printJSON(cr)
	}
	c := cr.Claim
	fmt.Fprintf(os.Stderr, "claimed task %s: claim %s, execution %s, epoch %d, lease %ds, expires %s, brief revision %d\n",
		c.TaskID, c.ID, c.ExecutionID, c.Epoch, c.LeaseSeconds, c.ExpiresAt.Format(time.RFC3339Nano), c.BriefRevision)
	switch {
	case cr.ProofUnavailable:
		fmt.Fprintln(os.Stderr, "proof unavailable (replay); you cannot act as the claimant; the claim lapses at expiry")
	case proofFile != "":
		fmt.Fprintf(os.Stderr, "proof written to %s\n", proofFile)
	default:
		fmt.Println(proof)
	}
	return nil
}

func claimErr(cr *yak.ClaimResponse) error {
	if cr.ProofUnavailable {
		return &exitErr{exitClaimLost, errors.New("claim replayed without a proof (proof_unavailable)")}
	}
	return nil
}

// ---- commands ----

func taskCmd() *cobra.Command {
	task := &cobra.Command{Use: "task", Short: "Create, claim, and settle tasks (profile yak.p2.tasks)"}

	create := boardCmd("create CHANNEL", "Create a task with a new thread (text from --text or stdin)", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string, c *yak.Client, b string) error {
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
		if len(lm) == 0 {
			lm = nil
		}
		req := yak.CreateTaskRequest{Thread: &yak.NewThread{Channel: args[0], Title: title, Text: text, Labels: lm}}
		if err := fillAssignDeps(cmd, c, &req); err != nil {
			return err
		}
		t, err := c.Tasks(b).Create(cmd.Context(), req, idemKey(cmd))
		if err != nil {
			return err
		}
		return printTask(t)
	})
	create.Flags().String("title", "", "thread and task title")
	create.Flags().String("text", "", "brief / root message text (- or omit to read stdin)")
	create.Flags().StringArray("set-label", nil, "root label key=value (repeatable)")
	assignDepFlags(create)
	addIdem(create)

	convert := boardCmd("convert THREAD", "Turn an existing thread into a task", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string, c *yak.Client, b string) error {
		req := yak.CreateTaskRequest{ThreadID: args[0]}
		if err := fillAssignDeps(cmd, c, &req); err != nil {
			return err
		}
		t, err := c.Tasks(b).Create(cmd.Context(), req, idemKey(cmd))
		if err != nil {
			return err
		}
		return printTask(t)
	})
	assignDepFlags(convert)
	addIdem(convert)

	show := boardCmd("show ID", "Show the effective task view", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string, c *yak.Client, b string) error {
		t, err := c.Tasks(b).Get(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if err := printTaskDetail(t); err != nil {
			return err
		}
		return t.CheckState()
	})

	list := boardCmd("list", "List tasks in creation order", cobra.NoArgs, func(cmd *cobra.Command, args []string, c *yak.Client, b string) error {
		f := yak.TaskFilter{}
		f.Channel, _ = cmd.Flags().GetString("channel")
		f.Subtree, _ = cmd.Flags().GetString("subtree")
		f.States, _ = cmd.Flags().GetStringArray("state")
		f.Labels, _ = cmd.Flags().GetStringArray("label")
		var err error
		a, _ := cmd.Flags().GetString("assignee")
		if f.Assignee, err = actorArg(cmd.Context(), c, "--assignee", a, true); err != nil {
			return err
		}
		o, _ := cmd.Flags().GetString("owner")
		if f.Owner, err = actorArg(cmd.Context(), c, "--owner", o, false); err != nil {
			return err
		}
		var bad error
		err = c.Tasks(b).ListAll(cmd.Context(), f, func(t yak.Task) error {
			if gf.json {
				_ = json.NewEncoder(os.Stdout).Encode(t)
			} else {
				fmt.Println(taskLine(&t))
			}
			if e := t.CheckState(); e != nil && bad == nil {
				bad = e
			}
			return nil
		})
		if err != nil {
			return err
		}
		return bad
	})
	list.Flags().String("channel", "", "exact channel path")
	list.Flags().String("subtree", "", "channel path and its descendants")
	list.Flags().StringArray("state", nil, "effective state (repeatable)")
	list.Flags().String("assignee", "", "me, none, or an assignee actor ID")
	list.Flags().String("owner", "", "me or an owner actor ID")
	list.Flags().StringArray("label", nil, "root label filter key or key:value (repeatable)")

	edit := boardCmd("edit ID", "Change title, owner or direct action flag (manager, --if-revision)", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string, c *yak.Client, b string) error {
		rev, err := ifRevision(cmd, true)
		if err != nil {
			return err
		}
		req := yak.PatchTaskRequest{}
		if cmd.Flags().Changed("title") {
			v, _ := cmd.Flags().GetString("title")
			req.Title = &v
		}
		if cmd.Flags().Changed("owner") {
			v, _ := cmd.Flags().GetString("owner")
			req.OwnerActorID = &v
		}
		if cmd.Flags().Changed("direct-action-required") {
			v, _ := cmd.Flags().GetBool("direct-action-required")
			req.DirectActionRequired = &v
		}
		if req.Title == nil && req.OwnerActorID == nil && req.DirectActionRequired == nil {
			return usage("pass --title, --owner or --direct-action-required")
		}
		t, err := c.Tasks(b).Patch(cmd.Context(), args[0], rev, req)
		if err != nil {
			return err
		}
		return printTask(t)
	})
	edit.Flags().Bool("direct-action-required", false, "require direct action on this task (use =false to clear)")
	edit.Flags().String("title", "", "new title")
	edit.Flags().String("owner", "", "new owner actor ID")
	addIfRevision(edit, true)

	brief := boardCmd("brief ID", "Show the current brief; --revision N, --list, or --set to update it", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string, c *yak.Client, b string) error {
		tc := c.Tasks(b)
		ctx := cmd.Context()
		if set, _ := cmd.Flags().GetBool("set"); set {
			rev, err := ifRevision(cmd, true)
			if err != nil {
				return err
			}
			text, err := textArg(cmd)
			if err != nil {
				return err
			}
			reason, _ := cmd.Flags().GetString("reason")
			br, err := tc.UpdateBrief(ctx, args[0], rev, text, reason, idemKey(cmd))
			if err != nil {
				return err
			}
			if gf.json {
				return printJSON(br)
			}
			fmt.Fprintf(os.Stderr, "brief revision %d stored; the task is blocked until a manager unblocks it\n", br.Revision)
			if br.Task != nil {
				fmt.Println(taskLine(br.Task))
			}
			return nil
		}
		if all, _ := cmd.Flags().GetBool("list"); all {
			tok := ""
			for {
				pg, err := tc.Briefs(ctx, args[0], tok, 200)
				if err != nil {
					return err
				}
				for _, br := range pg.Items {
					if gf.json {
						_ = json.NewEncoder(os.Stdout).Encode(br)
						continue
					}
					src := ""
					if br.Source != nil {
						src = br.Source.Kind + " " + br.Source.MessageID
					}
					fmt.Printf("revision %d  %s  redacted=%v  %s\n", br.Revision, br.CreatedByActorID, br.Redacted, src)
				}
				if pg.NextPageToken == nil || *pg.NextPageToken == "" {
					return nil
				}
				tok = *pg.NextPageToken
			}
		}
		rev, _ := cmd.Flags().GetInt("revision")
		if rev == 0 {
			t, err := tc.Get(ctx, args[0])
			if err != nil {
				return err
			}
			rev = t.BriefRevision
		}
		br, err := tc.Brief(ctx, args[0], rev)
		if err != nil {
			return err
		}
		if gf.json {
			return printJSON(br)
		}
		fmt.Fprintf(os.Stderr, "brief revision %d of task %s\n", br.Revision, br.TaskID)
		if br.Text == nil {
			fmt.Fprintln(os.Stderr, "(redacted)")
			return nil
		}
		fmt.Println(*br.Text)
		return nil
	})
	brief.Flags().Int("revision", 0, "brief revision to show (default: current)")
	brief.Flags().Bool("list", false, "list brief versions")
	brief.Flags().Bool("set", false, "store a new brief version (manager; blocks the task and revokes any claim)")
	brief.Flags().String("text", "", "with --set: new brief text (- or omit to read stdin)")
	brief.Flags().String("reason", "", "with --set: reason for the change")
	addIfRevision(brief, false)
	addIdem(brief)

	redact := boardCmd("redact-root ID", "Tombstone the task root and scrub every brief (cancels unfinished work)", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string, c *yak.Client, b string) error {
		rev, _ := cmd.Flags().GetInt64("if-root-revision")
		if rev <= 0 {
			return usage("--if-root-revision is required (the root message revision)")
		}
		reason, _ := cmd.Flags().GetString("reason")
		t, err := c.Tasks(b).RootRedaction(cmd.Context(), args[0], rev, reason, idemKey(cmd))
		if err != nil {
			return err
		}
		return printTask(t)
	})
	redact.Flags().Int64("if-root-revision", 0, "expected root message revision (required)")
	redact.Flags().String("reason", "", "reason")
	addIdem(redact)

	claims := boardCmd("claims ID", "Show the claim history (newest first, no proofs)", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string, c *yak.Client, b string) error {
		tok := ""
		for {
			pg, err := c.Tasks(b).Claims(cmd.Context(), args[0], tok, 200)
			if err != nil {
				return err
			}
			for _, cl := range pg.Items {
				if gf.json {
					_ = json.NewEncoder(os.Stdout).Encode(cl)
					continue
				}
				fmt.Printf("%s  epoch %-3d %-9s %s  execution %s  acquired %s  expires %s\n", cl.ID, cl.Epoch, cl.Status, cl.ActorID, cl.ExecutionID,
					cl.AcquiredAt.Format(time.RFC3339), cl.ExpiresAt.Format(time.RFC3339))
			}
			if pg.NextPageToken == nil || *pg.NextPageToken == "" {
				return nil
			}
			tok = *pg.NextPageToken
		}
	})

	claim := boardCmd("claim ID", "Claim one task; prints the proof once (or writes --proof-file)", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string, c *yak.Client, b string) error {
		lease, err := leaseSeconds(cmd)
		if err != nil {
			return err
		}
		req := yak.ClaimRequest{LeaseSeconds: lease}
		if cmd.Flags().Changed("expect-brief-revision") {
			v, _ := cmd.Flags().GetInt("expect-brief-revision")
			req.ExpectBriefRevision = &v
		}
		cr, err := c.Tasks(b).Claim(cmd.Context(), args[0], req, idemKey(cmd))
		if err != nil {
			return err
		}
		pf, _ := cmd.Flags().GetString("proof-file")
		if err := printClaim(cr, pf); err != nil {
			return err
		}
		return claimErr(cr)
	})
	addLease(claim)
	claim.Flags().Int("expect-brief-revision", 0, "claim only if the brief is still at this revision")
	claim.Flags().String("proof-file", "", "write the proof to this file (mode 0600) instead of stdout")
	addIdem(claim)

	next := boardCmd("next", "Claim the oldest eligible task; exit 7 if none before --wait ends", cobra.NoArgs, func(cmd *cobra.Command, args []string, c *yak.Client, b string) error {
		lease, err := leaseSeconds(cmd)
		if err != nil {
			return err
		}
		req, wait, err := claimNextFlags(cmd)
		if err != nil {
			return err
		}
		req.LeaseSeconds = lease
		cr, err := c.Tasks(b).ClaimNextWait(cmd.Context(), req, wait, 0, idemKey(cmd))
		if err != nil {
			return err
		}
		if !cr.Claimed() {
			if gf.json {
				_ = printJSON(cr)
			}
			return &exitErr{exitTimeout, errors.New("no work: no eligible task before the wait ended")}
		}
		pf, _ := cmd.Flags().GetString("proof-file")
		if err := printClaim(cr, pf); err != nil {
			return err
		}
		return claimErr(cr)
	})
	addClaimNextFlags(next)
	addLease(next)
	next.Flags().String("proof-file", "", "write the proof to this file (mode 0600) instead of stdout")
	addIdem(next)

	renew := boardCmd("renew ID", "Renew your claim (claimant)", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string, c *yak.Client, b string) error {
		proof, err := proofArg(cmd)
		if err != nil {
			return err
		}
		lease, err := leaseSeconds(cmd)
		if err != nil {
			return err
		}
		tc := c.Tasks(b)
		claimID, _ := cmd.Flags().GetString("claim")
		if claimID == "" {
			t, err := tc.Get(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if t.CurrentClaim == nil {
				return &exitErr{exitClaimLost, fmt.Errorf("task %s has no active claim (state %s)", t.ID, t.State)}
			}
			claimID = t.CurrentClaim.ID
		}
		cl, err := tc.Renew(cmd.Context(), args[0], claimID, proof, lease, idemKey(cmd))
		if err != nil {
			return err
		}
		if gf.json {
			return printJSON(cl)
		}
		fmt.Printf("claim %s renewed: lease %ds, expires %s\n", cl.ID, cl.LeaseSeconds, cl.ExpiresAt.Format(time.RFC3339Nano))
		return nil
	})
	renew.Flags().String("claim", "", "claim ID (default: the task's current claim)")
	renew.Flags().Duration("lease", 0, "new lease length, e.g. 120s (default: keep the claim's current lease)")
	addProofFlag(renew)
	addIdem(renew)

	release := boardCmd("release ID", "Give the task back (claimant)", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string, c *yak.Client, b string) error {
		proof, err := proofArg(cmd)
		if err != nil {
			return err
		}
		reason, _ := cmd.Flags().GetString("reason")
		t, err := c.Tasks(b).Release(cmd.Context(), args[0], proof, reason, idemKey(cmd))
		if err != nil {
			return err
		}
		return printTask(t)
	})
	release.Flags().String("reason", "", "optional reason")
	addProofFlag(release)
	addIdem(release)

	complete := boardCmd("complete ID", "Complete the task with a result (claimant)", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string, c *yak.Client, b string) error {
		proof, err := proofArg(cmd)
		if err != nil {
			return err
		}
		ev, data, err := evidenceFlags(cmd)
		if err != nil {
			return err
		}
		summary, _ := cmd.Flags().GetString("summary")
		t, err := c.Tasks(b).Complete(cmd.Context(), args[0], proof, yak.Result{Summary: summary, Evidence: ev, Data: data}, idemKey(cmd))
		if err != nil {
			return err
		}
		return printTask(t)
	})
	complete.Flags().String("summary", "", "result summary")
	addEvidenceFlags(complete)
	addProofFlag(complete)
	addIdem(complete)

	fail := boardCmd("fail ID", "Fail the task with a reason (claimant)", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string, c *yak.Client, b string) error {
		proof, err := proofArg(cmd)
		if err != nil {
			return err
		}
		reason, _ := cmd.Flags().GetString("reason")
		if reason == "" {
			return usage("--reason is required")
		}
		ev, data, err := evidenceFlags(cmd)
		if err != nil {
			return err
		}
		t, err := c.Tasks(b).Fail(cmd.Context(), args[0], proof, yak.FailRequest{Reason: reason, Evidence: ev, Data: data}, idemKey(cmd))
		if err != nil {
			return err
		}
		return printTask(t)
	})
	fail.Flags().String("reason", "", "failure reason (required)")
	addEvidenceFlags(fail)
	addProofFlag(fail)
	addIdem(fail)

	block := boardCmd("block ID", "Block the task (manager; or the claimant with --claimant)", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string, c *yak.Client, b string) error {
		reason, _ := cmd.Flags().GetString("reason")
		if reason == "" {
			return usage("--reason is required")
		}
		proof := ""
		if asClaimant, _ := cmd.Flags().GetBool("claimant"); asClaimant {
			var err error
			if proof, err = proofArg(cmd); err != nil {
				return err
			}
		}
		rev, _ := ifRevision(cmd, false)
		t, err := c.Tasks(b).Block(cmd.Context(), args[0], proof, reason, rev, idemKey(cmd))
		if err != nil {
			return err
		}
		return printTask(t)
	})
	block.Flags().String("reason", "", "reason (required)")
	block.Flags().Bool("claimant", false, "block as the current claimant (sends the proof)")
	addProofFlag(block)
	addIfRevision(block, false)
	addIdem(block)

	manager := func(use, short string, reasonRequired bool, op func(t *yak.Tasks, cmd *cobra.Command, id, reason string, rev int64) (*yak.Task, error)) *cobra.Command {
		mc := boardCmd(use, short, cobra.ExactArgs(1), func(cmd *cobra.Command, args []string, c *yak.Client, b string) error {
			reason, _ := cmd.Flags().GetString("reason")
			if reasonRequired && reason == "" {
				return usage("--reason is required")
			}
			rev, _ := ifRevision(cmd, false)
			t, err := op(c.Tasks(b), cmd, args[0], reason, rev)
			if err != nil {
				return err
			}
			return printTask(t)
		})
		h := "optional reason"
		if reasonRequired {
			h = "reason (required)"
		}
		mc.Flags().String("reason", "", h)
		addIfRevision(mc, false)
		addIdem(mc)
		return mc
	}
	unblock := manager("unblock ID", "Unblock the task (manager)", false, func(t *yak.Tasks, cmd *cobra.Command, id, reason string, rev int64) (*yak.Task, error) {
		return t.Unblock(cmd.Context(), id, reason, rev, idemKey(cmd))
	})
	cancel := manager("cancel ID", "Cancel the task (manager; revokes any claim)", true, func(t *yak.Tasks, cmd *cobra.Command, id, reason string, rev int64) (*yak.Task, error) {
		return t.Cancel(cmd.Context(), id, reason, rev, idemKey(cmd))
	})
	reopen := manager("reopen ID", "Reopen a failed or canceled task (manager)", false, func(t *yak.Tasks, cmd *cobra.Command, id, reason string, rev int64) (*yak.Task, error) {
		return t.Reopen(cmd.Context(), id, reason, rev, idemKey(cmd))
	})

	assign := boardCmd("assign ID [ACTOR]", "Set the assignee, or clear it with --none (manager, --if-revision)", cobra.RangeArgs(1, 2), func(cmd *cobra.Command, args []string, c *yak.Client, b string) error {
		rev, err := ifRevision(cmd, true)
		if err != nil {
			return err
		}
		none, _ := cmd.Flags().GetBool("none")
		var who *string
		switch {
		case none && len(args) == 1:
		case !none && len(args) == 2:
			a, err := actorArg(cmd.Context(), c, "ACTOR", args[1], false)
			if err != nil {
				return err
			}
			who = strPtr(a)
		default:
			return usage("pass exactly one of ACTOR or --none")
		}
		k, _ := cmd.Flags().GetString("idempotency-key")
		t, err := c.Tasks(b).SetAssignee(cmd.Context(), args[0], rev, who, k)
		if err != nil {
			return err
		}
		return printTask(t)
	})
	assign.Flags().Bool("none", false, "clear the assignee")
	addIfRevision(assign, true)
	addIdem(assign)

	deps := boardCmd("deps ID [TASK...]", "Replace the readable prerequisites (manager, --if-revision); --clear for none", cobra.MinimumNArgs(1), func(cmd *cobra.Command, args []string, c *yak.Client, b string) error {
		rev, err := ifRevision(cmd, true)
		if err != nil {
			return err
		}
		clear, _ := cmd.Flags().GetBool("clear")
		ids := args[1:]
		if clear == (len(ids) > 0) {
			return usage("pass prerequisite task IDs, or --clear")
		}
		k, _ := cmd.Flags().GetString("idempotency-key")
		t, err := c.Tasks(b).SetDependencies(cmd.Context(), args[0], rev, ids, k)
		if err != nil {
			return err
		}
		return printTask(t)
	})
	deps.Flags().Bool("clear", false, "remove every readable prerequisite")
	addIfRevision(deps, true)
	addIdem(deps)

	task.AddCommand(create, convert, show, list, edit, brief, redact, claims, claim, next, renew, release,
		complete, fail, block, unblock, cancel, reopen, assign, deps, batchCmd())
	return task
}

func assignDepFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("direct-action-required", false, "mark this task as requiring direct action")
	cmd.Flags().String("assignee", "", "me or an assignee actor ID (the only eligible claimant)")
	cmd.Flags().StringArray("depends-on", nil, "prerequisite task ID (repeatable or comma-separated)")
}

func fillAssignDeps(cmd *cobra.Command, c *yak.Client, req *yak.CreateTaskRequest) error {
	req.DirectActionRequired, _ = cmd.Flags().GetBool("direct-action-required")
	a, _ := cmd.Flags().GetString("assignee")
	a, err := actorArg(cmd.Context(), c, "--assignee", a, false)
	if err != nil {
		return err
	}
	if a != "" {
		req.AssigneeActorID = &a
	}
	deps, _ := cmd.Flags().GetStringArray("depends-on")
	for _, d := range deps {
		req.DependsOn = append(req.DependsOn, splitList(d)...)
	}
	return nil
}

func addClaimNextFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String("channel", "", "only tasks in this exact channel")
	f.String("subtree", "", "only tasks in this channel and its descendants")
	f.StringArray("label", nil, "root label key or key:value (repeatable; values of one key are ORed)")
	f.String("assignee", "eligible", "eligible | me | unassigned")
	f.Duration("wait", 0, "wait up to this long for work (repeats claim-next; each request waits at most 30s)")
}

func claimNextFlags(cmd *cobra.Command) (yak.ClaimNextRequest, time.Duration, error) {
	f := cmd.Flags()
	var req yak.ClaimNextRequest
	req.Channel, _ = f.GetString("channel")
	req.Subtree, _ = f.GetString("subtree")
	labels, _ := f.GetStringArray("label")
	req.Labels = labelFilter(labels)
	req.Assignee, _ = f.GetString("assignee")
	switch req.Assignee {
	case "eligible", "me", "unassigned":
	default:
		return req, 0, usage("--assignee must be eligible, me, or unassigned")
	}
	wait, _ := f.GetDuration("wait")
	if wait < 0 {
		return req, 0, usage("--wait must not be negative")
	}
	return req, wait, nil
}

// ---- batch / plan ----

type planFile struct {
	PlanID string          `json:"plan_id,omitempty"`
	Tasks  []yak.BatchItem `json:"tasks"`
}

func batchCmd() *cobra.Command {
	bc := boardCmd("batch FILE.json", "Create tasks from a plan file: ≤ batch size is one atomic batch; larger plans are resumable topological batches", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string, c *yak.Client, b string) error {
		raw, err := os.ReadFile(args[0])
		if err != nil {
			return err
		}
		var pf planFile
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&pf); err != nil {
			return usage("%s: %v", args[0], err)
		}
		if len(pf.Tasks) == 0 {
			return usage("%s: no tasks", args[0])
		}
		planID, _ := cmd.Flags().GetString("plan-id")
		if planID == "" {
			planID = pf.PlanID
		}
		if planID == "" {
			planID = yak.PlanID(pf.Tasks)
		}
		size, _ := cmd.Flags().GetInt("batch-size")
		if size < 1 {
			return usage("--batch-size must be at least 1")
		}
		progPath, _ := cmd.Flags().GetString("progress-file")
		if progPath == "" {
			progPath = args[0] + ".progress.json"
		}
		var prog *yak.PlanProgress
		if pb, err := os.ReadFile(progPath); err == nil {
			prog = &yak.PlanProgress{}
			if err := json.Unmarshal(pb, prog); err != nil {
				return fmt.Errorf("%s: %w", progPath, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		batches, err := yak.PlanBatches(pf.Tasks, size)
		if err != nil {
			return usage("%v", err)
		}
		if len(batches) > 1 {
			fmt.Fprintf(os.Stderr, "plan %s: %d tasks in %d batches; each batch is atomic, the plan is not; progress in %s\n", planID, len(pf.Tasks), len(batches), progPath)
		}
		prog, err = c.Tasks(b).SubmitPlan(cmd.Context(), pf.Tasks, yak.PlanOptions{
			PlanID: planID, BatchSize: size, Progress: prog,
			Save: func(p *yak.PlanProgress) error {
				bb, _ := json.MarshalIndent(p, "", "  ")
				tmp := progPath + ".tmp"
				if err := os.WriteFile(tmp, bb, 0o600); err != nil {
					return err
				}
				return os.Rename(tmp, progPath)
			},
			Log: func(format string, a ...any) {
				fmt.Fprintf(os.Stderr, "plan %s: "+format+"\n", append([]any{planID}, a...)...)
			},
		})
		if err != nil {
			if prog != nil && len(prog.BatchesDone) > 0 {
				fmt.Fprintf(os.Stderr, "plan %s: %d of %d batches done; rerun the same command to resume\n", planID, len(prog.BatchesDone), len(batches))
			}
			return err
		}
		type pair struct {
			Ref    string `json:"ref"`
			TaskID string `json:"task_id"`
		}
		var pairs []pair
		for _, it := range pf.Tasks {
			pairs = append(pairs, pair{it.Ref, prog.Tasks[it.Ref]})
		}
		if gf.json {
			return printJSON(map[string]any{"plan_id": planID, "atomic": len(batches) == 1, "batches": len(batches), "progress_file": progPath, "tasks": pairs})
		}
		for _, p := range pairs {
			fmt.Printf("%s  %s\n", p.Ref, p.TaskID)
		}
		return nil
	})
	bc.Flags().String("plan-id", "", "stable plan ID for keys <plan-id>:<n> and plan.id labels (default: plan_id in the file, else a hash of the tasks)")
	bc.Flags().String("progress-file", "", "resumable progress file (default FILE.json.progress.json)")
	bc.Flags().Int("batch-size", 100, "items per atomic batch (server max_batch_tasks)")
	return bc
}

// ---- yak run ----

func runCmd() *cobra.Command {
	rc := &cobra.Command{
		Use:   "run [TASK_ID] [flags] -- COMMAND [ARGS...]",
		Short: "Claim a task, run COMMAND while renewing the lease, and settle from its exit status",
		Long: `Claims TASK_ID, or the oldest eligible task (claim-next) when no ID is given,
then runs COMMAND with YAK_TASK_ID, YAK_CLAIM_ID, YAK_EXECUTION_ID,
YAK_CLAIM_EPOCH, YAK_RUN_ID, YAK_BOARD, YAK_BRIEF_REVISION, YAK_BRIEF_FILE,
and YAK_RESULT_FILE in its environment. The claim proof is never passed.
Exit 0 completes the task; other exits fail it. COMMAND may write
{"summary", "reason", "evidence": [{kind, ref, note}], "data": {...}} to
$YAK_RESULT_FILE. Evidence kinds are message, commit, url, artifact, or
other; the runner records any other kind in the file as "other" with the
original kind at the start of the note. If the server still rejects the
result (400/413), or the result file exists but is unusable (unreadable,
over 1 MiB, not one JSON object, unknown fields, non-object data), the
runner never settles without it: it releases the claim with a reason that
names the problem and exits 13 (also if the release fails).
Exit codes: 0 completed, 7 no work, 9 claim lost,
10 renewal not confirmed (command stopped), 11 command failed (task failed),
12 interrupted (claim released), 13 result rejected (claim released).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			dash := cmd.ArgsLenAtDash()
			if dash < 0 || dash >= len(args) {
				return usage("give the command after --, e.g. yak run -- make test")
			}
			pre, command := args[:dash], args[dash:]
			if len(pre) > 1 {
				return usage("at most one TASK_ID before --")
			}
			// Validate evidence before anything is claimed: a bad kind
			// must never reach settlement.
			evs, _ := cmd.Flags().GetStringArray("evidence")
			evidence, err := parseEvidence(evs)
			if err != nil {
				return err
			}
			commits, _ := cmd.Flags().GetStringArray("commit")
			cev, err := commitEvidence(commits)
			if err != nil {
				return err
			}
			evidence = append(evidence, cev...)
			c, p, err := client()
			if err != nil {
				return err
			}
			b, err := boardID(p)
			if err != nil {
				return err
			}
			lease, err := leaseSeconds(cmd)
			if err != nil {
				return err
			}
			opt := runOptions{Board: b, Command: command, Stdout: os.Stdout, Stderr: os.Stderr, Log: os.Stderr, LeaseSeconds: lease}
			if gf.json {
				opt.Stdout = os.Stderr // keep stdout for the report
			}
			if len(pre) == 1 {
				opt.TaskID = pre[0]
				for _, fl := range []string{"channel", "subtree", "label", "assignee", "wait"} {
					if cmd.Flags().Changed(fl) {
						return usage("--%s selects work for claim-next; do not combine it with TASK_ID", fl)
					}
				}
			} else {
				if opt.Next, opt.Wait, err = claimNextFlags(cmd); err != nil {
					return err
				}
			}
			if cmd.Flags().Changed("expect-brief-revision") {
				v, _ := cmd.Flags().GetInt("expect-brief-revision")
				opt.ExpectBriefRevision = &v
			}
			opt.Grace, _ = cmd.Flags().GetDuration("grace")
			opt.Summary, _ = cmd.Flags().GetString("summary")
			opt.ReleaseReason, _ = cmd.Flags().GetString("release-reason")
			opt.Dir, _ = cmd.Flags().GetString("dir")
			opt.Evidence = evidence
			sigs := make(chan os.Signal, 2)
			signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
			defer signal.Stop(sigs)
			opt.Signals = sigs

			rep, runErr := newRunner(c, opt).run(cmd.Context())
			if gf.json {
				_ = printJSON(rep)
			} else if rep != nil && rep.Outcome != "" {
				st := ""
				if rep.ExitStatus != nil {
					st = " exit_status=" + strconv.Itoa(*rep.ExitStatus)
				}
				fmt.Fprintf(os.Stderr, "yak run: outcome=%s task=%s execution=%s renewals=%d%s\n", rep.Outcome, rep.TaskID, rep.ExecutionID, rep.Renewals, st)
			}
			return runErr
		},
	}
	f := rc.Flags()
	addClaimNextFlags(rc)
	addLease(rc)
	f.Int("expect-brief-revision", 0, "with TASK_ID: claim only if the brief is still at this revision")
	f.Duration("grace", 10*time.Second, "time between SIGTERM and SIGKILL when stopping COMMAND")
	f.String("summary", "", "result summary on completion (COMMAND's result file wins)")
	f.StringArray("evidence", nil, "evidence kind:ref[:note] to record on settlement (repeatable)")
	f.StringArray("commit", nil, "commit ID to record as evidence (repeatable)")
	f.String("release-reason", "", "reason recorded when an interrupt releases the claim")
	f.String("dir", "", "working directory for COMMAND")
	return rc
}
