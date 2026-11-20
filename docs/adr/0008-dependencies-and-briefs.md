# ADR-0008 Dependencies, brief versions, and task-root protection

Status: Accepted pending owner review (2026-11-19). Implements design §3.1,
§6.1, §7.3, §7.4. Normative text: `spec/p2-tasks.md` §9 (DEP-001..010),
§11 (BATCH), §12 (BRIEF-001..022).

## Context

Tasks can require other tasks. Some prerequisites may be in channels the
reader cannot see. A task's instructions (its brief) must not change under
a running worker, and must not be changed or erased through ordinary
message routes. Redaction rules from P1 (RED-001) still apply to brief
text.

## Decision: dependencies

1. **Same-board DAG.** An edge (task → prerequisite) joins two tasks of one
   board. The storage uses composite foreign keys on `(board_id, task_id)`
   so a cross-board edge cannot exist. Self-edges and cycles are refused
   with `409 dependency_cycle`.
2. **Cycle check under the board lock, over the whole board graph.** The
   check runs after the board lock is held, so two concurrent edits cannot
   each pass and together form a cycle. It includes unreadable tasks
   (otherwise a hidden edge could close a cycle). The error lists only
   readable task IDs. A caller may learn that some path exists through
   tasks it cannot see; this cannot be avoided without allowing cycles.
3. **Who may link.** A manager of the dependent task who can also read each
   prerequisite being added. An unreadable prerequisite in a request is
   `404 not_found`, the same as an absent one.
4. **`PUT` replaces only the readable part.** Edges to prerequisites the
   caller cannot read are kept. A caller cannot remove what it cannot see,
   and cannot learn of it by the result.
5. **Satisfaction.** Only `completed` satisfies. `failed` and `canceled`
   prerequisites leave dependents `waiting`; nothing cascades. A manager
   can reopen the prerequisite, or edit the dependent's set.
6. **Edits.** Refused during an active claim (`409 task_claim_active`) and
   on terminal tasks (`409 invalid_transition`). Allowed on `waiting`,
   `ready`, `blocked`. `ready` ↔ `waiting` is re-evaluated; `blocked`
   stays blocked.
7. **Activation is synchronous.** Completing a task moves every `waiting`
   dependent whose prerequisites are now all completed to `ready` in the
   same transaction, with `task.became_ready` events. There is no
   activation worker, so claim-next sees new work as soon as the
   completion commits.
8. **Visibility.** Views list `{task_id, state}` only for readable
   prerequisites. There is **no count** of unreadable ones. The view has
   `unreadable_dependencies_pending`, true exactly when the task is
   `waiting` and every readable prerequisite is completed. That bit is
   derivable from data the reader already sees (`state` plus visible
   prerequisites), so it leaks nothing new and saves agents from guessing
   why a task waits. Event payloads are filtered the same way at read
   time.

## Decision: briefs

1. **Immutable versions.** A brief version has `revision`, `text`,
   `source`, `created_by_actor_id`, `created_at`, `reason`, `redacted`.
   Revision 1 is copied from the root message text when the task is
   created or converted, with the root message ID and revision as
   provenance.
2. **Task view carries `brief_revision`, not text.** Events, task views,
   and snapshots never contain brief text. This keeps redaction to two
   places (brief rows and the root message) plus claim responses, which
   are regenerated on replay.
3. **Task-aware update** (`POST …/tasks/{id}/briefs`, manager + `post`,
   `If-Match`). In one transaction: insert revision n+1; append a notice
   reply ("brief updated to revision n+1: <reason>") authored by the caller
   with the server label `abby.task.brief_revision`; revoke any active
   claim; set or keep `blocked` (`cause: brief_updated`). A manager must
   unblock after review.
4. **The notice does not carry the brief text.** If it did, the notice's
   author could delete it with the P1 route, and by RED-001 the server
   would have to scrub the brief too — an ordinary message route would
   then erase accepted instructions. With the text only in the brief row,
   deleting the notice changes nothing about the task.
5. **Terminal guard.** `completed` refuses updates. `failed` and `canceled`
   refuse with a hint to reopen first. Under the board lock, update and
   complete/cancel serialize: whichever commits first wins, and the other
   gets `invalid_transition` or `claim_lost` (CT-026).
6. **Root protection.** On a task thread, P1 `PATCH`/`DELETE` of the root
   message and `PATCH` of the thread return `409 task_root_protected` for
   everyone, including board administrators. Replies, reactions, and
   non-`abby.` root labels are unaffected. Title changes go through
   `PATCH …/tasks/{id}` (manager, `If-Match`), which emits
   `thread.updated` and `task.updated`.
7. **Root redaction** (`POST …/tasks/{id}/root-redaction`) needs both a
   manager and the P1 rights to delete the root (author with `read`+`post`,
   or `board_admin` with a reason, audited). One transaction: tombstone and
   scrub the root (P1 `scrubMessage`), null the text of every brief
   version, set `root_redacted`; if nonterminal, revoke the claim and
   cancel (`cause: root_redacted`); if terminal, keep state and outcome
   and emit `task.brief_redacted`.
8. **No reopen after root redaction.** A task whose instructions are gone
   cannot be worked again. A follow-up task with new instructions is the
   path. This follows the owner's "follow-up tasks for changes" rule.

## Alternatives considered

| Alternative | Why not |
|---|---|
| Report a count of unreadable prerequisites (earlier proposal) | Contradicts AUTH-015 and design §4.2 (no counts of inaccessible resources). |
| Hide `waiting` behind a generic state for hidden prerequisites | State would lie; claim-next and readers would disagree. |
| Allow cross-board dependencies | Needs cross-board ordering and authorization; the design defers it. |
| Cascade cancel/fail to dependents | The design says failed prerequisites stay unsatisfied; cascades surprise managers and lose work. |
| Activation worker | Adds lag; design §12.3 requires activation not to depend on a worker. |
| Edit the root message for a new brief | Changes the author's message and its revision history; conflicts with "conversion does not change authorship". |
| Brief update only revokes the claim, task returns to `ready` | Another worker could claim the new brief before a manager reviewed it; the design requires `blocked` until review. |
| Allow brief updates on `ready`/`waiting` without blocking | Simpler, but the design text says "puts or keeps blocked" for every nonterminal task. Flagged to the owner in `spec/p2-tasks.md` §21. |
| Let board administrators bypass root protection with P1 DELETE | Would cancel no work and leave a task with erased instructions still claimable. The compound route does both atomically. |

## Examples

Hidden prerequisite. Task A (`server`) depends on B (`server`) and H
(`ops/private`). Reader R can read `server` only.

| Situation | R sees on A |
|---|---|
| B completed, H ready | `state: waiting`, `dependencies: [{B, completed}]`, `unreadable_dependencies_pending: true` |
| B ready, H completed | `state: waiting`, `dependencies: [{B, ready}]`, `unreadable_dependencies_pending: false` |
| H completes after B | Event `task.became_ready` on A, `prerequisite_task_id` omitted for R |

Brief update during work.

1. Worker W claims T (epoch 3, `brief_revision` 1).
2. Owner posts brief 2 with `If-Match: "7"`. Same transaction: notice reply
   (pos 90, `message.created`), `task.brief_updated` (pos 91, rev 8,
   `revoked_claim` = W's claim, `state_before: claimed`),
   `thread.view_invalidated` (pos 92).
3. W calls complete with its proof → `409 claim_lost` (`state: blocked`,
   `epoch: 3`).
4. Owner unblocks → `ready`. W (or another worker) claims with
   `expect_brief_revision: 2` → epoch 4.

## Consequences

- Every brief change is visible in the thread and in the event stream;
  running work never silently follows changed instructions.
- Graph checks cost O(edges reachable) under the board lock. With
  `max_dependencies_per_task` = 100 and boards of thousands of tasks this
  is small; to be measured with ADR-014.
- Completion of a task with many dependents writes one row and two events
  per newly ready dependent in that transaction.
