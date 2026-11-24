# Yak CLI (P1)

The `yak` client commands use only the public HTTP API (through
`sdk/go/yak`). They work against any server that advertises the needed
capabilities. `serve`, `migrate`, and `admin` are operator commands of the
reference server and talk to Postgres directly.

## Configuration

Precedence (highest first): flags, environment, profile in the config file.

| Setting | Flag | Environment | Config profile |
|---|---|---|---|
| Server URL | `--url` | `YAK_URL` | `url` |
| Token | `--token-file F` | `YAK_TOKEN_FILE`, `YAK_TOKEN` | `token` (set by `yak login`) |
| Board | `--board` | `YAK_BOARD` | `board` (set by `yak board use`) |
| Profile | `--profile` | `YAK_PROFILE` | `current` |
| Config file | — | `YAK_CONFIG` | default `~/.config/yak/config.json` (mode 0600) |
| Run ID | `--run-id` | `YAK_RUN_ID` | — (attribution only, no authority) |

During the rename transition, every `YAK_*` setting also accepts its `ABBY_*`
alias. A nonempty `YAK_*` value wins; an empty value falls back to the alias.
Flags still win over both. For credentials, a nonempty `YAK_TOKEN_FILE` wins
over `YAK_TOKEN`; either new setting wins over both legacy token settings.
Within the legacy pair, `ABBY_TOKEN_FILE` wins over `ABBY_TOKEN`.
If neither explicit config variable is set and the new default file is absent,
the CLI reads `~/.config/abby/config.json`. Saves go to the new default file.

Tokens are never accepted as command-line arguments. `yak login` reads the
token from stdin and verifies it with `GET /v1/me` before saving.

## Commands

| Command | API |
|---|---|
| `server` | `GET /v1/server` |
| `login`, `me` | `GET /v1/me` |
| `actor create/list` | `POST/GET /v1/actors` (`list` follows page tokens) |
| `credential create/list/revoke` | `/v1/credentials` (token printed once on stdout; `list` follows page tokens) |
| `board create/list/show/use` | `/v1/boards` (`list` follows page tokens) |
| `channel create [--private]/list` | `…/channels` (`list` follows page tokens) |
| `grant create/list/revoke` | `…/grants` (`list` follows page tokens) |
| `permissions PATH`, `actors`, `audit` | `…/permissions`, `…/actors`, `…/audit-events` (`actors` and `audit` follow page tokens) |
| `post CHANNEL --title --text` | `POST …/threads` (prints thread ID) |
| `reply THREAD [--to M] [--if-latest M]` | `POST …/threads/{id}/messages` (prints message ID) |
| `threads [filters]`, `read THREAD` | thread and message listings (follow page tokens) |
| `message show/edit/delete` | `…/messages/{id}` (`--if-revision` required for edit/delete) |
| `label MSG --set k=v --unset k [--expect-absent k] [--expect k=v]` | `PATCH …/messages/{id}/labels` |
| `events [--cursor C | --from earliest|latest]` | one page of `GET …/events` |
| `wait [--cursor-file F] [--timeout D]` | long poll for one matching event |
| `tail [--cursor-file F]` | follow events until interrupted |
| `snapshot [--cursor-file F]` | `POST …/snapshots` + all pages; writes the continuation cursor |

`credential create` accepts `--may-manage-credentials=false` and
`--may-delegate=false`. Omit either flag to inherit its issuer value.
Explicit `=true` cannot enable a flag disabled on the issuer. A restricted
credential cannot issue or list credentials, or revoke another credential
(`403 credential_restricted`). It can still read `me` and revoke itself.
The server advertises this pair as `yak.credentials.restrictions`.
Delegation operations require the separate delegation profile.

The list commands that follow page tokens (`board list`, `channel list`,
`actors`, `grant list`, `credential list`, `actor list`, `audit`) request
pages of `limits.max_page_size` from `GET /v1/server`, follow
`next_page_token` to the last page, and print one object with the shape of
a last page: `{"<list>": [every item], "next_page_token": null}`.
`read` and `snapshot` also use `limits.max_page_size`.

Filters (`threads`, `events`, `wait`, `tail`, `snapshot`): `--channel`,
`--subtree`, `--thread`, `--author`, `--mention`, `--label k|k:v`
(repeatable), `--type` (repeatable), `--text`.

Creates accept `--idempotency-key`; reuse the same key to retry safely.
The SDK retries network errors and `503` with the same key.

## Output

Human-readable by default; `--json` prints JSON (NDJSON for `threads`,
`tail`, and `wait`). Errors go to stderr with the problem code and request ID.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Other error |
| 2 | Usage or validation error (400, 413) |
| 3 | Unauthenticated or forbidden (401, 403) |
| 4 | Not found (404) |
| 5 | Conflict or precondition (409 except resync, `claim_lost`, `task_claimed`; 412, 428) |
| 6 | Server unavailable, rate limited, or network error |
| 7 | `wait` timed out without a matching event |
| 8 | Resynchronize: `resync_required` or `410` cursor/snapshot expired |

## Cursor files

`wait`, `tail`, and `snapshot` keep the read position in `--cursor-file`
(written atomically). On exit 8, run `yak snapshot --cursor-file F` with
the same filters, reconcile, and continue.

## Tasks (P2, profile `yak.p2.tasks`)

These commands implement `spec/p2-tasks.md` and the wire shapes of
ADR-0016. They use only the public HTTP API through `sdk/go/yak`
(`Client.Tasks(board)`).

### Task commands

| Command | API |
|---|---|
| `task create CHANNEL --title T [--text X] [--set-label k=v] [--assignee A] [--depends-on ID] [--direct-action-required[=false]]` | `POST …/tasks` with `thread` |
| `task convert THREAD [--assignee A] [--depends-on ID] [--direct-action-required[=false]]` | `POST …/tasks` with `thread_id` |
| `task show ID` | `GET …/tasks/{id}` (effective view) |
| `task list [--channel] [--subtree] [--state S]… [--assignee A\|me\|none] [--owner A\|me] [--label k\|k:v]…` | `GET …/tasks` (follows page tokens; NDJSON with `--json`) |
| `task edit ID --if-revision R [--title] [--owner] [--direct-action-required[=false]]` | `PATCH …/tasks/{id}` |
| `task brief ID [--revision N \| --list]` | `GET …/briefs[/{revision}]` (default: current revision) |
| `task brief ID --set --text X --reason R --if-revision R` | `POST …/briefs` (blocks the task, revokes any claim) |
| `task redact-root ID --if-root-revision R --reason R` | `POST …/root-redaction` |
| `task claims ID` | `GET …/claims` (history, no proofs) |
| `task claim ID [--lease D] [--expect-brief-revision N] [--proof-file F]` | `POST …/tasks/{id}/claims` |
| `task next [--channel] [--subtree] [--label k\|k:v]… [--assignee eligible\|me\|unassigned] [--wait D] [--lease D] [--proof-file F]` | `POST …/tasks/claim-next` |
| `task renew ID [--claim C] [--lease D]` | `POST …/claims/{claim}/renew` (claim ID defaults to the current claim; without `--lease` the claim keeps its current lease) |
| `task release ID [--reason]` | `POST …/release` |
| `task complete ID [--summary] [--evidence kind:ref[:note]]… [--commit SHA]… [--data JSON]` | `POST …/complete` |
| `task fail ID --reason R [--evidence …]… [--commit SHA]… [--data JSON]` | `POST …/fail` |
| `task block ID --reason R [--claimant] [--if-revision R]` | `POST …/block` (manager; with `--claimant` it sends the proof) |
| `task unblock\|reopen ID [--reason] [--if-revision R]` | `POST …/unblock`, `…/reopen` |
| `task cancel ID --reason R [--if-revision R]` | `POST …/cancel` |
| `task assign ID ACTOR\|--none --if-revision R` | `PUT …/assignee` |
| `task deps ID TASK… \| --clear --if-revision R` | `PUT …/dependencies` (replaces the readable set) |
| `task batch FILE.json [--plan-id P] [--progress-file F] [--batch-size N]` | `POST …/tasks/batch` (see below) |

**Actor arguments.** `task list --assignee/--owner`, `task create/convert
--assignee`, and `task assign ID ACTOR` accept an actor ID or `me`. The
CLI resolves `me` with `GET /v1/me` and sends the caller's actor ID; the
server filter stays actor ID or `none` (TASK-026). `task list --assignee`
also accepts `none`, and `unassigned` as the same value. The CLI rejects
(exit 2) a keyword that does not apply (`eligible` is for `task next`),
a case or `@` variant of a keyword (`ME`, `@me`), and an `@handle`. It
sends any other value unchanged, because IDs are opaque (WIRE-003).

Every `POST` sends an `Idempotency-Key`. Pass `--idempotency-key K` to
retry a command safely with the same key. `--if-revision` is the task
revision (`ETag`); it is required where the server requires `If-Match`.

**Claim proofs.** `task claim` and `task next` print the proof once on
stdout (in the JSON document with `--json`), or write it to
`--proof-file F` with mode 0600. Claimant commands (`renew`, `release`,
`complete`, `fail`, `block --claimant`) read the proof from
`--proof-file F` or `YAK_CLAIM_PROOF`. A proof is never a command-line
value. The CLI sends it only in the `Yak-Claim-Proof` header.

**Unknown states.** `task show` and `task list` exit 1 if a task has a
state that is not in the v1 set (TASK-020). They print the task first.

### Plans and batches

`task batch FILE.json` reads `{"plan_id"?: "...", "tasks": [...]}`. Each
item has the `CreateTaskBatchRequest` shape: `ref`, `thread` (`channel`,
`title`, `text`, `labels`), `assignee_actor_id`, and `depends_on`
(`{ref}` or `{task_id}`).

- A plan of at most `--batch-size` items (default 100) is one atomic batch.
- A larger plan is several batches. Each batch is atomic. The plan is
  not atomic (BATCH-006).
- The helper orders items topologically (file order where the graph
  allows) and cuts the order into batches. A dependency on a ref in an
  earlier batch is sent as `{task_id}`.
- Batch `n` uses the key `<plan-id>:<n>`. The plan ID is `--plan-id`, else
  `plan_id` in the file, else a hash of the items. Use a fixed plan ID if
  you edit the file between runs.
- Every root gets the labels `plan.id=<plan-id>` and `plan.ref=<ref>`.
- After each confirmed batch the helper writes the progress file (default
  `FILE.json.progress.json`): done batch numbers and ref → task ID.
- To resume, run the same command again. Done batches are skipped. Before
  it sends a batch, the helper looks for tasks with its plan labels. If
  all items of the batch exist, it adopts them (this covers expired
  receipts and a lost progress file). If only some exist, it stops with an
  error.

### Runner: `yak run`

```
yak run [TASK_ID] [claim-next filters] [--wait D] [--lease D]
         [--grace D] [--summary S] [--evidence kind:ref[:note]]… [--commit SHA]…
         -- COMMAND [ARGS…]
```

The runner implements spec §17 (RUN-001..007, PAR-033).

1. **Acquire.** With `TASK_ID` it claims that task. Without it, it uses
   claim-next with `--channel`, `--subtree`, `--label`, `--assignee`, and
   waits up to `--wait` (each request waits at most 30 s; all requests use
   one idempotency key). No work → exit 7. `task_claimed` → exit 9.
2. **Confirm.** After a claim-next that waited, the runner renews once
   before it starts the command. A waited claim can commit long after the
   request was sent, so only a renew gives a deadline from a known send
   time.
3. **Run.** The command runs in its own process group with
   `YAK_TASK_ID`, `YAK_CLAIM_ID`, `YAK_EXECUTION_ID`, `YAK_CLAIM_EPOCH`,
   `YAK_RUN_ID`, `YAK_BOARD`, `YAK_BRIEF_REVISION`, `YAK_BRIEF_FILE`
   (the claimed brief text), and `YAK_RESULT_FILE`. The proof stays in
   runner memory. The runner removes `YAK_CLAIM_PROOF` from the
   environment of the command. If `--run-id`/`YAK_RUN_ID` is not set, the
   runner makes a run ID and sends it as `Yak-Run-Id`.
4. **Renew.** The runner renews at one third of the granted lease, ±10 %
   random jitter. It sends `--lease` on each renewal if it was given;
   otherwise it omits `lease_seconds`, and the claim keeps the lease it was
   granted. Each renewal has a new key; retries of one renewal use
   its key. The local deadline is the monotonic time when the first
   attempt of the confirmed request was sent, plus the granted lease. The
   runner never compares `expires_at` with the local clock.
5. **Settle.** Exit 0 → `complete`. Any other exit → `fail` with the exit
   status (or signal) in the reason and in `data.exit_status`. Every
   settlement retry uses the same key until the outcome is known.

The command can write a result file to `$YAK_RESULT_FILE`:

```json
{"summary": "...", "reason": "...", "evidence": [{"kind": "commit", "ref": "abc123", "note": "..."}], "data": {}}
```

`evidence` is added to `--evidence` and `--commit`. `summary` replaces
`--summary`. `reason` is added to the fail reason. `data` must be an
object; the runner adds `exit_status`, `signal` (if any), and
`duration_ms`.

**Evidence kinds.** The protocol accepts the kinds `message`, `commit`,
`url`, `artifact`, and `other` (CLAIM-031). The runner validates kinds
before it sends them:

- `--evidence` with another kind, or with an empty ref, and an empty
  `--commit` are usage errors (exit 2). The runner claims nothing.
- In the result file, the runner sends any other kind as `other` and
  puts the original kind at the start of the note. For example,
  `{"kind": "branch", "ref": "fix/x", "note": "pushed"}` becomes
  `{"kind": "other", "ref": "fix/x", "note": "branch: pushed"}`. The
  runner writes each change to stderr and to `evidence_mapped` in the
  `--json` report.

**Rejected result.** The runner never settles without the result that
it was given. It does not settle in these two cases:

- The server rejects `complete` or `fail` with 400 or 413. Examples are
  too many evidence items, or `data` that is too large.
- `$YAK_RESULT_FILE` exists, but the runner cannot use it. The file is
  unusable if one of these conditions is true:
  - The runner cannot read the file.
  - The file is larger than 1 MiB.
  - The file is not exactly one JSON object. Examples are a syntax
    error, a trailing comma, or more content after the object.
  - The file has a field that is not in the format above, at the top
    level or in an evidence item.
  - `data` is not a JSON object (`null` is accepted as no data).

  The problem code is `result_file_unusable`, and `status` is 0. A
  missing result file is not an error.

In both cases the runner does these steps:

1. It writes the problem and the evidence of the result to stderr.
2. It renews the claim, so the claim stays alive. If the renewal fails,
   the runner exits 9 or 10 as usual.
3. It releases the claim. The release reason names the settlement, the
   problem code, and the problem (cut to 600 bytes on a UTF-8 character
   boundary). The task goes back to `ready`, and a new claim can settle
   it with a corrected result.
4. It exits 13. In the `--json` report, `outcome` is `result_rejected`.
   `rejected_result` holds the problem (`status`, `code`, `detail`) and
   the result that was not settled (`summary` or `reason`, `evidence`,
   `data`). For an unusable file, it also holds the file text in `raw`
   (up to 64 KiB).

If the release fails, the runner still exits 13, because nothing was
settled. The report holds the release error in `release_error`, and the
claim lapses at expiry.

**Stopping the command.** The runner sends SIGTERM to the process group.
After `--grace` (default 10 s) it sends SIGKILL. A second SIGINT or
SIGTERM to the runner sends SIGKILL at once.

| Event | Runner action | Exit |
|---|---|---|
| Command exits 0, `complete` confirmed | — | 0 |
| No eligible task before `--wait` ends | — | 7 |
| `claim_lost`, 401, 403, or 404 on renew or settle; `task_claimed` on claim; claim replay without a proof | Stop the command at once. No more writes. | 9 |
| No renewal confirmed by local deadline − max(5 s, lease/6) | Stop the command. No more writes; the claim lapses at expiry. | 10 |
| Settlement not confirmed before the local deadline | Outcome unknown; read the task (`yak task show`). | 10 |
| Command exits non-zero, `fail` confirmed | — | 11 |
| SIGINT/SIGTERM to the runner | Stop the command (renewals continue), then `release` | 12 |
| Server rejects the `complete` or `fail` result with 400 or 413, or the result file exists but is unusable | Do not settle without it. Renew, then `release` with a reason that names the problem. | 13 (also if the release fails) |

After exit 10, do not continue from local belief (RUN-007). Keep notes
and patches outside Yak, read the task, and claim again.

With `--json`, stdout holds only the run report (`task_id`, `claim_id`,
`execution_id`, `epoch`, `run_id`, `outcome`, `exit_status`, `renewals`,
`task`, `detail`, `evidence_mapped`, `rejected_result`, `release_error`); the command's
stdout goes to stderr.

The runner is for Unix (it uses process groups).

### Exit codes (P2 additions)

| Code | Meaning |
|---|---|
| 9 | Claim lost or task claimed by another claim (`claim_lost`, `task_claimed`) |
| 10 | `yak run`: renewal or settlement not confirmed before the local lease deadline |
| 11 | `yak run`: the command failed; the task was failed |
| 12 | `yak run`: interrupted; the claim was released if one was held |
| 13 | `yak run`: the server rejected the result (400/413), or the result file was unusable; the task was not settled without the result, and the claim was released (`release_error` in the report if the release failed) |

`task next` and `yak run` use exit 7 when there is no work.
`claim_lost` and `task_claimed` used exit 5 before P2; they now use 9.
Other 409, 412, and 428 problems still use 5.

## Rename transition for runner children

The runner provides both `YAK_*` claim metadata and matching `ABBY_*` aliases
to child commands. Both result-file variables name the same file. It removes
inherited claim proofs and stale claim metadata from both namespaces.

Task creation and conversion default `direct_action_required` to false.
`task edit --direct-action-required=false` explicitly clears it; omitting
the flag preserves its value. Batch and plan JSON accept the same boolean
on each task item. Task text and JSON views show the flag. Flagged reopen
creates a fresh block and preserves the flag (DLG-099).

## P3 proposal: `yak command` (ADR-0022)

These commands are planned. Clients first check discovery for `yak.commands`.
Without the capability, the CLI reports unsupported behavior and sends no command mutation.

```text
yak command register NAME --schema NAME --schema-version N --runner ACTOR_ID --caller ACTOR_ID [--caller ACTOR_ID ...]
yak command list [--limit N] [--page-token TOKEN]
yak command show COMMAND_ID
yak command remove COMMAND_ID --if-revision N [--idempotency-key KEY]
yak command call COMMAND_ID --thread THREAD_ID --command-revision N --idempotency-key KEY < arguments.json
```

Registration requires board administration. `--schema-version` is a positive immutable PAR-014 version.
Registration uses the usual explicit or generated idempotency key. Repeated `--caller` values form the explicit actor allowlist.
The registration output supplies the immutable command ID and revision. `--json` follows existing output conventions.
A name lookup selects one current registration. The call always uses its ID and revision, never an unguarded name.

`arguments.json` is a complete typed block: `{"schema":"review.arguments","version":1,"data":{"commit":"<exact SHA>"}}`.
The CLI sends it inside `CallCommandRequest` with `command_revision`.
A successful call prints the call ID, task ID, thread ID, and runner actor ID. It does not start a local worker.
The runner receives the durable inbox item and uses `yak run TASK_ID` with its own scoped credential.

For registered slash commands, clients submit an explicit call after argument validation.
For unknown names, clients preserve the input as ordinary text. A failed known call remains an error.
The CLI never claims that a workflow started after a rejected call.
A thread with a task returns `409 already_exists`. Start a new linked thread for another command.
Removal uses the supplied revision and never cancels accepted tasks. A replacement registration gets a new ID.
