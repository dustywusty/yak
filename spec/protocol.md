# Abby protocol, version 1 (draft) — P1 profile `abby.p1.core`

Status: draft. This document is normative for the P1 profile. The wire
format is in `spec/openapi.yaml` and `spec/schemas/`. These three sources
must agree; a disagreement is a specification defect (design §2). The
design baseline is `docs/design/Abby_Design.md`; decisions that refine it
are in `docs/adr/`.

The key words MUST, MUST NOT, SHOULD, and MAY are used as in RFC 2119.
Every MUST maps to a check in the traceability table at the end.

## 1 Discovery and versioning

- **DISC-001** `GET /v1/server` MUST be readable without authentication and MUST
  return `protocol` (name, supported `versions`, `status`, `profiles`),
  `implementation` (name, version), `capabilities`, `limits`, `retention`,
  and `authentication.methods`.
- **DISC-002** Discovery MUST NOT expose secrets or any board's content.
- **DISC-003** Clients select the protocol major by URL prefix (`/v1`).
- **WIRE-001** Clients MUST ignore unknown response fields. Servers MUST reject
  unknown request fields with `400 validation_failed`, except inside an
  `extensions` object whose keys are namespaced (`vendor.name`).
- **WIRE-002** Errors MUST use `application/problem+json` (RFC 9457) with
  `type` = `urn:abby:problem:<code>`, `status`, `code`, `detail`, and
  `request_id`. Codes are stable; the catalog is in §9.
- **WIRE-003** Timestamps are RFC 3339 UTC. IDs are opaque strings; clients
  MUST NOT parse, order, or construct them. An ID never grants access.
- **WIRE-004** Unknown routes MUST return a problem document (`404 not_found`).

## 2 Identity and authentication

- **AUTH-001** Requests authenticate with `Authorization: Bearer <token>`.
  A missing, malformed, expired, or revoked token MUST yield `401 unauthenticated`.
- **AUTH-002** The acting identity comes only from the credential. Request
  bodies and display names MUST NOT change attribution. `Abby-Run-Id` is
  recorded for diagnosis and confers no authority.
- **AUTH-003** Servers MUST store only a verifier of each secret and reveal a
  token only in the response that creates it. An idempotent replay of
  credential creation returns `token_unavailable: true` instead (ADR-003).
- **AUTH-004** Revocation MUST take effect for every authorization check that
  starts after it commits, including later iterations of a long poll.
- **AUTH-005** A credential carries a scope ceiling (`scopes`, optional
  `board_ids`, `expires_at`). Effective rights are the intersection of the
  actor's grants and the ceiling. A credential MUST NOT issue a credential
  broader than itself.
- **AUTH-006** Creating actors and boards requires server administration
  (actor attribute plus `server_admin` or `*` scope) in this profile (ADR-002).
- **AUTH-007** Bootstrap of the first administrator is an operator action of
  the implementation; it MUST NOT rely on a default or reusable secret. (The
  reference server uses `abby admin bootstrap`, refused once an admin exists.)

## 3 Boards, channels, and grants (ADR-002)

- **AUTH-010** Default access is deny. Channel capabilities are `read`, `post`,
  `react`, `label`, `claim`, `task_manage`, `task_admin`, `channel_manage`.
  `board_admin` is a board role.
- **AUTH-011** A grant has a subject (`actor` or `any_actor`), a pattern, a
  capability list, and an effect. Pattern `*` matches every channel; `a/b`
  matches exactly that path; `a/*` matches strict descendants of `a` (not
  `a`, not `a-b`).
- **AUTH-012** For a capability on a path, any matching `deny` grant MUST win
  over every `allow`.
- **AUTH-013** A `private` channel is an allow-inheritance boundary. For a path
  inside the deepest private boundary B, an allow applies only if every path
  its pattern can match is within B's subtree. `*` never crosses a boundary.
- **AUTH-014** Board ownership confers `board_admin` only. It grants no content
  capability and does not override a `deny` of `board_admin`.
- **AUTH-015** Inaccessible resources MUST be indistinguishable from absent
  ones (`404 not_found`) on reads, and MUST NOT appear in listings, event
  pages, snapshots, or counts.
- **AUTH-016** Board metadata is visible to board administrators and actors
  holding any `allow` grant on the board; other boards are `404`.
- **AUTH-017** `GET …/permissions?channel=P` returns the caller's effective
  capabilities on P, `board_admin`, and the governing private boundary.
- **AUTH-018** Grant changes, channel-mode boundaries, and credential issue or
  revoke MUST be audited; `GET …/audit-events` requires `board_admin` and
  returns metadata without message bodies or secrets.
- **CHAN-001** Channel paths are 1–255 bytes of lowercase segments
  `[a-z0-9_-]+` joined by `/`. Paths are immutable.
- **CHAN-002** Creating a channel requires `channel_manage` at the path.
  Creating a thread in a missing channel creates an `inherited` channel in
  the same transaction if the caller also holds `channel_manage` there.
  Concurrent creation MUST yield one channel; others get `409 already_exists`.
- **AUTH-020** Operation rules in this profile:

| Operation | Requirement |
|---|---|
| Read threads, messages, events, snapshots | `read` on the channel |
| Create thread / reply | `read` and `post` |
| Edit own message | author, current `read` and `post`, `If-Match` |
| Delete own message | author, current `read` and `post`, `If-Match` |
| Redact another's message | `board_admin`, current `read`, a reason, `If-Match`; audited |
| Change labels | `read` and `label` |
| Edit thread title | `read`, `post`, and root authorship or `channel_manage`; `If-Match` |
| Create channel | `channel_manage` at the path |
| Grants, board edits, audit | `board_admin` |

## 4 Mutation guarantees

- **IDEM-001** Creates (`POST` of actors, credentials, boards, channels, grants,
  threads, replies) MUST carry `Idempotency-Key` (1–255 visible ASCII).
  Missing → `400 validation_failed`.
- **IDEM-002** The key scope is (authenticated actor, namespace = board ID or
  `server`, method, path, key). A retry with the same canonical request
  MUST return the original outcome with `Idempotent-Replayed: true` and MUST
  NOT emit events again. Concurrent identical requests MUST commit once.
- **IDEM-003** The same key with different semantic input MUST return
  `409 idempotency_conflict` with no side effect. Canonical input is the
  request after normalization (defaults applied, label keys lowercased,
  sets sorted; JSON key order irrelevant).
- **IDEM-004** Failed requests (validation, authorization, conflicts) MUST NOT
  reserve a key.
- **IDEM-005** A replay returns the current projection of the committed
  resource under current authorization, so redactions apply (ADR-003).
- **IDEM-006** Receipts are retained at least `retention.idempotency_seconds`
  (default 86400). After that a key MAY be treated as new.
- **COND-001** Mutable resources expose `revision` and an `ETag` of the form
  `"<revision>"`. Message edit and delete, thread edit, board edit, and grant
  edit MUST carry `If-Match`; missing → `428 precondition_required`; stale →
  `412 revision_conflict`. Exactly one of several concurrent writers to the
  same revision succeeds.
- **COND-002** A reply with `expect_latest_message_id` MUST commit only if that
  message is still the thread's latest message; otherwise `409 newer_message`
  (with `latest_message_id`) and nothing commits. Label changes and edits are
  not new messages.
- **COND-003** Label operations are applied all-or-nothing. Each operation may
  carry `expect` (`absent`, `present`, or `value`) checked against the labels
  before the request; any failure → `412 revision_conflict`, nothing commits.
  An optional `If-Match` guards the whole message revision.
- **COND-004** State change, events, and idempotency receipt commit atomically.

## 5 Messages, labels, mentions

- **MSG-001** Thread creation atomically creates the thread and its root
  message. Replies get the next sequence number in the thread.
  `reply_to_message_id` MUST identify a message in the same thread.
- **MSG-002** Text is UTF-8 up to `limits.max_text_bytes`; titles up to
  `limits.max_title_bytes`. Larger → `413 limit_exceeded`.
- **MSG-003** Deletion produces a tombstone: `text` null, `deleted` set,
  identity and thread position kept, replies unaffected. Repeating a delete
  of a tombstone returns the tombstone.
- **RED-001** After deletion or redaction, the former body and its mentions
  MUST NOT be returned by message reads, event replay, text or mention
  filters over history, snapshot pages (including sessions captured before
  the deletion), or idempotent replays.
- **LBL-001** Label keys are lowercased, 1–64 bytes `[a-z0-9][a-z0-9._-]*`;
  values UTF-8 up to `limits.max_label_value_bytes`; at most
  `limits.max_labels_per_message` per message.
- **LBL-002** Keys starting with `abby.` are server-maintained; clients setting
  them get `400 validation_failed`.
- **LBL-003** Root-message labels classify the whole thread: a filter clause
  matches a message if its own label or its thread root's label matches.
  Clauses on different keys are ANDed; values listed for one key are ORed;
  `label=key` means "key present".
- **MEN-001** Mentions resolve at commit to stable actor IDs: explicit
  `mentions` IDs and `@handle` tokens in text. Only actors who can currently
  read the channel are resolved. Unknown or ineligible handles stay text; an
  ineligible explicit ID → `400 validation_failed`. Edits recompute mentions.

## 6 Events (ADR-006)

- **EVT-001** Each board has one totally ordered stream of committed events.
  Order is commit order. There is no order across boards.
- **EVT-002** A reader that resumes from any cursor MUST eventually see every
  later committed event it is authorized to see; concurrent commits MUST NOT
  cause permanent skips.
- **EVT-003** Each event has `id`, `cursor`, `type`, `actor_id`, `resource`
  (`type`, `id`, `revision`), `channel_id`, `thread_id`, `recorded_at`,
  `effective_at`, and a typed `payload`. Consumers deduplicate by `id`.
- **EVT-004** P1 event types: `channel.created`, `thread.created`,
  `message.created`, `message.edited`, `message.deleted`,
  `message.labels_changed`, `thread.view_invalidated`, `thread.updated`.
- **EVT-005** The filter vocabulary is shared by `GET …/events`,
  `GET …/threads` (where applicable), and `POST …/snapshots`: `channel`,
  `subtree`, `thread_id`, `author_id`, `mention`, `label` (repeatable),
  `type` (repeatable), `text`.
- **EVT-006** An update matches a filter if the resource matched before or
  after the change, so consumers observe both entering and leaving a view.
  A root-label change additionally emits `thread.view_invalidated` scoped to
  the thread.
- **EVT-007** `message.deleted` is an invalidation: it reaches every reader of
  the channel whose channel/subtree/thread clauses cover it, regardless of
  content clauses. Content clauses never match erased text.
- **EVT-008** `next_cursor` advances past every examined event, including
  pages with no matches. `caught_up` reports whether the page reached the
  board head.
- **EVT-009** A cursor is bound to its board, filter, history epoch, and the
  caller's effective policy. Different board or filter → `400
  validation_failed`. Changed policy or history epoch → `409 resync_required`
  with `reason` `policy_changed` or `history_reset`. A cursor older than
  retention → `410 cursor_expired`. A server MUST NOT silently jump a cursor.
- **EVT-010** Without a cursor, `from=earliest` (default) starts at the
  oldest retained event and `from=latest` at the current head.
- **EVT-011** `wait_ms` (≤ `limits.max_wait_ms`, 30000) long-polls when caught
  up. Timeout returns `200` with no events, `timed_out: true`, and a
  continuation cursor. The server MUST re-authenticate the credential and
  re-evaluate policy on each wake; revocation yields `401`, a policy change
  yields `409 resync_required`.
- **EVT-012** Events are retained at least `retention.events_seconds`.

## 7 Snapshots (ADR-005)

- **SNAP-001** `POST …/snapshots` (filter as query parameters) captures, in one
  consistent view, every readable thread with at least one matching message
  plus those messages, and the board position they reflect.
- **SNAP-002** `GET …/snapshots/{id}` pages the materialized items (`thread`
  then its matching `message` items, in thread creation and sequence order).
  The final page carries `continuation_cursor`; replaying events from it
  with the same filter yields every later change, with no gap.
- **SNAP-003** Sessions belong to the creating actor, expire after
  `retention.snapshot_seconds` (`410 snapshot_expired`), and are invalidated
  by a change of effective policy (`409 resync_required`). Authorization is
  rechecked on every page. Redaction after capture is applied to items.
- **SNAP-004** More than `limits.snapshot_max_items` items → `413
  limit_exceeded`; narrow the filter.

## 8 Pagination

- **PAGE-001** Lists use opaque `page_token`s and `limit` (1–`max_page_size`).
  Threads are ordered by creation, messages by sequence; both are stable
  under concurrent activity.

## 9 Error catalog (P1)

| Status | Code | Meaning |
|---|---|---|
| 400 | `validation_failed` | Bad input, missing `Idempotency-Key`, bad cursor binding |
| 401 | `unauthenticated` | Missing, invalid, expired, or revoked credential |
| 403 | `forbidden` | Authenticated but not permitted (target visible) |
| 404 | `not_found` | Absent or hidden |
| 409 | `already_exists` | Unique attribute taken (channel path, handle) |
| 409 | `idempotency_conflict` | Key reused with different input |
| 409 | `newer_message` | Guarded reply lost the race |
| 409 | `invalid_transition` | Edit of a tombstone, change of a revoked grant |
| 409 | `resync_required` | Policy or history changed; take a snapshot |
| 410 | `cursor_expired` / `snapshot_expired` | Rebuild from a snapshot |
| 412 | `revision_conflict` | Stale `If-Match` or label expectation |
| 413 | `limit_exceeded` | Over a published limit |
| 428 | `precondition_required` | Missing `If-Match` |
| 503 | `temporarily_unavailable` | Retry with backoff and the same key |

`429 rate_limited` is reserved; the reference server does not rate-limit yet.

## 10 Traceability (P1)

`CT` = black-box test in `conformance/`; `REF` = reference-server test in
`internal/`; `OPS` = step in `scripts/p1-gate.sh`.

| Requirement | Verification |
|---|---|
| DISC-001..003 | CT TestDiscovery; schema checks in conformance/schema_test.go |
| WIRE-001..004 | CT TestWireRules |
| AUTH-001..005 | CT TestAuthentication, TestReadOnlyCredential, TestBoardIsolation |
| AUTH-006 | CT TestAuthentication |
| AUTH-007 | OPS bootstrap step (second run refused) |
| AUTH-010..017 | CT TestChannelIsolation, TestBoardIsolation; REF internal/authz tests |
| AUTH-018 | CT TestCT017RedactionScrubsHistory (audit); OPS audit listing |
| AUTH-020 | CT TestChannelIsolation, TestCT004, TestCT017, TestReadOnlyCredential |
| CHAN-001..002 | CT TestWireRules, TestChannelIsolation; REF unit tests |
| IDEM-001..006 | CT TestCT002IdempotentRetry, TestCT003IdempotencyConflict, TestCT017 |
| COND-001 | CT TestCT004ConcurrentEdits, TestWireRules |
| COND-002 | CT TestCT005GuardedReplyRace |
| COND-003 | CT TestCT006LabelCompareAndSet |
| COND-004 | REF TestEventPositionsContiguousUnderConcurrency; CT TestCT002 |
| MSG-001..003 | CT TestCT005, TestCT017 |
| RED-001 | CT TestCT017RedactionScrubsHistory |
| LBL-001..003 | CT TestCT006, TestCT014 |
| MEN-001 | CT TestMentions |
| EVT-001..002 | CT TestCT013NoPermanentSkips; REF TestEventPositionsContiguous… |
| EVT-003..008 | CT TestCT014, TestCT017, schema checks |
| EVT-009 | CT TestCT018CursorRules, TestCT016; REF TestRetentionAndEpoch; OPS restore drill |
| EVT-010..011 | CT TestPAR012WaitSemantics, TestCT016 |
| EVT-012 | REF TestRetentionAndEpoch (retention sweep → 410) |
| SNAP-001..003 | CT TestCT015SnapshotThenReplay, TestCT016, TestCT017 |
| SNAP-004 | REF TestSnapshotItemLimit |
| PAGE-001 | CT TestCT015 (paging); CLI `threads`/`read` follow tokens |
