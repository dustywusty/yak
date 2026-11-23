---
name: yak
description: 'Coordinate work with the Yak CLI. Use when tracking requests, recording follow-ups, checking outstanding asks, or claiming and completing board tasks.'
metadata:
  version: "1.0.0"
---

# Yak

## When To Use

Use Yak to record an actionable request, find out who owes an answer, or execute a board task. Start with the board and source material the user authorized. A board record or mention does not wake an agent. A claim identifies the holder of a lease. Worker activity requires independent evidence.

## Procedures

### Connect and read

Use a compatible `yak` CLI and an existing board. Each agent needs its own actor and a scoped credential file from the board operator. Set `YAK_URL` to the reachable endpoint, `YAK_BOARD` to the board ID, and `YAK_TOKEN_FILE` to the private file path. Keep credentials outside the skill, project and messages. A remote agent needs a route to the server. Loopback reaches only the local host.

```sh
yak server
yak me
yak permissions "$channel"
yak task list --channel "$channel"
yak task show "$task_id" --json
yak task brief "$task_id"
yak read "$thread_id"
```

Set `channel` to the authorized channel path. Take the task ID from the list and the thread ID from the task. Check the actor, credential scopes and board restrictions. Check discovery for `yak.tasks`, `yak.tasks.briefs`, and the capabilities required for the operation, such as `yak.tasks.claims` or `yak.tasks.dependencies`. A read-only credential can inspect and summarize work. Normal work uses the existing setup without admin access.

### Record or update an ask

1. Identify each distinct requested outcome. Create one task per actionable ask, even when several share a message. Record the requester separately from the Yak owner (initially the creator) and the assignee (the eligible executor). Assign only a known, authorized actor.
2. Preserve the original wording, source URL and message identity when available, project/context, desired outcome and next action. Record the request date separately from the deadline. Preserve any explicit due date, time and timezone exactly as supplied. Leave missing fields unknown. Never infer a deadline, priority, actor or approval. Share quotes and links only within the source's authorized audience. If the board audience is wider, use an approved reference or request a suitable channel.
3. Check existing tasks, including closed ones, and read candidate briefs and replies. Match by stable source reference **and item identity** (for example, message 42, checklist line A). A matching title alone is insufficient. A repeated delivery needs no write. Append a genuine update to the matching thread with its own source. A reply to a completed ask can contain a distinct new action. Link that new task to the original.
4. For a new ask, prepare the brief as plain text, then create the task. For an update, reply to its thread. Use one stable idempotency key per distinct write and reuse it only for an identical retry. Reconcile board history as well, because server key retention expires.

```sh
yak task create "$channel" --title "$title" --idempotency-key "$create_key" < "$brief_file"
yak reply "$thread_id" --idempotency-key "$update_key" < "$update_file"
```

Add `--assignee "$actor_id"` to creation when assignment is known and authorized. An unassigned ready task is claimable by eligible actors. If a change alters the executable brief, an authorized manager can version it with `yak task brief "$task_id" --set` and the updated brief on stdin. Read the current task revision first and pass it with `--if-revision`. This change blocks the task and revokes an active claim. Review the new brief before unblocking it.

### Track a question that blocks work

Create or reuse a separate question task linked to the original task ID. Name the person whose answer is needed, the exact question, and the responsible relay. Assign the question to that person only with a configured actor and authorization. Otherwise use a known, authorized relay and name the respondent in the brief. Recording a question gives no permission to contact anyone by email, chat or another service.

Keep the question visibly blocked while awaiting the answer. Use the current question-task revision for this manager command.

```sh
yak task block "$question_id" --reason "$waiting_reason" --if-revision "$revision"
```

Link the question in the original thread. If execution depends on the answer, have a manager add the question as a prerequisite or explicitly block the original with a reason. For `yak task deps`, read the task immediately before the edit. Supply **all** retained readable prerequisite IDs plus the question ID and the current `--if-revision`. This command replaces the readable list. On conflict, reread and reconcile. If permission is missing, record the needed change for the owner and report the actual state.

Leave the unanswered question unclaimed. When an authorized source supplies the answer, append its exact wording and source to the question thread. A manager can unblock the question at the current revision. Its eligible executor can then complete it with the answer as evidence. Check the original task again. Yak evaluates dependencies after completion. A manual block needs a manager to check the reason and unblock it at the current revision. Continue the original work only when eligible.

### Execute and review a task

Select authorized ready work. Read its current brief and thread, including updates. Prepare a worker command that performs the requested work and checks its outcome. Start it under the managed runner:

```sh
yak run "$task_id" --expect-brief-revision "$brief_revision" --json -- "$worker_command"
```

Set `worker_command` to an executable for this task. The runner acquires the claim, renews the lease during execution and settles it. Use the brief's revision for the claim guard. Keep work inside the supervised child process group so lease loss can stop it. The child receives `YAK_BRIEF_FILE` and `YAK_RESULT_FILE`. Write a result with a summary and evidence such as a message ID, artifact reference or URL. Keep evidence accessible to the intended readers.

Exit zero only after checking that the requested outcome was met. A nonzero child exit records failure. Sending the question, starting the work or acknowledging the request alone leaves the outcome open. For planned interruption, signal the runner so it stops the child and attempts release. Review the runner report, then read the task and its recorded outcome.

### Summarize open work

List tasks in `waiting`, `ready`, `claimed`, and `blocked` states. Read the brief and replies for each relevant ask. Report task ID, requester, assignee or relay, next action, blocker and any explicit deadline with its supplied timezone. Brief fields and optional labels for source or due dates are team conventions. Yak has no typed due date or overdue state. Mark an item “overdue (inferred)” only when the supplied deadline is unambiguous and has passed at the stated report time.

## Decision Tree

- New request or follow-up → match the source and item before creating a task.
- Missing answer → track the question and its effect on the original work.
- Authorized ready task → run the worker under a lease and inspect the outcome.
- Status request → read and summarize visible records.

## Error Handling

- **Unexpected identity or access denied.** Stop writes. Check endpoint, board, actor and channel permissions with the operator.
- **Outage or lost claim.** Stop task side effects before the last confirmed lease can expire. Keep local notes. On reconnect, reread the task, brief and history before retrying. Resume only under a valid fresh claim. Never settle with expired proof.
- **Result rejected.** Inspect the runner report and current state. Correct the result, check existing effects and acquire a fresh claim if execution must resume. An uncertain release or lost response requires a fresh read before further writes.
