# ADR-0016 P2 task wire shapes, storage, and claim-next

Status: Accepted pending owner review (2026-11-19). Normative behavior is in
`spec/p2-tasks.md`; this ADR fixes the HTTP shapes until they move into
`spec/openapi.yaml` and `spec/schemas/`, and records the reference storage
and selection design. Builds on ADR-0003, ADR-0004, ADR-0006, ADR-0008.

## 1 Headers

| Header | Direction | Use |
|---|---|---|
| `Authorization: Bearer …` | request | As P1 (AUTH-001). |
| `Idempotency-Key` | request | Required on every task `POST` (TASK-070, CLAIM-040). Clients SHOULD use ≥ 128-bit random keys, because a replay of a claim can return a proof. |
| `If-Match: "<revision>"` | request | Required on `PATCH …/tasks/{id}`, `PUT …/assignee`, `PUT …/dependencies`, `POST …/briefs` (task revision), `POST …/root-redaction` (root message revision). Optional on manager lifecycle commands. |
| `Abby-Claim-Proof: <proof>` | request | Claimant operations: renew, release, complete, fail, and block by the claimant. Never in body or query. Servers strip it from logs. |
| `Abby-Run-Id` | request | Optional; recorded on the claim and events; no authority (AUTH-002). |
| `ETag: "<revision>"` | response | Task revision on task responses. |
| `Idempotent-Replayed: true` | response | As P1 (IDEM-002). |

Block chooses its mode by the header: with `Abby-Claim-Proof` it is a
claimant block (proof must verify); without it the caller must be a
manager.

## 2 Routes

Relative to `/v1/boards/{board_id}`. `M` = manager, `C` = claimant with
proof. All `POST`s need `Idempotency-Key`.

| Method | Path | Rights | Request | Success |
|---|---|---|---|---|
| GET | `/tasks` | `read` | query: `channel`, `subtree`, `state`*, `assignee` (ID or `none`), `owner`, `label`*, `page_token`, `limit` | 200 `TaskPage` |
| POST | `/tasks` | L1 or L2 | `CreateTaskRequest` | 201 `Task` |
| POST | `/tasks/batch` | L1 per item | `CreateTaskBatchRequest` | 201 `TaskBatch` |
| GET | `/tasks/{task_id}` | `read` | — | 200 `Task` + `ETag` |
| PATCH | `/tasks/{task_id}` | M, `If-Match` | `PatchTaskRequest` | 200 `Task` |
| GET | `/tasks/{task_id}/briefs` | `read` | `page_token`, `limit` | 200 `BriefPage` |
| GET | `/tasks/{task_id}/briefs/{revision}` | `read` | — | 200 `Brief` |
| POST | `/tasks/{task_id}/briefs` | M + `post`, `If-Match` | `{text, reason}` | 201 `Brief` (with `task`) |
| POST | `/tasks/{task_id}/root-redaction` | M + root delete rights, `If-Match` (root) | `{reason}` | 200 `Task` |
| GET | `/tasks/{task_id}/claims` | `read` | `page_token`, `limit` | 200 `ClaimPage` (no proofs; newest first) |
| POST | `/tasks/claim-next` | `read`+`claim` | `ClaimNextRequest` | 201 `ClaimResponse` or 200 `{outcome:"no_work", timed_out}` |
| POST | `/tasks/{task_id}/claims` | `read`+`claim`, eligible | `ClaimRequest` | 201 `ClaimResponse` |
| POST | `/tasks/{task_id}/claims/{claim_id}/renew` | C | `{lease_seconds?}` | 200 `Claim` |
| POST | `/tasks/{task_id}/release` | C | `{reason?}` | 200 `Task` |
| POST | `/tasks/{task_id}/complete` | C | `{result: {summary?, evidence?, data?}}` | 200 `Task` |
| POST | `/tasks/{task_id}/fail` | C | `{reason, evidence?, data?}` | 200 `Task` |
| POST | `/tasks/{task_id}/block` | M or C | `{reason}` | 200 `Task` |
| POST | `/tasks/{task_id}/unblock` | M | `{reason?}` | 200 `Task` |
| POST | `/tasks/{task_id}/cancel` | M | `{reason}` | 200 `Task` |
| POST | `/tasks/{task_id}/reopen` | M | `{reason?}` | 200 `Task` |
| PUT | `/tasks/{task_id}/assignee` | M, `If-Match` | `{assignee_actor_id: string \| null}` | 200 `Task` |
| PUT | `/tasks/{task_id}/dependencies` | M, `If-Match` | `{task_ids: [string]}` | 200 `Task` |

`*` = repeatable. Inventory additions beyond design §11.2:
`GET …/briefs/{revision}` and `GET …/claims` (claim history, needed for
BOOT-04 evidence of execution IDs and lease outcomes).

Changes to P1 shapes (additive): `Thread` gains `task_id` (string or null);
`Discovery` gains the limits and capability of TASK-002..003; snapshot
pages gain item `type: "task"`.

## 3 Request shapes

Unknown fields are rejected (WIRE-001). `extensions` is allowed where P1
allows it.

```
CreateTaskRequest = exactly one of:
  { thread: { channel: ChannelPath, title: string, text: string,
              labels?: {key: value} },
    assignee_actor_id?: string|null,
    depends_on?: [task_id] }
  { thread_id: string,
    assignee_actor_id?: string|null,
    depends_on?: [task_id] }

CreateTaskBatchRequest =
  { tasks: [ { ref: string,
               thread: { channel, title, text, labels? },
               assignee_actor_id?: string|null,
               depends_on?: [ {ref: string} | {task_id: string} ] } ] }   // 1..100

PatchTaskRequest  = { title?: string, owner_actor_id?: string }        // ≥1 field
ClaimRequest      = { lease_seconds?: int, expect_brief_revision?: int }
ClaimNextRequest  = { channel?: ChannelPath, subtree?: ChannelPath,
                      labels?: { key: [value] },      // [] = key present
                      assignee?: "eligible"|"me"|"unassigned",
                      lease_seconds?: int, wait_ms?: int }
```

Normalization for fingerprints (ADR-0003): defaults applied
(`assignee: "eligible"`, `lease_seconds` = default, `wait_ms: 0`), label
keys lowercased, value lists and `task_ids` sorted and de-duplicated,
batch item order kept (it is semantic: creation order). Proof-bearing
commands add the proof's claim ID to the fingerprint, not the secret.

## 4 Response shapes

- `Task` — fields of TASK-021; example in `spec/p2-tasks.md` §19.1.
- `Claim` — `id`, `task_id`, `actor_id`, `execution_id`, `run_id`, `epoch`,
  `brief_revision`, `status` (`active` | `released` | `expired` |
  `completed` | `failed` | `revoked`), `revision`, `lease_seconds`,
  `acquired_at`, `expires_at`, `ended_at`, `end_recorded_at`.
- `ClaimResponse` — `outcome: "claimed"`, `claim`, `proof` (string or
  null), `proof_unavailable` (bool), `brief` (`{revision, text,
  redacted}`), `task`. Example in §19.2 of the spec.
- `Brief` — `task_id`, `revision`, `text` (null if redacted), `redacted`,
  `source` (`{kind: "root"|"brief_update", message_id, message_revision}`),
  `reason`, `created_by_actor_id`, `created_at`. The `POST` response adds
  `task`.
- `TaskBatch` — `batch_id`, `tasks: [{ref, task}]` in request order.
- `TaskPage`, `BriefPage`, `ClaimPage` — `items`, `next_page_token`.
- Problem documents add the members of spec §18.

## 5 Storage sketch (migration `0002_p2_tasks.sql`)

Reference only; the protocol is storage-neutral.

```sql
CREATE TABLE tasks (
    id                 text PRIMARY KEY,
    board_id           text NOT NULL REFERENCES boards(id),
    thread_id          text NOT NULL,
    channel_id         text NOT NULL,                 -- copy of thread channel (threads do not move)
    owner_actor_id     text NOT NULL REFERENCES actors(id),
    assignee_actor_id  text REFERENCES actors(id),
    created_by         text NOT NULL REFERENCES actors(id),
    origin             text NOT NULL CHECK (origin IN ('new_thread','converted','batch')),
    batch_id           text,
    state              text NOT NULL CHECK (state IN
                         ('waiting','ready','claimed','blocked','completed','failed','canceled')),
    revision           bigint NOT NULL DEFAULT 1,
    epoch              bigint NOT NULL DEFAULT 0,
    retry_count        int    NOT NULL DEFAULT 0,
    brief_revision     int    NOT NULL DEFAULT 1,
    current_claim_id   text,                          -- set iff state = 'claimed'
    claim_expires_at   timestamptz,                   -- copy of the claim's expires_at
    created_position   bigint NOT NULL,               -- position of task.created
    block              jsonb,                         -- {reason, cause, by_actor_id, at}
    outcome            jsonb,                         -- see spec §19.1
    root_redacted_at   timestamptz,
    created_at         timestamptz NOT NULL,
    updated_at         timestamptz NOT NULL,
    state_changed_at   timestamptz NOT NULL,
    UNIQUE (board_id, id),
    UNIQUE (thread_id),                               -- at most one task per thread
    FOREIGN KEY (board_id, thread_id)  REFERENCES threads(board_id, id),
    FOREIGN KEY (board_id, channel_id) REFERENCES channels(board_id, id),
    CHECK ((state = 'claimed') = (current_claim_id IS NOT NULL)),
    CHECK ((state = 'claimed') = (claim_expires_at IS NOT NULL))
);
-- Claim-next: ready queue in creation order, per board.
CREATE INDEX tasks_ready_queue   ON tasks (board_id, created_position)
    WHERE state = 'ready';
-- Claim-next with assignee = me / eligible.
CREATE INDEX tasks_ready_assignee ON tasks (board_id, assignee_actor_id, created_position)
    WHERE state = 'ready';
-- Lapsed-lease candidates for claim-next, and the sweeper.
CREATE INDEX tasks_claim_expiry  ON tasks (board_id, claim_expires_at)
    WHERE state = 'claimed';
-- Lists in creation order.
CREATE INDEX tasks_board_created ON tasks (board_id, created_position);

CREATE TABLE claims (
    id                text PRIMARY KEY,
    board_id          text NOT NULL,
    task_id           text NOT NULL,
    actor_id          text NOT NULL REFERENCES actors(id),
    credential_id     text NOT NULL REFERENCES credentials(id),
    run_id            text,
    execution_id      text NOT NULL UNIQUE,
    epoch             bigint NOT NULL,
    brief_revision    int    NOT NULL,
    lease_seconds     int    NOT NULL,
    status            text NOT NULL CHECK (status IN
                        ('active','released','expired','completed','failed','revoked')),
    revision          bigint NOT NULL DEFAULT 1,      -- +1 per renewal
    acquired_at       timestamptz NOT NULL,
    expires_at        timestamptz NOT NULL,
    ended_at          timestamptz,                    -- = expires_at for lapses
    end_recorded_at   timestamptz,
    proof_verifier    bytea NOT NULL,                 -- SHA-256(secret)
    proof_ciphertext  bytea,                          -- AES-256-GCM; NULL once ended (ADR-0004)
    proof_key_id      text,
    FOREIGN KEY (board_id, task_id) REFERENCES tasks(board_id, id),
    UNIQUE (task_id, epoch)
);
-- Belt and braces for CLAIM-002 (recorded-active; lapses are still 'active' until materialized).
CREATE UNIQUE INDEX claims_one_active ON claims (task_id) WHERE status = 'active';
CREATE INDEX claims_task_history ON claims (task_id, epoch DESC);

CREATE TABLE task_briefs (
    board_id          text NOT NULL,
    task_id           text NOT NULL,
    revision          int  NOT NULL,
    text              text,                           -- NULL once redacted
    source_kind       text NOT NULL CHECK (source_kind IN ('root','brief_update')),
    source_message_id text NOT NULL REFERENCES messages(id),
    source_message_revision bigint NOT NULL,
    reason            text,
    created_by        text NOT NULL REFERENCES actors(id),
    created_at        timestamptz NOT NULL,
    redacted_at       timestamptz,
    PRIMARY KEY (task_id, revision),
    FOREIGN KEY (board_id, task_id) REFERENCES tasks(board_id, id)
);

CREATE TABLE task_dependencies (
    board_id            text NOT NULL,
    task_id             text NOT NULL,                -- dependent
    depends_on_task_id  text NOT NULL,                -- prerequisite
    created_by          text NOT NULL REFERENCES actors(id),
    created_at          timestamptz NOT NULL,
    PRIMARY KEY (task_id, depends_on_task_id),
    FOREIGN KEY (board_id, task_id)            REFERENCES tasks(board_id, id),
    FOREIGN KEY (board_id, depends_on_task_id) REFERENCES tasks(board_id, id),
    CHECK (task_id <> depends_on_task_id)
);
-- Activation on completion and reverse traversal for cycle checks.
CREATE INDEX task_dependencies_reverse ON task_dependencies (board_id, depends_on_task_id);

CREATE TABLE task_batches (
    id          text PRIMARY KEY,
    board_id    text NOT NULL REFERENCES boards(id),
    created_by  text NOT NULL REFERENCES actors(id),
    items       jsonb NOT NULL,                       -- [{ref, task_id}] in request order
    created_at  timestamptz NOT NULL
);
```

Notes:

- `abby.task.state` is not stored in `message_labels`. Root-label reads,
  thread lists, and event facets join `tasks.state` (with the ADR-0004
  projection) so there is one source of truth and the effective state is
  what filters see.
- Idempotency receipts are unchanged. `resource_type` is `task`, `claim`,
  `task_batch`, or `brief`; replay regenerates from these rows.
- Root redaction extends P1 `scrubMessage`: it also nulls
  `task_briefs.text`. Snapshot `task` items hold no brief text, so only
  the root `message` items need scrubbing; open snapshot sessions that
  hold the task are updated to the new task view in the same transaction
  (as P1 does for deleted messages).
- Event payloads that list dependencies store `{task_id, channel_id}`
  pairs; the read path drops pairs whose channel the reader cannot read
  (DEP-010).

## 6 Claim-next under the per-board lock

Transaction (READ COMMITTED, the P1 default for `boardMutation`):

1. `pg_advisory_xact_lock(idempotency scope)`; replay check (ADR-0003).
2. `SELECT … FROM board_heads … FOR UPDATE` — the board lock.
3. `now := clock()` (CLAIM-021). Load policy; compute `C` = IDs of
   channels where the caller has `read` and `claim` and that match
   `channel`/`subtree`. If `C` is empty → no work.
4. Select the candidate (each statement takes a fresh READ COMMITTED
   snapshot, which starts after step 2 and so sees every commit of every
   previous lock holder):

   ```sql
   SELECT id FROM (
       (SELECT id, created_position FROM tasks
         WHERE board_id = $1 AND state = 'ready' AND channel_id = ANY($C)
           AND <assignee clause> AND <root label clauses>
         ORDER BY created_position, id LIMIT $k)
     UNION ALL
       (SELECT id, created_position FROM tasks
         WHERE board_id = $1 AND state = 'claimed' AND claim_expires_at <= $now
           AND channel_id = ANY($C) AND <assignee clause> AND <root label clauses>
         ORDER BY created_position, id LIMIT $k)
   ) c ORDER BY created_position, id LIMIT 1;
   ```

   Then `SELECT … FROM tasks WHERE id = $chosen FOR UPDATE` (the row lock
   is uncontended because the board lock is held; it keeps the fixed lock
   order board → resource).

   `<assignee clause>`: `eligible` → `(assignee_actor_id IS NULL OR
   assignee_actor_id = $actor)`; `me` → `= $actor`; `unassigned` → `IS
   NULL`. Root label clauses are `EXISTS` on `message_labels` of
   `threads.root_message_id` (AND across keys, OR within a key), plus
   `tasks.state` for `abby.task.state`.
5. If the row was a lapsed claim, materialize it (ADR-0004). Then insert
   the claim, update the task (`state`, `epoch+1`, `revision+1`,
   `current_claim_id`, `claim_expires_at`), emit `task.claimed` and
   `thread.view_invalidated`, store the receipt, commit.

**Why the result is always eligible at commit.** Eligibility depends on
task state, assignment, dependencies (through state), root labels, the
caller's grants and channel modes, and time. Every write to any of these
except time and credential revocation is a board mutation and takes the
same `board_heads` row lock (P1 grants, channels, labels, and all task
commands). While claim-next holds the lock, none of them can commit, and
the snapshot of step 4 already includes all that committed before. Time is
fixed at step 3 and lapses are handled explicitly. Credential revocation
is server-level; design §4.2 lets an operation authorized before the
revocation commits finish. So the selected task is eligible at the instant
the claim commits, and no later writer can see an intermediate state.

**Why it is the oldest eligible.** Both sub-queries walk partial indexes
in `created_position` order and the outer query merges them. A row that a
filter rejects is skipped, not returned; `$k` bounds each walk, and if
both walks are exhausted without a hit the server repeats with the next
`created_position` window (keyset), so a long prefix of non-matching rows
cannot hide an older eligible task.

**Why SKIP LOCKED is not used.** The board lock already serializes
claimers on one board; SKIP LOCKED would only matter without it, and it
would break "oldest eligible" by skipping a row another transaction is
about to release (design §12.3).

**Waiting.** If step 4 finds nothing and `wait_ms > 0`, the transaction
commits nothing and ends (no key reserved). The handler waits on the
process Hub (P1 LISTEN wakeups for the board) or a 2 s re-check timer,
whichever is first, then repeats from step 1 with re-authentication,
until a claim commits or the deadline passes. No connection or lock is
held while waiting. Client disconnect cancels the wait context.

**Contention.** Claim-next shares the per-board lock with every board
write. Each attempt is short (indexed walk + a few writes). Lock wait and
claim latency are to be measured under ADR-014; a split ready-queue lock
is a possible later change only if it keeps the guarantees above.

## 7 Sweeper

Worker loop every 10 s: `SELECT DISTINCT board_id FROM tasks WHERE state
= 'claimed' AND claim_expires_at <= now()` (index `tasks_claim_expiry`);
for each board, one board transaction that re-selects lapsed rows `FOR
UPDATE` (bounded, e.g. 100) and materializes them per ADR-0004. It also
nulls `proof_ciphertext` on materialized claims. Metric: expiry lag
(`recorded_at − effective_at`).

## 8 Alternatives considered

| Alternative | Why not |
|---|---|
| Proof in the request body | Bodies are fingerprinted and more often logged; a header is easy to strip and keeps bodies identical across retries. |
| Claim ID in every claimant path | The proof already names its claim; the extra segment adds a second source of truth. Kept on renew only because the design lists it. |
| Store `abby.task.state` in `message_labels` | Two sources of truth, and stored values would be wrong between a lapse and its materialization. |
| Separate `task_state_changes` table for history | The event log already is the history; `last_claim` and `GET …/claims` cover the common reads. |
| `SELECT … FOR UPDATE SKIP LOCKED` queue without the board lock | Breaks event order = commit order and the oldest-eligible promise. |
| One `POST /tasks/{id}/transitions` route | Generic command routes are harder to authorize and document; design §11.2 lists explicit commands. |
