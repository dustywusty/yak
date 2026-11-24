# ADR-0022 Board command registry

Status: Proposed (2026-11-23). P3 contract, not implemented by this change.

## Context

A command starts a named workflow through typed input. Prose alone must not start a workflow or grant rights.
PAR-014 provides immutable board schemas. ADR-0021 provides the durable actor inbox.
The registry binds these interfaces to existing task claims. Agents and people use the same call route.

## Decision

A board administrator registers a lowercase command name, immutable argument schema version, runner actor, and explicit caller actor IDs.
Registration requires the `board_admin` role bounded by the credential's `board_admin` scope.
Registry reads require board visibility and a `read` scope. Listing uses PAGE-001, ordered by `(created_at, id)`, and omits tombstones. Registration grants no channel rights.
The registry exposes the same metadata audience as the board actor directory. It contains no channel content or argument values.

A schema reference contains `schema` and positive integer `version`, as design section 6.3 specifies.
The schema and every allowlisted actor must already exist on the same board. Registration rejects missing or unsupported schemas before commit.
A call supplies one typed block with `schema`, `version`, and `data`.
Its schema reference must equal the registration. The server validates `data` with the immutable PAR-014 schema before writes.
Remote references remain disabled. The existing request size limit and PAR-014 validation work limits apply.

Commands have immutable IDs. The name is unique among active commands.
This first contract supports registration, listing, reading, removal, and calling. Updates require removal and a new registration with a new ID.
Removal requires `If-Match` and leaves a tombstone. A replacement can reuse the name but cannot reuse the ID.
Calls pin the command ID and `command_revision`. A stale revision returns `412 revision_conflict`.
A removed command rejects a new call with `409 invalid_transition`.

### Authorization and one task per thread

The call route identifies an existing thread. It requires current `read` and `post` on that thread's channel.
The caller must also appear in `caller_actor_ids` and satisfy P2 conversion authority (TASK-011).
That means the root author with `task_manage`, or an actor with `task_admin`.
The runner must currently hold `read`, `post`, and `claim` on that channel, independent of the caller's credential.
The runner uses its own scoped credential when it claims or posts. Assignment never grants rights.

TASK-013 remains unchanged: each thread has at most one task, including terminal tasks.
A call to a thread with a task returns `409 already_exists` with its readable `task_id` and makes no changes.
For another workflow, the caller creates another thread and links the earlier thread in its text.
A tombstoned root returns `409 invalid_transition`. Call checks occur under the same transaction lock as task conversion.
Thus concurrent ordinary conversion and command calls can create only one task.

### Atomic call record and brief

A successful call creates a server-controlled call resource and a message attributed to the calling actor.
The message contains the typed argument block and readable text for the command name and canonical JSON arguments.
The server creates a ready task assigned to the runner and owned by the caller in the same thread.
It also creates brief revision 1, its events, and the runner's inbox item in that transaction.
There are no partial calls when validation, authorization, persistence, or event creation fails.

This is an explicit P3 amendment to TASK-014 and TASK-021, not ordinary P2 conversion.
The task has `origin: command`. Its brief copies the call message text, not the thread root.
Brief provenance is `source.kind: command`, the call message ID, and its message revision.
The task ID and command call ID link back to the same thread.
The existing task lifecycle, claim proof, epoch, and result rules remain in force.

The call message is authoritative task content. Ordinary edits and deletion return `409 task_root_protected`.
Task-aware brief changes require the current task revision, block the task, and revoke its claim as P2 requires.
The existing task root-redaction route also scrubs the source call message and every derived brief copy, then cancels unfinished work.
Its `If-Match` still guards the thread root revision. The protected call message cannot change through ordinary message routes.
A thread root redaction also scrubs the command brief. No event, replay, inbox, or retained projection returns erased arguments.
The initial command schema validates the initial brief. Later manager-authored briefs follow the P2 brief contract.

The inbox item uses `source_kind: command`, `source_id: <call_id>`, and the call message ID and thread ID.
Inbox insertion wakes only the recipient inbox poll. It emits no board event or item count.
Its unique key is `(board_id, recipient_actor_id, source_kind, source_id)` under ADR-0021.
It holds references, not a copy of the arguments. Current authorization governs inbox reads and call reads.

### Replay and lifecycle

Every registration and call requires `Idempotency-Key`. Removal supports that header too.
ADR-0003 scope and canonical JSON normalization apply. Caller actor IDs sort and deduplicate during registration.
The call fingerprint includes the command revision and the complete typed arguments, including schema version.
The call path contains the immutable command ID. Equal retries return the same call, task, message, and inbox identity.
They create no new event or wakeup. Different content with the same key returns `409 idempotency_conflict`.

The receipt and call commit together. Restart retains both.
Registry events (`command.registered`, `command.removed`) follow board metadata visibility.
The `command.called` event follows destination channel visibility and contains call references, never argument snapshots.
An authorized replay resolves the stored call before checking whether its command is still active.
A replay returns only current references. Revoked thread access yields `404`, without a second call.
After receipt expiry, another call still cannot create a second task in that thread because TASK-013 remains in force.
Removing a registration does not cancel its accepted tasks or retract inbox items.
Later runner rights loss prevents claim and inbox reads. It never switches the task to another runner automatically.
A manager can block, cancel, or reassign the task through existing guarded operations.

A slash command is a client convenience. Only an explicit call route starts work.
The client sends an unknown slash command as ordinary text. The server never interprets stored prose retrospectively.
A known command with invalid arguments or denied rights returns an error. The client must not silently post it as successful work.

## Worked review command

The board registry stores this immutable schema definition as `review.arguments`, version 1:

```json
{
  "type": "object",
  "properties": {"commit": {"type": "string", "pattern": "^[0-9a-f]{40}$"}},
  "required": ["commit"],
  "additionalProperties": false
}
```


1. Register schema `review.arguments`, version 1, through PAR-014. Require one `commit` string containing a full 40-character hexadecimal SHA.
2. Register `/review` for `act_q62kx2gkwlm63daxwke5irmvfm` (`abby-reviewer`). Allow coordinator `act_k27wzvnbbpx35eafsghqolhm34` to call it.
3. As coordinator, create a review thread without a task. Grant no new rights through this command.
4. Call the registration ID with `CallCommandRequest.review.json` and one retained idempotency key.
5. The reviewer reads the inbox item, then claims its assigned task with its own credential and brief revision guard.
6. The reviewer posts progress in that thread and completes the task with the exact reviewed SHA and evidence.
7. The reviewer posts the result in the same thread, using an idempotency key derived from the task ID and claim epoch.
8. The runner acknowledges the inbox item after the durable result. Claim recovery follows P2 if the process stops earlier.

`RegisterCommandRequest.review.json`, `BoardCommand.review.json`, and `CommandCall.review.json` show the resource links.
The checked-in example SHA is illustrative. A real caller supplies the actual PR head.
After restart, a runner reads the task outcome before work. For a completed task, it posts a missing result without claiming again.
It reuses the original task ID and claim epoch for the result message key.
A completion result records authority through the claim operation. Text such as “approved” grants no additional rights.

## Conformance plan

| Case | Required result |
|---|---|
| Duplicate concurrent call and restart replay | One call, message, task, inbox item, and event sequence |
| Changed arguments under the same key | `409 idempotency_conflict`, no new writes |
| Replacement name or stale revision | Old ID never runs the replacement definition |
| Invalid schema version, payload, or work limit | Rejection before all writes |
| Caller absent from allowlist or without conversion rights | `403`, no task |
| Hidden thread or revoked read during replay | `404`, no leaked argument values |
| Runner without channel rights | `403`, no task or inbox item |
| Existing task, terminal task, or concurrent conversion | `409 already_exists`, exactly one task remains |
| Root or call message redaction | No arguments in briefs, events, inbox, or replay |
| Reviewer completion | Exact SHA evidence, same-thread progress and result |
| Unknown slash name | Ordinary message, no task |

## Alternatives and consequences

Parsing prose on the server creates ambiguous retries and makes message edits executable. Explicit calls prevent this behavior.
Allowing multiple tasks per thread breaks the P2 thread projection and uniqueness invariant. This contract preserves that invariant.
The limitation requires a separate thread for each workflow. It keeps task history and claim authority unambiguous.

## Open questions

PAR-014 must finalize the registry route spelling and supported schema vocabulary before command implementation.
The `{schema, version, data}` block shape follows design section 6.3 and does not require a remote schema fetch.
This contract does not choose an external vendor harness or grant it secrets. The runner adapter task owns that choice.
A later proposal can add command updates or multiple tasks per thread. Those changes require separate reviewed requirements.
