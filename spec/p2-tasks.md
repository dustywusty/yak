# Abby protocol, version 1 (draft) — P2 profile `abby.p2.tasks`

Status: draft, approved by the owner for implementation on 2026-11-20. This document is normative for the P2
profile. It extends `spec/protocol.md` (profile `abby.p1.core`); every P1
requirement still applies unless a requirement here amends it by ID. The
wire shapes are in ADR-0016 until `spec/openapi.yaml` and `spec/schemas/`
gain the task components. The decisions behind this text are ADR-0004
(claim expiry and reads), ADR-0008 (dependencies and briefs), and ADR-0016
(wire shapes and storage).

The key words MUST, MUST NOT, SHOULD, and MAY are used as in RFC 2119.
Every MUST maps to a check in the traceability table (§20).

Design baseline: `docs/design/Abby_Design.md` §3.1, §4.1, §5.1, §7, §8,
§11.2, §11.3, §14.1, §15.2.

## 0 Owner constraints (non-negotiable)

These constraints come from the project owner. Every requirement below is
written to keep them. A later change that breaks one of them needs an
owner decision, not an ADR alone.

1. Task state is typed. Messages never complete tasks and never grant authority.
2. A claim binds one execution attempt. Stale operations are rejected.
3. Correctness does not depend on the expiry sweeper.
4. Event order is commit order.
5. Idempotency replay does not extend an expired claim.
6. A completed task is terminal. Further work is a follow-up task.

## 1 Terms

| Term | Meaning |
|---|---|
| Task | Typed work resource bound to exactly one thread. |
| Owner | The actor accountable for the task. Owner + `task_manage` may manage it. Ownership grants no capability by itself. |
| Assignee | Optional actor that is the only eligible claimant. Assignment is eligibility, not ownership and not a claim. |
| Claim | Expiring, exclusive right of one actor to work on one task for one execution attempt. |
| Execution | The attempt that a claim binds. One claim = one execution, identified by `execution_id`. |
| Epoch | Per-task integer, incremented by every new claim. A fencing token for external systems. |
| Proof | Secret returned once with a new claim. Presented in `Abby-Claim-Proof` to act as the claimant. |
| Manager | For a given task: its owner while holding `task_manage` on the task channel, or any actor holding `task_admin` there. Both also need current `read`. |
| Active claim | A claim whose recorded status is `active` and whose `expires_at` is later than server time. |
| Effective view | The task as it is at server time `now`, with any lapsed lease applied (§7.6). |
| Nonterminal | `waiting`, `ready`, `claimed`, `blocked`. |
| Terminal | `completed` (final), `failed`, `canceled` (both reopenable). |

## 2 Discovery

- **TASK-001** A server that implements this profile MUST list
  `abby.p2.tasks` in `protocol.profiles` of `GET /v1/server`.
- **TASK-002** Discovery MUST add these `limits`:
  `claim_lease_default_seconds` (reference 120), `claim_lease_min_seconds`
  (15), `claim_lease_max_seconds` (300), `max_batch_tasks` (100),
  `max_dependencies_per_task` (100), `max_task_result_bytes` (16384),
  `max_evidence_items` (50), and `max_claim_wait_ms` (30000).
- **TASK-003** Discovery `capabilities` stays the P1 array of capability
  identifiers. A server whose replayed claim responses can return the proof
  (CLAIM-041) MUST list `abby.tasks.claim_proof_replay` in it; a server
  that cannot MUST NOT list it, and then a replay always returns
  `proof_unavailable`. In this document, "`claim_proof_replay` is true"
  means that the identifier is listed.

## 3 Creating tasks

- **TASK-010** `POST …/tasks` with a `thread` object creates a new thread,
  its root message, and a task in one transaction. The caller MUST hold
  `read`, `post`, and `task_manage` on the channel. The caller becomes the
  owner and `created_by_actor_id`. Channel auto-creation follows CHAN-002.
- **TASK-011** `POST …/tasks` with `thread_id` converts an existing thread.
  The caller MUST hold current `read` and `post` on the thread channel and
  MUST be either (a) the root author and hold `task_manage`, or (b) hold
  `task_admin`. Otherwise the server MUST return `403 forbidden` and MUST
  NOT change the thread. (Design §3.1, CT-025.)
- **TASK-012** Conversion MUST NOT change the authorship, text, revision,
  or sequence of any existing message. The caller becomes owner. The root
  author gains no task rights from authorship alone.
- **TASK-013** A thread has at most one task. Converting a thread that
  already has a task MUST return `409 already_exists` with `task_id` if the
  caller can read the task. Converting a thread whose root is a tombstone
  MUST return `409 invalid_transition`.
- **TASK-014** At creation the server MUST store brief revision 1 as an
  immutable copy of the root text, with provenance (root message ID and
  revision). See §12.
- **TASK-015** The initial state MUST be `ready` if every prerequisite is
  `completed`, otherwise `waiting` (§9). A task with no prerequisites is
  `ready`. An optional `assignee_actor_id` follows TASK-051.
- **TASK-016** Task creation and conversion MUST carry `Idempotency-Key`
  (IDEM-001 applies).

## 4 Task view and state

- **TASK-020** The state set for protocol v1 is closed: `waiting`, `ready`,
  `claimed`, `blocked`, `completed`, `failed`, `canceled`. Clients MUST
  treat an unknown state as an error and MUST NOT treat it as done (design
  §2.1).
- **TASK-021** A task view MUST include: `id`, `board_id`, `thread_id`,
  `channel_id`, `channel_path`, `title`, `owner_actor_id`,
  `assignee_actor_id` (null if none), `created_by_actor_id`, `origin`
  (`new_thread` | `converted` | `batch`), `state`, `revision`,
  `brief_revision`, `epoch`, `retry_count`, `dependencies`,
  `unreadable_dependencies_pending`, `current_claim`, `last_claim`, `block`,
  `outcome`, `root_redacted`, `created_at`, `updated_at`,
  `state_changed_at`. The `ETag` is `"<revision>"`.
- **TASK-022** Task views, task lists, task events, and snapshots MUST NOT
  contain a claim proof or brief text. Brief text is read only through the
  brief routes (§12) and the claim response.
- **TASK-023** `current_claim` MUST be the active claim, or null. A claim
  whose lease is over MUST NOT appear as `current_claim`, even if its
  expiry is not yet recorded (§7.6). `last_claim` MUST be the most recent
  claim that is not active, or null.
- **TASK-024** `title` is the thread title. `GET …/threads/{id}` MUST add an
  optional `task_id` field (null if the thread has no task).
- **TASK-025** Reads (`GET …/tasks/{id}`, `GET …/tasks`, snapshots, claim
  and replay responses) MUST return the effective view (§7.6).
- **TASK-026** `GET …/tasks` lists readable tasks in creation order, with
  filters `channel`, `subtree`, `state` (repeatable, effective state),
  `assignee` (actor ID or `none`), `owner`, and `label` (P1 syntax,
  evaluated on root labels). Pagination follows PAGE-001.

### 4.1 Reserved label `abby.task.state`

- **TASK-027** The root message of a task thread MUST expose the label
  `abby.task.state` whose value is the effective task state. The label is
  therefore a root label of the thread (LBL-003) and applies to every
  message of the thread for filtering.
- **TASK-028** Clients MUST NOT set, change, or remove any `abby.*` label;
  the server MUST reject such an attempt with `400 validation_failed`
  (LBL-002). The label is a projection of task state. It is never an input.
- **TASK-029** Threads without a task MUST NOT have `abby.task.state`.
  `label=abby.task.state` therefore selects task threads.

## 5 Lifecycle

- **TASK-030** Every state change MUST happen only through the operations
  in the table below. Generic `PATCH` MUST NOT change `state`, `assignee`,
  dependencies, or brief. Any operation not allowed from the current
  effective state MUST return `409 invalid_transition` with
  `current_state`, and MUST commit nothing.
- **TASK-031** Before any task operation evaluates its preconditions, the
  server MUST apply a lapsed lease on that task (§7.6). If the operation
  then commits, the expiry event MUST commit first in the same transaction.
- **TASK-032** A `completed` task is terminal. No operation may change its
  state, assignee, dependencies, or brief. Additional work MUST be a new
  task (a follow-up task MAY depend on the completed task).

`M` = manager (§1). `C` = current claimant: same actor, presents the proof
of the active claim, and holds current `read` and `claim`. `tvi` =
`thread.view_invalidated` with `reason: "task_state_changed"` (§15.2).

| # | From (effective) | Operation | Who | Preconditions | To | Events in order |
|---|---|---|---|---|---|---|
| L1 | — | Create with new thread | `read`+`post`+`task_manage` | Prerequisites readable, same board, acyclic | `ready` / `waiting` | `thread.created`, `task.created` |
| L2 | — | Convert thread | `read`+`post` and (root author+`task_manage` or `task_admin`) | No task on thread; root not deleted | `ready` / `waiting` | `task.created`, `tvi` |
| L3 | `ready` | Claim / claim-next | `read`+`claim`, eligible (TASK-052) | No active claim | `claimed` (epoch+1, new execution) | `task.claimed`, `tvi` |
| L4 | `claimed` | Renew | C | Claim active | `claimed` | `task.claim_renewed` |
| L5 | `claimed` | Release | C | Claim active | `ready` | `task.released`, `tvi` |
| L6 | `claimed` | Lease lapses | Server (materialization) | `expires_at` ≤ now | `ready` | `task.claim_expired`, `tvi` |
| L7 | `claimed` | Complete | C | Claim active | `completed` | `task.completed`, `tvi`, then per dependent: `task.became_ready`, `tvi` |
| L8 | `claimed` | Fail | C, reason | Claim active | `failed` | `task.failed`, `tvi` |
| L9 | `ready`, `waiting` | Block | M, reason | — | `blocked` | `task.blocked`, `tvi` |
| L10 | `claimed` | Block | M or C, reason | — | `blocked`; claim revoked | `task.blocked`, `tvi` |
| L11 | `blocked` | Unblock | M | — | `ready` / `waiting` | `task.unblocked`, `tvi` |
| L12 | nonterminal | Cancel | M, reason | — | `canceled`; claim revoked | `task.canceled`, `tvi` |
| L13 | `failed`, `canceled` | Reopen | M | Root not redacted | `ready` / `waiting`; `retry_count`+1 | `task.reopened`, `tvi` |
| L14 | `waiting` | Last prerequisite completes | Server, same transaction as L7 | All prerequisites `completed` | `ready` | `task.became_ready`, `tvi` |
| L15 | `waiting`, `ready`, `blocked` | Replace dependencies | M, `If-Match` | No active claim; acyclic | `waiting`↔`ready`; `blocked` stays | `task.dependencies_changed` (+`tvi` if state changed) |
| L16 | `waiting`, `ready`, `blocked` | Replace assignee | M, `If-Match` | No active claim | unchanged | `task.assigned` |
| L17 | nonterminal | Brief update | M + `post`, `If-Match` | — | `blocked`; claim revoked | `message.created`, `task.brief_updated` (+`tvi` if state changed) |
| L18 | any | Root redaction | M + message deletion rights, `If-Match` on root | — | nonterminal → `canceled`; terminal unchanged | `message.deleted`, then `task.canceled` + `tvi`, or `task.brief_redacted` |
| L19 | any except `completed` | Edit title / owner | M, `If-Match` | — | unchanged | `thread.updated` (title), `task.updated` |

Rows not listed are invalid. In particular: claim from `waiting`,
`blocked`, or terminal; complete or fail without an active claim; unblock
from anything but `blocked`; reopen from `completed`; block of `blocked`;
any change to `completed` except root redaction.

- **TASK-033** Release and lapse (L5, L6) MUST return the task to `ready`.
  (Dependencies cannot change during an active claim (DEP-006) and a
  completed prerequisite cannot become incomplete (TASK-032), so the
  prerequisites still hold.)
- **TASK-034** Block, cancel, and brief update MUST revoke any active claim
  in the same transaction. A revoked claim's status becomes `revoked`; its
  proof MUST then fail with `409 claim_lost`.
- **TASK-035** Unblock and reopen MUST evaluate prerequisites at commit
  time: `ready` if all are `completed`, else `waiting`.
- **TASK-036** Reopen MUST NOT create a claim or an execution. It MUST
  keep the prior `outcome` in history (the event log and `last_claim`), and
  MUST set `outcome` to null on the task view.
- **TASK-037** Reasons for block, cancel, and fail are required, 1 to
  `limits.max_text_bytes`. Reasons for release, unblock, and reopen are
  optional.
- **TASK-038** Lifecycle commands by a manager (block, unblock, cancel,
  reopen) MAY carry `If-Match`; a stale value returns `412
  revision_conflict`.

## 6 Authorization

- **TASK-040** Every task operation MUST require current `read` on the task
  channel. A task the caller cannot read MUST be `404 not_found` (AUTH-015).
- **TASK-041** Manager operations (L9–L13, L15–L19) MUST require a manager
  (§1). Owner without `task_manage` is not a manager. Denies and credential
  ceilings apply to every capability (AUTH-005, AUTH-012).
- **TASK-042** Claimant operations (L4, L5, L7, L8, L10 as C) MUST require
  current `read` and `claim`, the same actor as the claim, and the proof of
  the active claim. Loss of `claim` or `read` after acquisition MUST make
  these operations fail (`403` or `404`). The claim is not revoked by the
  loss; it lapses at `expires_at` unless a manager blocks the task.
- **TASK-043** A proof MUST NOT confer authority on a different actor. A
  valid proof presented by another actor MUST return `403 forbidden`.
- **TASK-044** Owner, assignee, and claim are independent. An owner is not
  eligible to claim an assigned task unless the owner is the assignee. A
  claimant is not a manager. An assignee is not a manager.

## 7 Claims

### 7.1 Acquisition and fencing

- **CLAIM-001** `POST …/tasks/{id}/claims` claims one task (L3). It MUST
  commit only if the effective state is `ready`, no claim is active, and
  the caller is eligible (TASK-052). If another claim is active the server
  MUST return `409 task_claimed`. If the state is not `ready`, `409
  invalid_transition`.
- **CLAIM-002** At most one claim per task MUST be active at any time,
  under any concurrency (CT-007).
- **CLAIM-003** Every successful claim MUST create a new claim ID, a new
  server-issued `execution_id`, and a new epoch equal to the previous task
  epoch plus one, including a re-acquisition by the same actor after
  release or lapse. Epochs of one task MUST strictly increase.
- **CLAIM-004** The claim MUST record `actor_id`, `credential_id`,
  `run_id` (from `Abby-Run-Id`, optional, no authority), `epoch`,
  `execution_id`, `brief_revision` (the task's current brief revision),
  `lease_seconds`, `acquired_at`, and `expires_at`.
- **CLAIM-005** An optional `expect_brief_revision` in the claim request
  MUST cause `412 revision_conflict` if it differs from the current brief
  revision. This lets a runner claim only the instructions it has read.

### 7.2 Proof

- **CLAIM-010** The successful claim response (and claim-next response)
  MUST contain the proof. The proof is a high-entropy secret (≥ 128 bits of
  randomness). Clients MUST treat it as opaque.
- **CLAIM-011** The server MUST store only a verifier of the proof. The
  proof MUST NOT appear in task views, claim views, events, snapshots,
  audit records, logs, or error documents.
- **CLAIM-012** Clients present the proof in the request header
  `Abby-Claim-Proof: <proof>`. It MUST NOT be accepted in a body or query
  parameter.
- **CLAIM-013** A claimant operation without the header MUST return
  `428 precondition_required`. A malformed proof MUST return
  `400 validation_failed`.
- **CLAIM-014** A well-formed proof that does not verify as the active
  claim of this task — expired (even if not yet recorded), released,
  revoked, settled, superseded by a newer epoch, or belonging to another
  task — MUST return `409 claim_lost` with the task's current `state` and
  `epoch`, and MUST commit nothing (CT-008).
- **CLAIM-015** On the renew route the `claim_id` path segment MUST match
  the claim of the proof; a mismatch is `409 claim_lost`.

### 7.3 Leases

- **CLAIM-020** `lease_seconds` is optional on claim, claim-next, and
  renew; the default is `limits.claim_lease_default_seconds`. A value
  outside `[claim_lease_min_seconds, claim_lease_max_seconds]` MUST return
  `400 validation_failed`. The server MUST NOT clamp silently.
- **CLAIM-021** The server MUST sample authoritative time after it acquires
  the board lock and MUST compute `expires_at = now + lease_seconds`.
- **CLAIM-022** A claim is active while `now < expires_at`. At or after
  `expires_at` every claimant operation MUST fail with `409 claim_lost`.
- **CLAIM-023** Renew (L4) MUST set `expires_at = now + lease_seconds` and
  increment the claim's `revision`. It MUST NOT change the task revision,
  epoch, execution, or state. Renewal MAY shorten the lease.

### 7.4 Settlement

- **CLAIM-030** Release (L5), complete (L7), fail (L8), and claimant block
  (L10) MUST end the claim in the same transaction with status `released`,
  `completed`, `failed`, or `revoked`, and MUST record `ended_at`.
- **CLAIM-031** Complete MUST accept `result`: `summary` (text, optional),
  `evidence` (≤ `max_evidence_items` of `{kind, ref, note}`, where `kind`
  is `message`, `commit`, `url`, `artifact`, or `other`), and `data`
  (JSON object ≤ `max_task_result_bytes`). Fail MUST require `reason` and
  accept the same `evidence` and `data`.
- **CLAIM-032** The server MUST record evidence as given and MUST NOT infer
  success or failure from evidence or text.
- **CLAIM-033** Renewal, release, completion, failure, block, and cancel on
  one task MUST serialize: exactly one of them settles a given claim, and
  every later operation with the same proof fails with `409 claim_lost`
  (CT-010).

### 7.5 Idempotency of claims

- **CLAIM-040** Claim, claim-next, renew, release, complete, fail, block,
  unblock, cancel, reopen, brief update, and root redaction MUST carry
  `Idempotency-Key`. For proof-bearing commands, the fingerprint MUST
  include the claim ID named by the proof and MUST NOT include the secret.
- **CLAIM-041** A replay of a claim or claim-next MUST return the original
  claim (same claim ID, execution ID, epoch) and MUST NOT create a claim,
  emit events, or change `expires_at`. The `proof` MUST be returned only if
  all hold: discovery lists `abby.tasks.claim_proof_replay`, the claim is active
  at replay time, and the caller currently holds `read` and `claim`.
  Otherwise the response MUST have `proof: null` and
  `proof_unavailable: true`.
- **CLAIM-042** No replay MUST extend or restore any lease. A replay of a
  renew returns the current claim view without changing it. A lapsed claim
  stays lapsed (CT-009).
- **CLAIM-043** Replay material that can reproduce a proof MUST be
  encrypted at rest with a key not stored in the database, and MUST be
  destroyed in the transaction that ends the claim (or by the sweeper for a
  lapsed claim). Without such a key the server MUST NOT list
  `abby.tasks.claim_proof_replay` (TASK-003).
- **CLAIM-044** A replay of complete, fail, or release MUST return the
  current task view with `Idempotent-Replayed: true`, even if the claim
  has ended since. This is how a client that lost a completion response
  learns that it succeeded (CT-009).
- **CLAIM-045** IDEM-004 applies: a rejected claim (`task_claimed`,
  `invalid_transition`, `forbidden`), a `claim_lost`, or a claim-next
  `no_work` result MUST NOT reserve the key.

### 7.6 Lapse, effective reads, and the sweeper (ADR-0004)

- **CLAIM-050** Correctness MUST NOT depend on the sweeper. Every read MUST
  return the effective view, and every operation MUST apply a lapsed lease
  before it evaluates preconditions (TASK-031).
- **CLAIM-051** Effective view of a task whose recorded state is `claimed`
  and whose claim has `expires_at ≤ now`: `state` = `ready`,
  `abby.task.state` = `ready`, `current_claim` = null, `last_claim` = that
  claim with `status: "expired"`, `ended_at = expires_at`, and
  `end_recorded_at: null`; `revision` = recorded revision + 1;
  `updated_at` and `state_changed_at` = `expires_at`.
- **CLAIM-052** Materialization of a lapse (by an operation or the
  sweeper) MUST write exactly the view of CLAIM-051 (same revision and
  timestamps) and MUST emit one `task.claim_expired` with `effective_at =
  expires_at` and `recorded_at` = materialization time, followed by `tvi`.
  A lapse MUST be materialized at most once.
- **CLAIM-053** A request that is rejected MUST commit nothing, including
  materialization. A rejected operation reports the effective state.
- **CLAIM-054** The reference sweeper SHOULD materialize lapses within 30
  seconds of `expires_at`. Event consumers MUST NOT assume that
  `effective_at` is monotonic in stream order.
- **CLAIM-055** Leases govern Abby state only. Abby MUST NOT claim to stop
  external processes. External systems SHOULD fence with `epoch` or
  `execution_id`.

## 8 Assignment

- **TASK-050** `PUT …/tasks/{id}/assignee` with `{assignee_actor_id}`
  (actor ID or null) replaces the assignment (L16). It MUST require a
  manager and `If-Match`.
- **TASK-051** The new assignee MUST be an actor that can currently read
  the task channel; otherwise `400 validation_failed` (the same rule as
  MEN-001, so assignment cannot probe hidden actors).
- **TASK-052** Claim eligibility: a task with `assignee_actor_id = null` is
  eligible for every actor with `read` and `claim`; an assigned task is
  eligible only for the assignee. A claim by another actor MUST return
  `403 forbidden` with `reason: "not_assignee"`.
- **TASK-053** If a claim is active, an assignment change MUST return
  `409 task_claim_active` and change neither the claim nor the
  assignment. To reassign claimed work, a manager blocks the task (which
  revokes the claim), reassigns, then unblocks (CT-027).
- **TASK-054** A terminal task MUST NOT be reassigned (`409
  invalid_transition`). Failed or canceled tasks are reopened first.

## 9 Dependencies (ADR-0008)

- **DEP-001** A dependency says "task A requires task B to be `completed`".
  Prerequisites MUST be tasks on the same board. A cross-board or unknown
  ID MUST be rejected as `404 not_found` with the offending `task_id`.
- **DEP-002** The dependency graph of a board MUST stay acyclic. A change
  that would create a cycle (including a self-dependency) MUST return
  `409 dependency_cycle` and commit nothing. The cycle check MUST include
  tasks the caller cannot read. The error MAY list the cycle path, but MUST
  list only readable task IDs.
- **DEP-003** To add a prerequisite the caller MUST be able to read it.
  An unreadable prerequisite in a request is `404 not_found` (AUTH-015).
  Removing a prerequisite the caller cannot read is not possible through
  `PUT` (DEP-005).
- **DEP-004** A task is satisfied only when all prerequisites are
  `completed`. `failed` and `canceled` prerequisites MUST remain
  unsatisfied. The server MUST NOT complete or cancel dependents
  automatically.
- **DEP-005** `PUT …/tasks/{id}/dependencies` with `{task_ids: [...]}`
  replaces the readable part of the set (L15). Prerequisites the caller
  cannot read MUST be kept unchanged. It MUST require a manager and
  `If-Match`. At most `max_dependencies_per_task` prerequisites in total.
- **DEP-006** Dependency changes MUST be rejected with `409
  task_claim_active` while a claim is active, and with `409
  invalid_transition` on a terminal task.
- **DEP-007** When a task completes, every `waiting` dependent whose
  prerequisites are now all `completed` MUST become `ready` in the same
  transaction (L14). No worker is involved. A `blocked` dependent stays
  `blocked`; its readiness is evaluated at unblock.
- **DEP-008** Reopening a failed or canceled prerequisite MUST keep every
  dependency link. Dependents stay `waiting` until it completes (CT-011).
- **DEP-009** Views and event payloads MUST list only prerequisites the
  reader can read, as `{task_id, state}`. They MUST NOT give a count of
  unreadable prerequisites. The view MUST give
  `unreadable_dependencies_pending` = true exactly when the task is
  `waiting` and every readable prerequisite is `completed` — a fact the
  reader can already derive from visible data.
- **DEP-010** `task.dependencies_changed` and `task.became_ready` payloads
  MUST be projected per reader under DEP-009 when events are read.

## 10 Claim-next (selection)

- **SEL-001** `POST …/tasks/claim-next` MUST select and claim one task in
  one transaction, or return no work. A selected task MUST be claimed under
  CLAIM-001..005 in that transaction.
- **SEL-002** Candidates MUST be tasks whose effective state is `ready`
  (including tasks whose lease has lapsed but is not yet recorded), whose
  channel the caller can read with `claim`, and for which the caller is
  eligible (TASK-052).
- **SEL-003** Filters: `channel` (exact), `subtree`, `labels` (same
  semantics as P1 `label`, on root labels), and `assignee`:
  `eligible` (default: unassigned or assigned to the caller), `me` (only
  tasks assigned to the caller), or `unassigned`.
- **SEL-004** Among candidates the server MUST pick the oldest by task
  creation position (the board event position of its `task.created`), with
  task ID as a tie-breaker.
- **SEL-005** The server MUST NOT return a task that is not eligible at the
  commit of the claim. Selection and claim MUST be atomic with respect to
  every change of state, assignment, dependencies, root labels, and policy.
- **SEL-006** If no candidate exists and `wait_ms` (0..`max_claim_wait_ms`,
  default 0) is positive, the server MUST wait until a candidate appears or
  `wait_ms` elapses. It MUST NOT hold a transaction or board lock while
  waiting, and MUST re-authenticate and re-evaluate policy on each attempt
  (AUTH-004).
- **SEL-007** No work MUST return `200` with `{"outcome": "no_work",
  "timed_out": <bool>}`. A claim MUST return `201` with
  `"outcome": "claimed"` and the claim response body (§19.2).
- **SEL-008** If the client goes away during a wait, the server SHOULD stop
  waiting. A claim that committed after the client left is recovered by
  replaying the same `Idempotency-Key` (CLAIM-041), or lapses at expiry.

## 11 Batches

- **BATCH-001** `POST …/tasks/batch` creates 1..`max_batch_tasks` tasks,
  each with a new thread, atomically. More items MUST return `413
  limit_exceeded`.
- **BATCH-002** Each item has a client-local `ref` (1–64 bytes,
  `[A-Za-z0-9._-]`, unique in the batch). `depends_on` entries are either
  `{ref}` (an item of this batch) or `{task_id}` (an existing task).
- **BATCH-003** The server MUST validate every item, every authorization,
  every reference, and the combined graph (with existing edges) before it
  creates anything. Any failure MUST reject the whole batch and commit
  nothing (CT-012). The problem document MUST include `errors`: a list of
  `{index, ref, code, detail}`, with only readable IDs.
- **BATCH-004** Tasks MUST be created, and `thread.created`/`task.created`
  pairs emitted, in request order. Creation positions therefore follow
  request order.
- **BATCH-005** The response MUST map each `ref` to its task. A replay MUST
  return the same mapping.
- **BATCH-006** A larger plan is several batches. Clients MUST NOT
  describe it as atomic. The reference plan helper SHOULD submit batches in
  topological order with deterministic keys (`<plan-id>:<batch-n>`), SHOULD
  label each root with a client label (for example `plan.ref=<ref>`) so a
  rerun can find existing tasks after receipts expire, and SHOULD report
  resumable progress.

## 12 Briefs, root protection, root redaction (ADR-0008)

- **BRIEF-001** A brief version is immutable: `revision`, `text`,
  `source` (`root` with message ID and revision, or `brief_update` with the
  notice message ID), `created_by_actor_id`, `created_at`, `reason`, and
  `redacted`. `GET …/tasks/{id}/briefs` lists versions;
  `GET …/tasks/{id}/briefs/{revision}` reads one.
- **BRIEF-002** `POST …/tasks/{id}/briefs` with `{text, reason}` and
  `If-Match` (task revision) is the task-aware brief update (L17). It MUST
  require a manager and current `post`.
- **BRIEF-003** On a nonterminal task the update MUST, in one transaction:
  store brief revision n+1; append a reply in the thread, authored by the
  caller, that states the new revision and the reason (it does not carry
  the brief text) and has the server label `abby.task.brief_revision=<n+1>`;
  revoke any active claim; and set or keep `blocked` with `block.cause =
  "brief_updated"`. The task stays `blocked` until a manager unblocks it.
- **BRIEF-004** A brief update on `completed` MUST return `409
  invalid_transition`; on `failed` or `canceled` it MUST return `409
  invalid_transition` with a hint to reopen first (CT-026).
- **BRIEF-005** Brief update and completion or cancellation MUST serialize:
  either the terminal operation commits first and the update fails, or the
  update commits first and the claim is lost (CT-026).
- **BRIEF-010** On a task thread, the P1 routes `PATCH` and `DELETE
  …/messages/{root}` and `PATCH …/threads/{id}` MUST return `409
  task_root_protected` and commit nothing, for every caller including the
  root author and board administrators. This amends AUTH-020 for task
  threads. Replies, reactions, and non-`abby.` root labels are unaffected.
- **BRIEF-011** Ordinary message authorship MUST NOT revoke a claim or
  change a brief.
- **BRIEF-020** `POST …/tasks/{id}/root-redaction` with `{reason}` and
  `If-Match` (root message revision) MUST require both a manager and the
  P1 rights to delete the root: the root author with `read`+`post`
  (deletion kind `author`), or `board_admin` with `read` and a reason
  (kind `redaction`, audited).
- **BRIEF-021** Root redaction MUST, in one transaction: tombstone the root
  and scrub it as RED-001 requires; set `text` null and `redacted: true` on
  every brief version and remove those bodies from every retained copy
  (claim replay responses, snapshot items); set `root_redacted: true`; and
  if the task is nonterminal, revoke any claim and cancel it with
  `cause: "root_redacted"`. A terminal task keeps its state and outcome.
- **BRIEF-022** A task with `root_redacted: true` MUST NOT be reopened
  (`409 invalid_transition`). Further work is a new task.

## 13 Messages never drive tasks

- **TASK-060** Posting, editing, deleting, labeling, reacting to, or
  mentioning in messages MUST NOT change task state, assignment, claims,
  dependencies, or briefs, whatever the content says. Only the task
  operations of §5 do.
- **TASK-061** No message content grants a capability or manager status.
  Approval is recorded as a task transition by an authorized actor.

## 14 Task-level idempotency and conditions

- **TASK-070** `POST …/tasks`, `…/tasks/batch`, and every `POST` task
  command (CLAIM-040) MUST carry `Idempotency-Key`. `PUT` assignee and
  dependencies MUST carry `If-Match` and MAY carry `Idempotency-Key`.
- **TASK-071** A replay returns the current projection of the committed
  resource (IDEM-005): a task view for task commands, the batch mapping for
  batches, and the claim response for claims (CLAIM-041).
- **TASK-072** `PATCH …/tasks/{id}` (title, owner), `PUT` assignee and
  dependencies, and brief update MUST carry `If-Match`; missing → `428`,
  stale → `412 revision_conflict`.

## 15 Events

- **TASK-080** Every committed task change MUST emit its events (table
  §5) in the same transaction, at transactional board positions (EVT-001,
  COND-004). Event order is commit order.
- **TASK-081** Clients cannot create task events. Only operations emit
  them.
- **TASK-082** Each task event MUST have `resource` = `{type: "task", id,
  revision}` (task revision after the change), except
  `task.claim_renewed`, whose resource is `{type: "claim", id, revision}`
  (claim revision). `channel_id` and `thread_id` MUST be set.
- **TASK-083** Task events are thread-scoped for filtering: the channel,
  subtree, thread, label (root labels, before and after, including
  `abby.task.state`), and type clauses apply; the author, mention, and text
  clauses do not exclude them (same as P1 `thread.view_invalidated`).
- **TASK-084** Event payloads MUST NOT contain proofs or brief text, and
  MUST be projected per reader for dependency IDs (DEP-010).

### 15.1 Event types and payloads

Every payload has `task` (task view after the change, per-reader
projection of `dependencies`). Additional fields:

| Type | Additional payload |
|---|---|
| `task.created` | `origin`, `batch_id` (or null) |
| `task.claimed` | `claim` |
| `task.claim_renewed` | `claim` |
| `task.released` | `claim`, `reason` |
| `task.claim_expired` | `claim` (`status: "expired"`) — `effective_at = claim.expires_at` |
| `task.completed` | `claim`, `outcome` |
| `task.failed` | `claim`, `outcome` |
| `task.blocked` | `reason`, `cause` (`manual` \| `claimant` \| `brief_updated`), `revoked_claim` (or null) |
| `task.unblocked` | `reason` |
| `task.canceled` | `reason`, `cause` (`manual` \| `root_redacted`), `revoked_claim` (or null) |
| `task.reopened` | `reason`, `previous_outcome` |
| `task.became_ready` | `cause: "dependencies_satisfied"`, `prerequisite_task_id` (omitted if unreadable) |
| `task.dependencies_changed` | `added`, `removed` (readable IDs only), `state_before`, `state_after` |
| `task.assigned` | `assignee_before`, `assignee_after` |
| `task.updated` | `changed` (field names), `title`, `owner_actor_id` |
| `task.brief_updated` | `brief_revision`, `message_id`, `state_before`, `revoked_claim` (or null) |
| `task.brief_redacted` | `brief_revisions` (list of revision numbers) |

`claim` and `revoked_claim` are claim views (§19) without proof.

### 15.2 Thread view invalidation

- **TASK-085** Every change of effective task state changes the root label
  `abby.task.state`, so the server MUST emit `thread.view_invalidated`
  with `reason: "task_state_changed"` and `labels_before`/`labels_after`
  right after the task event (EVT-006). A new task created with a new
  thread (L1) does not need it: its `thread.created` already shows the
  label.

## 16 Interaction with P1 cursors, snapshots, and filters

- **TASK-090** Task events share the board stream, cursors, retention,
  history epoch, and policy binding of P1 (EVT-001..012). Grants or
  channel-mode changes that alter task visibility change the policy
  fingerprint and yield `409 resync_required` (EVT-009).
- **TASK-091** `type=` filters accept the task event types of §15.1.
- **TASK-092** Snapshots MUST add one `task` item right after each `thread`
  item whose thread has a task. The item is the effective view at capture
  time, projected per reader. Replay from the continuation cursor then
  reaches a consistent state: a later `task.claim_expired` for a lapse that
  the snapshot already showed carries the same revision, so consumers that
  apply events by revision ignore it.
- **TASK-093** A snapshot with label filter `abby.task.state=<s>` MUST use
  the effective state at capture time.

## 17 Runner helper (reference CLI)

These requirements apply to the reference CLI runner (`abby run`, PAR-033)
and to any client that claims to implement the managed-runner behavior.

- **RUN-001** The runner MUST obtain work only through claim or claim-next,
  and MUST keep the proof in memory only. It MUST NOT pass the proof to the
  managed subprocess. It SHOULD pass `ABBY_TASK_ID`, `ABBY_EXECUTION_ID`,
  and `ABBY_CLAIM_EPOCH`.
- **RUN-002** The runner MUST renew at about one third of the granted lease
  with random jitter (±10 %), using a new `Idempotency-Key` per renewal.
- **RUN-003** The runner MUST compute its local deadline as (local
  monotonic time when it sent the request that produced the current
  expiry) + `lease_seconds`. It MUST NOT compare `expires_at` with local
  wall-clock time.
- **RUN-004** If no renewal is confirmed by the local deadline minus a
  safety margin (max(5 s, lease/6)), the runner MUST stop the subprocess
  and stop mutating work. On `409 claim_lost` or a lost `read`/`claim`
  right it MUST stop at once.
- **RUN-005** Exit status maps to settlement: 0 → complete; non-zero → fail
  with the exit status in `data`; runner interrupt (SIGINT/SIGTERM) →
  stop the subprocess, then release. The runner MUST NOT settle without
  the result it was given. If the server rejects the complete or fail
  request with 400 or 413, or if the subprocess wrote a result that the
  runner cannot read or parse, the runner MUST NOT settle again without
  that result. It MUST keep the claim alive until it releases the claim,
  MUST release with a reason that names the rejection or the parse
  problem, and MUST report the result that was not settled. The reference
  CLI exits 13 in this case (spec/cli.md).
- **RUN-006** Every settlement retry MUST reuse its original
  `Idempotency-Key` until the outcome is known (§5.3 of the design).
- **RUN-007** After an outage the runner MUST NOT resume work from local
  belief. It MUST read the task, and resume only if a renew with its proof
  succeeds; otherwise it MUST discard the attempt and keep notes outside
  Abby.

## 18 Error catalog additions (P2)

| Status | Code | Meaning | Extra members |
|---|---|---|---|
| 403 | `forbidden` | Also: not the assignee; proof of another actor | `reason` (`not_assignee`, `proof_actor_mismatch`, `missing_capability`) |
| 409 | `task_claimed` | Another claim is active | `epoch` |
| 409 | `claim_lost` | Proof is not the active claim | `state`, `epoch` |
| 409 | `task_claim_active` | Assignment or dependency change during an active claim | `claim_expires_at` |
| 409 | `invalid_transition` | Operation not allowed from the current state | `current_state`, `hint` |
| 409 | `dependency_cycle` | Change would create a cycle | `cycle` (readable IDs only) |
| 409 | `task_root_protected` | P1 edit, delete, or title change on a task thread | `task_id` |
| 409 | `already_exists` | Thread already has a task | `task_id` (if readable) |
| 412 | `revision_conflict` | Stale `If-Match` or `expect_brief_revision` | `revision` |
| 413 | `limit_exceeded` | Batch, dependencies, evidence, or result too large | `limit` |
| 428 | `precondition_required` | Missing `If-Match` or missing `Abby-Claim-Proof` | `header` |

`task_claimed` and `claim_lost` were already named in design §5.3; the
others are new in P2.

## 19 Examples

### 19.1 Task view

```json
{
  "id": "task_example",
  "board_id": "board_example",
  "thread_id": "thread_example",
  "channel_id": "chan_server",
  "channel_path": "server",
  "title": "Implement claim-next",
  "owner_actor_id": "actor_lead",
  "assignee_actor_id": null,
  "created_by_actor_id": "actor_lead",
  "origin": "batch",
  "state": "claimed",
  "revision": 4,
  "brief_revision": 1,
  "epoch": 2,
  "retry_count": 0,
  "dependencies": [
    { "task_id": "task_schema", "state": "completed" }
  ],
  "unreadable_dependencies_pending": false,
  "current_claim": {
    "id": "claim_example",
    "task_id": "task_example",
    "actor_id": "actor_worker",
    "execution_id": "exec_example",
    "run_id": "run_example",
    "epoch": 2,
    "brief_revision": 1,
    "status": "active",
    "revision": 3,
    "lease_seconds": 120,
    "acquired_at": "2026-11-19T20:00:00Z",
    "expires_at": "2026-11-19T20:02:00Z",
    "ended_at": null,
    "end_recorded_at": null
  },
  "last_claim": {
    "id": "claim_earlier",
    "task_id": "task_example",
    "actor_id": "actor_worker_2",
    "execution_id": "exec_earlier",
    "run_id": null,
    "epoch": 1,
    "brief_revision": 1,
    "status": "expired",
    "revision": 1,
    "lease_seconds": 120,
    "acquired_at": "2026-11-19T19:50:00Z",
    "expires_at": "2026-11-19T19:52:00Z",
    "ended_at": "2026-11-19T19:52:00Z",
    "end_recorded_at": "2026-11-19T19:52:11Z"
  },
  "block": null,
  "outcome": null,
  "root_redacted": false,
  "created_at": "2026-11-19T19:40:00Z",
  "updated_at": "2026-11-19T20:01:20Z",
  "state_changed_at": "2026-11-19T20:00:00Z"
}
```

`block`, when set: `{"reason", "cause", "by_actor_id", "at"}`.
`outcome`, when set: `{"kind": "completed"|"failed"|"canceled", "reason",
"summary", "evidence", "data", "by_actor_id", "claim_id", "execution_id",
"at"}`.

### 19.2 Claim response (`201`, also the `claimed` result of claim-next)

```json
{
  "outcome": "claimed",
  "claim": {
    "id": "claim_example",
    "task_id": "task_example",
    "actor_id": "actor_worker",
    "execution_id": "exec_example",
    "run_id": "run_example",
    "epoch": 2,
    "brief_revision": 1,
    "status": "active",
    "revision": 1,
    "lease_seconds": 120,
    "acquired_at": "2026-11-19T20:00:00Z",
    "expires_at": "2026-11-19T20:02:00Z",
    "ended_at": null,
    "end_recorded_at": null
  },
  "proof": "acp1.claim_example.k3Jx0qV7bN2u8YtQe5LmRw9sZcHf1aGd4oPiUyTvXbE",
  "proof_unavailable": false,
  "brief": { "revision": 1, "text": "Implement POST /tasks/claim-next per SEL-001..008.", "redacted": false },
  "task": { "id": "task_example", "state": "claimed", "revision": 4, "epoch": 2, "...": "full task view" }
}
```

The client then sends `Abby-Claim-Proof:
acp1.claim_example.k3Jx…` on renew, release, complete, fail, and
claimant block. A replay after the lease lapsed returns the same `claim`
(with `status: "expired"`), `"proof": null`, and `"proof_unavailable":
true`.

## 20 Traceability (P2)

`CT` = black-box test in `conformance/` (planned names); `REF` =
reference-server test in `internal/` (planned); `OPS` = step in a P2 gate
script. New scenario IDs CT-029..CT-038 extend design §14.1.

| ID | Scenario (new) | Required result |
|---|---|---|
| CT-029 | Full lifecycle walk | Every row of §5 commits its events; every unlisted transition is `invalid_transition` with nothing committed |
| CT-030 | Claim-next order and filters | Oldest eligible first; channel/subtree/label/assignee filters honored; unreadable tasks never selected |
| CT-031 | Claim-next wait | A task made ready during the wait is claimed; a timeout returns `no_work` with `timed_out: true` |
| CT-032 | Lapsed lease read before any sweep | Read after `expires_at` shows effective `ready`, `current_claim: null`; another actor claims at once |
| CT-033 | Hidden prerequisite | No ID or count leaks; `unreadable_dependencies_pending` matches; readiness still follows completion |
| CT-034 | Root protection and root redaction | P1 edit/delete/title on task root → `task_root_protected`; root redaction cancels and scrubs briefs |
| CT-035 | Messages and labels cannot drive tasks | `abby.*` label writes rejected; "done" text changes nothing |
| CT-036 | Task snapshot then replay | Snapshot task items plus replay reach the live task view, including a lapse during the gap |
| CT-037 | Dependency edits | Cycle → `dependency_cycle`; edit during claim → `task_claim_active`; edit on terminal → `invalid_transition` |
| CT-038 | Proof hygiene | Proof absent from views, events, snapshots, errors; missing header → 428; other actor → 403 |

| Requirement | Verification |
|---|---|
| TASK-001..003 | CT TestDiscovery (P2 fields); schema checks |
| TASK-010, TASK-012, TASK-014..016 | CT TestTaskCreateAndConvert; CT TestCT002 pattern applied to task create |
| TASK-011, TASK-013 | CT TestCT025ConversionAuthority |
| TASK-020..026 | CT TestCT029LifecycleWalk; schema checks; CT TestCT032LapsedLeaseRead |
| TASK-027..029 | CT TestCT035MessagesCannotDriveTasks; CT TestCT014-style label enter/leave on `abby.task.state` (TestTaskStateLabelFilter) |
| TASK-030..038 | CT TestCT029LifecycleWalk; REF TestLifecycleTableExhaustive |
| TASK-040..044 | CT TestTaskAuthorization (deny, ceiling, owner without `task_manage`, assignee not manager); CT TestCT038ProofHygiene |
| TASK-050..054 | CT TestCT027AssignmentVsClaim |
| TASK-060..061 | CT TestCT035MessagesCannotDriveTasks |
| TASK-070..072 | CT TestCT009LostResponses; CT TestTaskConditions |
| TASK-080..085 | CT TestCT029LifecycleWalk (event sequences); REF TestEventPositionsContiguousUnderConcurrency extended to task commands |
| TASK-090..093 | CT TestCT036TaskSnapshotThenReplay; CT TestCT016 extended (revocation during claim-next wait) |
| CLAIM-001..003 | CT TestCT007ClaimRace (N parallel claimers, one winner, epochs strictly increase) |
| CLAIM-004..005 | CT TestClaimFields; CT TestCT026BriefUpdateVsTerminal (expect_brief_revision) |
| CLAIM-010..015 | CT TestCT008ExpiredProofRejected; CT TestCT038ProofHygiene |
| CLAIM-020..023 | CT TestLeaseBounds (14 s and 301 s rejected; default 120 s); CT TestCT010RenewCancelCompleteRace |
| CLAIM-030..033 | CT TestCT010RenewCancelCompleteRace; CT TestCompleteEvidenceRecorded |
| CLAIM-040..042, CLAIM-044..045 | CT TestCT009LostResponses (lost claim response, lost completion response, replay after lapse) |
| CLAIM-043 | REF TestClaimReplayMaterialEncrypted (ciphertext only; destroyed on end); OPS discovery shows `claim_proof_replay` matches key config |
| CLAIM-050..053 | CT TestCT032LapsedLeaseRead; CT TestCT008; REF TestExpiryProjectionMatchesMaterialization (sweeper disabled) |
| CLAIM-054 | REF TestSweeperMaterializesWithEffectiveAt; OPS expiry-lag metric |
| CLAIM-055 | Documentation (`examples/build-abby/` runner recipe); BOOT-04 evidence |
| DEP-001..003 | CT TestCT012AtomicBatchRejection; CT TestCT037DependencyEdits; CT TestCT033HiddenPrerequisite |
| DEP-004, DEP-007..008 | CT TestCT011PrerequisiteFailReopenComplete |
| DEP-005..006 | CT TestCT037DependencyEdits |
| DEP-009..010 | CT TestCT033HiddenPrerequisite |
| SEL-001..005 | CT TestCT030ClaimNextOrderAndFilters; CT TestCT007 (claim-next variant); REF TestClaimNextUnderBoardLock |
| SEL-006..008 | CT TestCT031ClaimNextWait; CT TestCT016 extended |
| BATCH-001..005 | CT TestCT012AtomicBatchRejection; CT TestBatchRefsAndOrder |
| BATCH-006 | REF plan-helper tests; BOOT-02 seed rerun evidence |
| BRIEF-001..005 | CT TestCT026BriefUpdateVsTerminal; CT TestBriefVersions |
| BRIEF-010..011 | CT TestCT034RootProtection; CT TestCT025 (author controls intact when conversion is refused) |
| BRIEF-020..022 | CT TestCT034RootProtection; CT TestCT017 extended (brief bodies scrubbed from replay and snapshots) |
| RUN-001..007 | REF runner tests with a fake subprocess (RUN-005 result handling: TestRunMapsUnknownEvidenceKinds, TestRunResultRejectedReleasesWithoutSettling, TestRunUnusableResultFileReleasesWithoutSettling, TestRunRejectedReleaseReasonIsValidUTF8, TestRunRejectedReleaseFailureStillExits13); OPS BOOT-04 drill (kill a claimed runner; replacement claims after lapse; old proof → `claim_lost`) |

## 21 Notes on the design baseline

Points where the design is silent, redundant, or in tension, and how this
profile resolves them. ADR references give the reasoning.

1. **Hidden dependency counts.** The ADR-0008 proposal reported "a count
   of unsatisfied hidden dependencies". AUTH-015 and design §4.2 forbid
   counts of inaccessible resources. DEP-009 drops the count and exposes
   only a derivable boolean.
2. **"Release or expire → ready or waiting".** `waiting` is unreachable:
   dependency edits are forbidden during a live claim and `completed` is
   terminal. TASK-033 says `ready`.
3. **Brief text in a discussion message.** If the update's message held
   the brief text, deleting that message would (by §6.1 redaction rules)
   erase accepted instructions through an ordinary message route, against
   §6.1's own rule. BRIEF-003 keeps the text only in the brief record.
4. **Renew route has `claim_id`, others do not** (§11.2). The proof names
   its claim, so the other routes do not need it; CLAIM-015 checks the
   match on renew.
5. **Claim ID vs execution ID.** §7.2 makes every claim a new execution,
   so they are 1:1 in v1. Both are kept: `execution_id` is the value to
   hand to external systems.
6. **Tie-breaker.** Creation positions are unique per board, so the task-ID
   tie-breaker of §7.3 never applies. It is kept for implementations that
   use another creation order.
7. **Brief update on unclaimed tasks.** §7.4 blocks every nonterminal task
   on a brief update, even `ready` or `waiting` ones without a claim. This
   profile keeps the rule as written (a second manager action is needed to
   unblock). It is safe but adds a step; the owner may want to relax it.
8. **Thread title.** The design does not say whether the thread title of a
   task thread is protected. BRIEF-010 protects it via the P1 route and
   moves title edits to `PATCH …/tasks/{id}` under manager rights.
9. **P1 amendments.** BRIEF-010 changes P1 AUTH-020 behavior for task
   threads, and TASK-024 adds `task_id` to the thread view. `protocol.md`
   should gain a cross-reference when P2 lands.
