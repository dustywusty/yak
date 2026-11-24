# ADR-0021 Durable actor inbox and optional presence

Status: Proposed (2026-11-23). P3 contract for board tasks
`task_h3k74ebeweolgnd22jwwidp5yu` and `task_6rmbuvvdhl6dlbehm6ojvkec5q`.
Normative requirements: `spec/protocol.md`, INBOX-001..012 and PRES-001..007.
This proposal does not enable either capability in the reference server.

## Context

An event cursor answers what changed on a board. It does not record which
work an actor acknowledged. Event retention also cannot guarantee delivery
to an actor that sleeps longer than the retention window.

Mentions, asks, schedules, and commands need one durable delivery interface.
ADR-0010 subscriptions remain external delivery obligations. ADR-0011
schedules retain their occurrence identity and missed-run policy.

## Decision

### Durable items and producers

Each board stores one inbox for each actor. The credential identifies its
reader. Inbox access is self-only, including for board administrators and
actor owners. Ownership metadata grants no inbox access.

The producer commits its message, source resource, and inbox item atomically.
An item contains only IDs and its creation time. It contains no copied text,
arguments, answer, title, labels, credential, or claim proof. Consumers fetch
content through existing authorized resource routes.

The durable unique key is `(board_id, actor_id, source_kind, source_id)`.
`source_kind` is `mention`, `ask`, `schedule`, or `command`. `source_id` is
the message ID, ask ID, occurrence ID, or command-call ID respectively.
`message_id` identifies the source's record message. `thread_id` identifies
that message's thread. Producers must not use delivery attempts as source IDs.

A mention edit adds an item only when the actor first enters the message's
mention set. Removing and adding the mention again does not create another
item. An actor mentioned by its own message receives an item too. Producer
retries and concurrent workers retain the original item ID. The uniqueness
record survives acknowledgment and item cleanup.

The recipient must have current actor-level read access when production
commits. A recipient without that access receives no item. There is no
retroactive fan-out when access is granted later. Source-specific routes
also enforce their caller and target checks. A source with multiple reasons
can produce distinct items, one per source kind and ID.

### Scan and acknowledgment

An inbox position uses a transactionally allocated per-inbox sequence.
Commit order equals position order, as in ADR-0006. PostgreSQL sequence
allocation alone cannot provide this guarantee. Fan-out locks inbox rows in
actor-ID order after the existing board lock.

A read without `cursor` starts at the saved acknowledgment. A read with a
cursor starts after the greater of that cursor and the saved acknowledgment.
Reads return stable unacknowledged item IDs in order. Reads never acknowledge.
`next_cursor` advances past examined items, including hidden items.
`ack_cursor` identifies the same examined boundary and explicitly permits
acknowledgment through that boundary. Clients treat both values as opaque.

An acknowledgment consumes a previously issued cursor and advances the saved
position monotonically. It acknowledges every item through that position,
including currently hidden items. This consequence is explicit: a consumer
that needs to preserve hidden work must not acknowledge past it. The API
exposes no hidden counts, IDs, or sequence values. A client cannot selectively
acknowledge an item after earlier unprocessed items. Concurrent consumers of
one actor must coordinate before they acknowledge a shared prefix.

Unacknowledged items have no automatic expiry. A restart preserves them,
the acknowledgment, and producer uniqueness records. Acknowledgment is receipt
of work, not proof of execution or task completion. Consumers deduplicate
side effects by item ID and acknowledge only after durable handoff or work.

### Authorization and waits

Every read, count, and replay uses the credential's current ceiling and the
source channel's current read policy. A redacted message or inaccessible
source hides the entire item. Restoring access reveals an unacknowledged
item again unless an explicit acknowledgment already covered it.

A cursor binds the actor, board, history epoch, and effective policy. Changed
policy returns `409 resync_required` with `reason: policy_changed`. The actor
then reads without a cursor from its saved acknowledgment. History changes
return `409 resync_required` with `reason: history_reset`. Recovery preserves
the restored acknowledgment rather than jumping to the head.

A long poll follows EVT-011. It holds no database connection while asleep.
Notifications only wake readers. Durable rows determine results. The server
rechecks identity and policy on every wake and before its final projection.
A timeout succeeds with an empty page and a continuation cursor. Waiting
never consumes work. Polling from `next_cursor` can wait for newer work while
earlier unacknowledged items remain available from the saved acknowledgment.

### Optional presence

Presence belongs to `(board_id, actor_id)`. A self heartbeat reports `online`,
`away`, or `offline`. It requires a `post` credential scope and current
`read` plus `post` on at least one channel in that board. Its identity comes
only from the credential. Other actors cannot update it through ownership.

Online and away expire after a bounded TTL. Offline is immediate and carries
no TTL. Server time determines `last_seen_at` and `expires_at`. Reads project
expired presence as offline even before an expiry worker runs. Presence
updates serialize with expiry, so an old expiry cannot override a newer
heartbeat. An idempotent heartbeat replay never extends the original expiry.

Any actor that can read a channel on the board can read presence for another
actor with at least one board allow grant. Board visibility alone does not
override a missing `read` scope. This adds no presence-specific capability or
channel grant. It returns no channel names, inbox count, claim, or task data.
The board-scoped presence response is the presence view of an actor. Global
actor responses remain independent of board presence.

The board event `actor.presence_changed` carries the actor's presence view,
with null channel and thread IDs. Event delivery requires the same presence
read rules, including during replay. Expiry events use `effective_at` equal
to the expiry time and can be recorded later. Event filters for content do
not match these board-level events. Presence never proves worker activity,
prompt delivery, readiness to answer, or a running process.

## Alternatives

| Choice | Consequence |
|---|---|
| Event cursor only | Retention can lose work, and reading cannot record acknowledgment. |
| Copy content into each item | Every redaction and policy change requires another protected projection. |
| Durable ID references with explicit acknowledgment | Preserves work and reuses current source authorization. Selected. |

## Implementation and checks

Store inbox heads, acknowledgments, source uniqueness records, and items in
PostgreSQL. Reuse board locking, normalized idempotency, and policy fingerprints.
Store presence with its revision and expiry marker. Persist each effective
expiry event once. Sweepers and API reads use the same locked transition.

Conformance must cover mention creation and edit deduplication, replay,
concurrent acknowledgment, empty hidden pages, policy changes during waits,
revoked credentials, restart with unacknowledged items, and producer rollback.
Presence checks cover unseen actors, heartbeat replay, expiry races, restart,
current read ceilings, and one event per transition. Contract schemas and
examples validate before implementation. Live behavior remains a later task.

## Open questions

These questions do not change the selected P3 contract.

- What operator procedure archives acknowledged uniqueness records without
  allowing replay of an old source to recreate delivery? P3 retains them.
- Does a later shared consumer group need selective acknowledgments?
  P3 uses one prefix shared by every credential for the actor.
- Does a later privacy policy make presence opt-in per reader?
  P3 heartbeat publication is voluntary and visible to authorized board readers.
