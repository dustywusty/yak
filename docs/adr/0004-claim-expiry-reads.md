# ADR-0004 Claim expiry, effective reads, and claim replay

Status: Accepted (owner approved the draft 2026-11-20). Implements design §5.1
(claim replay), §7.2, §8 (effective time). Normative text: `spec/p2-tasks.md`
§7 (CLAIM-001..055), TASK-025, TASK-031, TASK-082, TASK-092. Amended
2026-11-20 by §4 (renewal semantics).

## Context

A claim has a lease. The lease can end while no process is running: the
server may be down, or the sweeper may be late. The owner requires that
correctness does not depend on the sweeper, that event order is commit
order, and that an idempotent replay never extends an expired claim.

Three questions follow:

1. What does a read show between `expires_at` and the moment the server
   records the expiry?
2. When and how is the expiry recorded, and what times does its event
   carry?
3. What does a replay of a claim response return, given that the proof is
   a secret shown once?

## Decision

### 1. Reads return the effective view

A read computes the task as it is at server time `now`. If the recorded
state is `claimed` and `expires_at ≤ now`, the read shows:

| Field | Value |
|---|---|
| `state`, `yak.task.state` | `ready` |
| `current_claim` | `null` |
| `last_claim` | that claim, `status: "expired"`, `ended_at = expires_at`, `end_recorded_at: null` |
| `revision` | recorded revision + 1 |
| `updated_at`, `state_changed_at` | `expires_at` |

The projection is deterministic. It depends only on the recorded row and
on `expires_at`, not on when the read happens (as long as `now ≥
expires_at`). It is also exactly what materialization will write (§2), so
a client never sees two different views with the same revision.

`ready` is always correct after a lapse: dependency edits are refused
during an active claim and `completed` is terminal, so the prerequisites
that made the task claimable still hold (TASK-033).

All read surfaces use it: `GET …/tasks/{id}`, `GET …/tasks` (including the
`state` filter), `label=yak.task.state:…` on thread lists, snapshot task
items, claim-next candidate selection, and replayed responses.

### 2. Materialization: first writer under the board lock

The lapse is recorded by whichever comes first:

- any task operation on that task (claim, claim-next selecting it, block,
  cancel, assignee, dependencies, brief update, PATCH, root redaction), or
- the expiry sweeper.

Both run inside the normal board transaction (idempotency lock →
`board_heads` row → task row). They write the CLAIM-051 view and emit:

1. `task.claim_expired` — `resource.revision` = recorded revision + 1,
   `effective_at = expires_at`, `recorded_at` = materialization time;
2. `thread.view_invalidated` (`reason: "task_state_changed"`).

If an operation materializes, these two events come first in its
transaction, then the operation's own events. Position order is still
commit order. `effective_at` is therefore not monotonic in the stream;
consumers must not assume it is.

A rejected request commits nothing, including materialization. The
projection makes this safe: a later reader still sees the same effective
view. This keeps "a failed request has no side effect" true for every
error code.

The sweeper is housekeeping only. It scans `tasks` with `state = 'claimed'
AND claim_expires_at <= now()` per board, takes the board lock, re-checks,
and materializes up to a bounded batch. Target lag: 30 s (CLAIM-054). It
also destroys the claim's encrypted replay material (§3).

### 3. Claim replay: encrypted material, never an extension

The proof has the form `acp1.<claim_id>.<secret>`; the secret is 256 bits
from a CSPRNG. The server stores `SHA-256(secret)` as the verifier
(high-entropy secret, as for credentials in ADR-0015).

For replay, the server also stores the secret encrypted with AES-256-GCM:

- Key: from operator configuration (`YAK_CLAIM_REPLAY_KEYS`, a keyring of
  `key_id:base64key`, first entry used to encrypt). Never stored in the
  database. The reference server also accepts `YAK_CLAIM_PROOF_KEY`
  (one base64 key, ID `k1`) when the keyring variable is unset.
- Associated data: `board_id ‖ task_id ‖ claim_id ‖ actor_id ‖ epoch`, so
  a ciphertext copied to another row does not decrypt.
- Stored columns: `proof_ciphertext`, `proof_key_id`.
- Lifetime: set to NULL in the same transaction that ends the claim
  (release, complete, fail, revoke, materialized lapse).

A replay of a claim or claim-next (same actor, key, and fingerprint)
returns the original claim view. It returns the proof only if
`claim_proof_replay` is enabled, the claim is active at replay time, and
the caller still holds `read` and `claim`. Otherwise `proof: null,
proof_unavailable: true`. A replay never writes: no new claim, no event,
no change to `expires_at`.

If no key is configured, the server stores no ciphertext and advertises
no `yak.tasks.claim_proof_replay` capability. Replays then always return
`proof_unavailable: true`; the lost claim lapses within one lease (≤ 300 s)
or a manager blocks the task.

A replay of renew returns the current claim view without renewing. A
replay of complete, fail, or release returns the current task view with
`Idempotent-Replayed: true`, even though the claim has ended — that is
how a client that lost a completion response learns it succeeded.

### 4. Renewal changes the task revision and keeps the lease length

Added 2026-11-20 (tasks `task_j2cnk7xuvfpropcjthn6i5sehi` and
`task_twdcj46fvqln5vscwkx6rzqh5y`, found by the viewer and by the BOOT-04
live exercise).

**Task revision.** The first version of CLAIM-023 said a renewal changes
the claim revision but not the task revision. But the task view shows
`current_claim.expires_at`, so after a renewal the same task revision had
two different views. TASK-092 tells clients to apply task views by
revision. A client that keeps only newer revisions dropped every renewal
and showed an old expiry.

Decision: a renewal is a task change. It increments the task revision and
sets `updated_at` (CLAIM-023). It does not change the epoch, execution, or
state. `task.claim_renewed` carries the task resource at the new
revision, like every other task event (TASK-082). The claim revision is
still in `payload.claim.revision`.

Rationale:

- One ordering key. Clients keep the rule "apply a task view if its
  revision is higher". They do not need a pair of keys or a special case
  for one event type.
- The ETag stays honest. `ETag` is the task revision, and a different view
  now always has a different ETag. Caches and conditional GETs work.
- The lapse projection stays deterministic. The effective view after a
  lapse is still "recorded revision + 1" (§1), because a renewal writes
  its revision to the row.
- Cost: an `If-Match` request from a manager can now fail with
  `412 revision_conflict` because the claimant renewed in between (at most
  every lease/3). Assignee and dependency changes are refused during an
  active claim anyway (TASK-053, DEP-006). For the others (PATCH, brief
  update, block or cancel with `If-Match`) a 412 is a safe, normal retry:
  read the task again and send the new revision.

**Lease length.** The first version of CLAIM-020 gave renew the same
default as claim: `claim_lease_default_seconds`. A worker that claimed
with a short lease to bound takeover time got the long default on every
renewal that left out `lease_seconds`. Decision: on renew, the default is
the claim's current `lease_seconds`. An explicit value still changes it,
inside the advertised bounds. The idempotency fingerprint records the body
as sent, so `{}` and `{"lease_seconds": n}` under one key are different
requests.

## Alternatives considered

| Alternative | Why not |
|---|---|
| Show recorded state `claimed` with `current_claim.active: false` (the earlier proposal) | Every client must compute effective state. `yak.task.state` filters and the `state` list filter would return the wrong set. Claim-next and lists would disagree. |
| Materialize on read (a GET writes and emits) | GETs would need the board lock and would emit events; read load would become write load; a read-only credential would cause writes. |
| Depend on the sweeper | Forbidden by the owner. A late sweeper would block new claims. |
| Use a database timer or `pg_cron` | Not portable; still a sweeper. |
| Derive the proof as `HMAC(key, claim_id)` (no stored ciphertext) | Anyone with the key and a claim ID (which every reader sees in events) can forge proofs. Encryption needs the key and a database copy. |
| No proof replay (like credentials, ADR-0003) | Simpler, but a lost claim response strands the task for a whole lease and fails the intent of CT-009. Kept as the fallback when no key is configured. |
| Replay re-renews an active claim | Violates "replay never extends a lease". |
| Keep the task revision on renewal; order task views by (task revision, claim revision) | Every client needs a two-part key and must know that `current_claim` can be null or change identity. The ETag would not change when the view changes. Rejected for the one-key rule (§4). |
| Drop `current_claim.expires_at` from the task view | Readers need the expiry to see when a claim can be taken over. Rejected. |
| Renew without `lease_seconds` uses the server default | A short lease silently became the long default after the first renewal (BOOT-04). Rejected (§4). |
| Materialize as part of a rejected request | Makes some failures commit events; complicates IDEM-004 and the "rejected means nothing changed" rule. |

## Examples

Lease of 120 s, claim at 10:00:00, server down 10:01:00–10:10:00, sweeper
runs at 10:10:30.

| Time | Action | Result |
|---|---|---|
| 10:00:00 | A claims (pos 41) | rev 2, `claimed`, `expires_at` 10:02:00 |
| 10:10:05 | Reader GETs task | rev 3, `ready`, `current_claim: null`, `last_claim.status: expired`, `end_recorded_at: null` |
| 10:10:06 | A renews with its proof | `409 claim_lost` (state `ready`, epoch 1); nothing committed |
| 10:10:07 | B claims | pos 42 `task.claim_expired` (rev 3, `effective_at` 10:02:00, `recorded_at` 10:10:07); pos 43 `thread.view_invalidated`; pos 44 `task.claimed` (rev 4, epoch 2); pos 45 `thread.view_invalidated` |
| 10:10:30 | Sweeper | Nothing to do for this task |
| 10:10:40 | A replays its original claim key | Claim `claim_A`, `status: expired`, `proof: null`, `proof_unavailable: true`; no event |

If B had not claimed, the sweeper at 10:10:30 would have written the same
rev 3 and emitted the same two events.

## Consequences

- Readers, lists, and snapshots agree with what will be recorded.
- Rejected operations stay side-effect free.
- Operators who want proof replay must manage one more key; rotation
  works by adding a new first key and keeping old ones until all active
  claims (≤ 300 s) have ended.
- Logs and traces must drop the `Yak-Claim-Proof` header (design §13).
- ADR-0005's deferred "expiry projection for tasks" is resolved: snapshot
  task items use the effective view at capture time (TASK-092).
