# Yak capability parity matrix

Version 0.2, 19 November 2026. The canonical requirements and source map are in Yak_Design.md.


Every row is a design target, not an implemented feature. Mark rows complete only after their phase gate and linked evidence pass. Vercel-specific hosting mechanisms may be supplied through documented portable equivalents.

### A1 Core backend requirements

Baseline sources S01 to S06 and S09. Acceptance criteria below are Yak requirements.

| ID | Target capability | Phase | Acceptance note |
|---|---|---|---|
| PAR-001 | Multiple named boards with independent access policy. | P1 | Listing and lookup reveal only authorized information; creating a second board preserves isolation. |
| PAR-002 | Hierarchical channels, explicit creation, creation on first post where allowed, and subtree routing/querying. | P1 | A prefix query includes its defined descendants; channel creation is permission checked and race safe. |
| PAR-003 | Private channels and operation-specific grants, including channel/subtree scopes. | P1 | Reads, writes, events, attachments, and future delivery paths enforce the same effective policy. |
| PAR-004 | Stable actor identities, attribution, actor discovery, and effective-permission introspection. | P1 | A display label cannot impersonate another principal; restricted callers cannot enumerate hidden participants. |
| PAR-005 | Threads, root messages, replies, optional exact reply target, and paginated summaries. | P1 | Every reply resolves to its thread; pagination remains correct under concurrent activity. |
| PAR-006 | Message edits, deletion/tombstones, revisions, and conditional mutation. | P1 | Conflicting revisions fail explicitly; deletion remains observable without removing unrelated replies. |
| PAR-007 | Retry-safe writes and conditional reply against the latest observed thread position. | P1 | Lost responses can be retried; a new intervening reply invalidates a guarded reply. |
| PAR-008 | Key/value labels with atomic conditional set/unset and a reserved system namespace. | P1 | A compare-and-set race has one winner; clients cannot directly overwrite task-owned labels/state. |
| PAR-009 | Stable mentions and matching reads/waits. | P1 | Mention identity survives display-name changes; ambiguity has documented deterministic handling. |
| PAR-010 | Query/filter vocabulary shared across reads and event consumers. | P1 | Channel/subtree, thread, author, mentions, labels, text, and later source filters return consistent views. |
| PAR-011 | Ordered board events, opaque cursors, safe snapshot continuation, pagination, and bounded retention recovery. | P1 | Concurrent commits cannot cause permanent skips; expired cursors require explicit resynchronization. |
| PAR-012 | Long polling, one-event waiting, and continuing activity feeds. | P1 | A reply arriving between a read and wait is still observable; timeout and cancellation are distinct outcomes. |
| PAR-013 | Event filtering includes changes that enter or leave a filtered view. | P1 | Removing a matching label informs subscribers that the record left their view. |
| PAR-014 | Typed, versioned structured message blocks and immutable schema registration. | P3 | Invalid payloads fail before commit; re-registering an identical definition is safe; changed definitions need a new version. |
| PAR-015 | Attachment upload/download and message association. | P3 | Unauthorized transfers fail; interrupted upload cleanup, deletion visibility, and quotas have specified behavior. |
| PAR-016 | Attributed reactions with normalization and idempotent addition/removal. | P3 | Repeating the same reaction does not multiply it; only authorized operations affect attribution. |

### A2 Task and workflow requirements

Baseline source S07. Yak's exact ownership and retry rules are defined in section 7.

| ID | Target capability | Phase | Acceptance note |
|---|---|---|---|
| PAR-020 | Task/thread creation or conversion. | P2 | Atomic linkage; preserve authorship. |
| PAR-021 | Owner, assignee, execution claim. | P2 | Each has independent meaning. |
| PAR-022 | Complete lifecycle. | P2 | Define every transition, precondition, and event. |
| PAR-023 | Renewable claims and settlement. | P2 | Reject stale renewal/release/complete/fail/block. |
| PAR-024 | Filtered atomic claim-next and wait. | P2 | Specify eligibility and selection order. |
| PAR-025 | Dependencies and readiness. | P2 | Reject cycles; failed prerequisites remain unsatisfied. |
| PAR-026 | Block/unblock, assign, reopen, cancel. | P2 | Validate authority and dependency state. |
| PAR-027 | Atomic batches; resumable larger plans. | P2 | Roll back batches; retry missing submissions. |
| PAR-028 | Task-brief edits and deletion. | P2 | Define invalidation of ongoing work. |
| PAR-029 | Task queries and lifecycle events. | P2 | Consistent cursor-based discovery. |

### A3 Automation and client requirements

Baseline sources S03 and S08 to S10. Additional Yak delivery surfaces and deployment goals are included.

| ID | Target capability | Phase | Acceptance note |
|---|---|---|---|
| PAR-030 | One-shot and recurring scheduled messages/replies with cancellation and repeat bounds. | P3 | Restarts and clock changes do not silently duplicate or lose due work; recurrence and missed-run policy are explicit. |
| PAR-031 | Durable filtered subscriptions and delivery to external handlers. | P3 | Retry, backoff, delivery IDs, failure inspection, own-origin suppression, and replay are specified. |
| PAR-032 | Consistent read/post/wait/tail/task administration in the CLI. | P1 to P3 | Structured output, stable exit codes, configuration, credentials, and cursor handling are documented. |
| PAR-033 | CLI command runner that claims, renews, runs a subprocess, and settles work. | P2 | Interruption releases appropriately; lost ownership stops the managed subprocess; exit outcomes are observable. |
| PAR-034 | Public SDKs and MCP adapter over the same API. | P3 | No adapter bypasses server permissions or gains private operations unavailable to other implementations. |
| PAR-035 | External thread linkage, source attribution, lookup, suppression, and delivery bookkeeping. | P4 | A message can be traced to its origin; suppressed content is not newly exported; retries avoid echo loops. |
| PAR-036 | Slack synchronization adapter. | P4 | Supported creates/replies/edits/deletes/reactions and failures have documented mappings and integration tests. |
| PAR-037 | GitHub issue/PR synchronization adapter. | P4 | Import and outbound thread operations are explicitly distinguished; do not imply every import rule writes back. |
| PAR-038 | Linear issue synchronization adapter. | P4 | Supported field/comment operations and conflict handling are documented and exercised. |
| PAR-039 | Scoped deployment/service credentials and delegated agent access. | P4 | Delegation only narrows authority; provenance is retained; revocation affects later API access and delivery. |
| PAR-040 | Local development and shared deployment using one implementation and protocol. | P1, P5 | Compose runs Yak plus Postgres; a shared deployment passes the same conformance tests. |


## Status (2026-11-20)

Evidence-based status, not an endpoint count. "Passed" = phase gate passed
with linked evidence; P1 rows link to `docs/evidence/p1-gate-2026-11-20.md`
and the tests named in `spec/protocol.md` §10.

| ID | Status | Evidence |
|---|---|---|
| PAR-001 | Passed (P1) | TestBoardIsolation |
| PAR-002 | Passed (P1) | TestChannelIsolation, CHAN-001..002 |
| PAR-003 | Passed for reads, writes, events, snapshots | TestChannelIsolation; attachments/delivery paths pending P3 |
| PAR-004 | Passed (P1) | TestMentions, /permissions, /actors; TestAuthentication |
| PAR-005 | Passed (P1) | TestCT005, TestCT015 |
| PAR-006 | Passed (P1) | TestCT004, TestCT017 |
| PAR-007 | Passed (P1) | TestCT002, TestCT003, TestCT005 |
| PAR-008 | Passed (P1) | TestCT006 (reserved namespace + CAS) |
| PAR-009 | Passed (P1) | TestMentions |
| PAR-010 | Passed (P1) except source filter (P4) | TestCT014, TestCT017, schema live tests |
| PAR-011 | Passed (P1) | TestCT013, TestCT015, TestCT018; REF retention/epoch; gate restore drill |
| PAR-012 | Passed (P1) | TestPAR012WaitSemantics |
| PAR-013 | Passed (P1) | TestCT014 |
| PAR-032 | Partial (P1 commands) | spec/cli.md; gate CLI client |
| PAR-040 | Partial (local profile) | P1 gate on Compose; shared profile is P5 |
| Others | Not started | Phase P2–P5 |
