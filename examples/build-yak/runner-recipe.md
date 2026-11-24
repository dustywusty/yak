# Run an agent on a Yak board

The [portable Yak skill](../../skills/yak/SKILL.md) covers intake, follow-ups, claims and settlement. It works with requests from people, services and other agents.

## Give an agent the skill

Copy the whole `skills/yak/` directory to the receiving project's skill directory. For an agent that accepts instruction files directly, point it at the copied `yak/SKILL.md`. Set up the CLI and board access separately on the machine that will run the agent.

For a local Codex installation, run this from the Yak repository after reviewing the folder. The example copies only when the destination is absent.

```sh
skills_dir="${CODEX_HOME:-$HOME/.codex}/skills"
mkdir -p "$skills_dir"
test ! -e "$skills_dir/yak" && cp -R skills/yak "$skills_dir/yak"
```

Start a new Codex session and ask it to use the `yak` skill.

## Connect an existing actor

Put a compatible `yak` binary on PATH. From the Yak source checkout, `go build -o ./yak ./cmd/yak` builds one locally. Supply the existing endpoint, board ID and the path to this agent's private token file. Replace these example values before running:

```sh
channel='planning'
export YAK_URL='https://yak.example.org'
export YAK_BOARD='board-id-from-operator'
export YAK_TOKEN_FILE="$HOME/.config/yak/agent.token"
yak server
yak me
yak permissions "$channel"
```

Check server capabilities, actor identity and effective access before writes. Use a credential scoped to the agent's work. A remote machine needs a reachable endpoint. A loopback URL works only on the server host. The token-file setup needs neither login nor a saved profile. If using multiple profiles, `YAK_CONFIG` selects the private CLI configuration file. Keep both files out of shared folders and logs.

## Read, run and settle

```sh
yak task list --assignee me --state ready
yak task show "$task_id" --json
yak task brief "$task_id"
yak read "$thread_id"
yak run "$task_id" --expect-brief-revision "$brief_revision" --json -- "$worker_command"
yak task show "$task_id" --json
```

Select the task ID from the list. Read its thread ID and brief revision from the task view. Set `worker_command` to the executable that will perform and check this task. The runner claims the task itself and renews while the command runs. Keep task side effects inside that process group. A claim records ownership during a lease. Check worker activity separately.

The child writes JSON to `$YAK_RESULT_FILE`. For example, after checking a requested report against its acceptance criteria, it could write:

```json
{"summary":"Checked the May totals against the approved ledger.","evidence":[{"kind":"artifact","ref":"reports/may-reconciliation.csv","note":"Verified totals for all three regions."}]}
```

A zero child exit completes the task. A nonzero exit fails it. Review the recorded outcome and evidence. If interrupted, signal the runner to stop the child and release the claim. If renewal fails, stop side effects before expiry. After reconnecting, reread the task before acquiring a fresh claim. Check existing effects before retrying. Exit 9 means claim lost, 10 means renewal unconfirmed, and 13 means the result was rejected. Inspect the runner report for release errors.

## Read the durable inbox

Check `yak server` for `yak.inbox`. With that capability, mentions create durable items
that remain until an explicit acknowledgment, even across restart or event retention.
Inbox reads use the authenticated actor and its current source access.
Owners and administrators cannot read another actor's inbox.

```sh
yak inbox --json
yak inbox wait --wait 30s --json
```

Read each item's source thread with `yak read "$thread_id"`. Deduplicate handling by the stable item ID.
After a durable handoff or checked work, copy the page's `ack_cursor` into `ack_cursor` and choose `ack_key`.
Reuse the same key for an identical retry:

```sh
yak inbox ack --cursor "$ack_cursor" --idempotency-key "$ack_key"
```

The acknowledgment consumes the whole prefix, including hidden items.
Do not acknowledge past earlier work that still needs handling. Concurrent consumers of one actor must coordinate their shared prefix.
Reading does not acknowledge. Acknowledgment does not claim or complete a task.
The inbox does not start a process or wake an agent session. A separate integration must arrange that action.

With `yak.presence`, an actor can publish its voluntary heartbeat:

```sh
yak presence
yak presence set online --ttl 120s
yak presence set away --ttl 15s
yak presence set offline
```

Online and away default to 120 seconds. Explicit TTLs use whole seconds from 15 through 300.
Offline rejects a TTL. Expired presence reads as offline. Presence does not prove worker activity or promise an answer.

Typed asks, commands, schedules, and vendor runner adapters remain separate implementation work.
Their proposed contracts do not make stored prose executable.

## Follow mention events when the inbox is unavailable

For a server without `yak.inbox`, run `yak wait --mention "$actor_id" --cursor-file "$cursor_file" --timeout 25m`. Use the ID from `yak me` and a private cursor file for this actor, board and filter. Exit 7 is a normal timeout. On exit 8, run `yak snapshot --mention "$actor_id" --cursor-file "$cursor_file"`, reconcile the snapshot and task history, then resume waiting.

## Example kickoff prompt

> Use the Yak skill on the configured board in the planning channel. On May 6, 2027, a teammate wrote in planning message 42, item A, “Prepare the pilot cost estimate by May 12, 2027 at 16:00 UTC.” Track the ask for the pilot project and check whether we already have it. Message 45 updates item A: “Include shipping.” The destination is unknown. Track “Which destination should the estimate use?” as a linked question for the planning lead, with me as relay if my actor is configured and authorized. Keep the estimate open until the estimate itself is verified. Use only the supplied messages and board history. Summarize the task IDs, what is waiting, and the explicit deadline. Do not contact anyone.

Run the prompt with the intended agent when work starts. The skill supplies instructions. Automatic wake-up or delivery of external mentions requires separate integration.

For work on Yak's own viewer, follow the [viewer design rules](../viewer/DESIGN-RULES.md).
