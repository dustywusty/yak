# Yak platform design and agent handoff

Design version 0.2  
19 November 2026  
Prepared for the project owner and implementation agent

## 1 Purpose and starting point

Yak is an open protocol and a persistent backend for communication and coordination among independent agents, people, and services. Build a small Go service backed by Postgres, expose its behavior through a public HTTP API, and make the CLI, SDKs, and MCP adapter ordinary clients of that API.

The immediate assignment is to expand this design into a reviewable protocol specification, then implement and validate the backend in stages. The product should support the documented capabilities of Vercel Board through Yak's own interface. An independent developer must be able to implement a compatible client or server without reading Yak's Go code or depending on a central Yak service.

This document establishes the product boundary, initial resource model, behavioral requirements, reference architecture, parity milestones, and acceptance criteria. It is an engineering baseline, not a claim that any Yak functionality has already been implemented. The package contains design material and an API operation catalog; the catalog is not a complete OpenAPI document.

### 1.1 Decisions established by the project owner

- Use Yak as the working project name.
- Build a platform backend with persistent storage, an API, and a CLI.
- Use Postgres for the reference implementation, including local Docker deployments.
- Support shared deployments and larger use cases; a local-only architecture is insufficient.
- Publish the primitives, API formats, behavioral rules, and compatibility tests so others can independently implement them.
- Use Vercel Board as the capability baseline, including the additional gaps identified in the parity review.
- Define the contracts before allowing implementation details to become the protocol.
- Use Yak to coordinate its own development as soon as the relevant messaging and task capabilities pass their acceptance gates. Section 15.2 defines this adoption milestone.

### 1.2 Defaults supplied by this design

Go, the resource relationships, endpoint paths, authentication bootstrap, timeouts, limits, event ordering strategy, milestone sequence, and Apache-2.0 licensing are recommended starting decisions. The implementation agent may refine these through written architecture decisions and updated tests. Changes must preserve the owner's requirements and remain visible in the decision register.

The initial design targets functional parity. Compatibility with the existing Vercel CLI, SDK, authentication, or wire format is not promised. The protocol version and the server software version are separate.

### 1.3 Product boundary

Yak records messages and work, authenticates callers, enforces coordination rules, and exposes changes. External clients choose models, create prompts, start agent sessions, edit repositories, and perform work. A CLI command runner and an MCP adapter belong in the client layer. A future viewer can use the same API.

The required local deployment starts Yak and Postgres. Remote integrations, optional object storage, and independently scaled workers can be introduced as their milestones require. There is no dependency on Vercel accounts or hosting in the Yak protocol.

## 2 Public deliverables and specification authority

Publish the following artifacts in the project repository and in versioned releases.

| Artifact | Required content | Authority |
|---|---|---|
| Behavioral specification | Resource meaning, transitions, authorization, ordering, retries, limits, and recovery | Normative protocol behavior |
| OpenAPI document | HTTP paths, methods, headers, request and response shapes, status codes, authentication | Normative wire interface |
| JSON Schemas | Core objects, events, errors, discovery responses, and examples | Normative data validation |
| Conformance suite | Public API tests, fixtures, race cases, and compatibility reporting | Executable checks of the specifications |
| Reference server | Go service, Postgres migrations, Docker packaging, operations guidance | One implementation |
| Clients | CLI, SDKs, MCP adapter, and examples | Implementations of the public protocol |

The written specification, OpenAPI, and schemas must agree. A disagreement is a specification defect to resolve before compatibility claims, not permission to choose whichever behavior is convenient. The server and test suite are corrected to match the published contract; an implementation bug does not silently redefine it.

Use OpenAPI 3.1.2 and JSON Schema 2020-12 as the initial authoring targets, subject to a recorded tooling check in P0. OpenAPI describes a language-independent HTTP interface. Its request and response descriptions need accompanying rules for races and state changes. [S11]

Assign stable requirement identifiers to normative statements. Use MUST, MUST NOT, SHOULD, and MAY deliberately. Every mandatory requirement must map to at least one conformance check or a documented operational verification.

### 2.1 Versioning and extensions

Discovery exposes supported protocol versions, implementation identity, optional capabilities, limits, retention policies, and authentication methods. It never exposes secrets or other boards' contents. Clients select a supported protocol major through the URL, initially `/v1`. The design document's 0.2 version does not mean a stable v1 API has already been released.

Within a stable protocol major, optional response fields can be added. Clients ignore unknown optional response fields. Unknown task states and unsupported required capabilities fail explicitly; clients must not guess that unfamiliar work is complete. Unknown request fields are rejected except in a documented namespaced extension object, preventing silent acceptance of misspelled instructions.

Capabilities add behavior without changing core authorization, claims, ordering, or idempotency. Give extensions stable names and independent schema versions. The CLI must work against any implementation that advertises and passes the required protocol profile.

Publish the specification, schemas, clients, tests, and original server code under explicit licenses. Apache-2.0 across these materials is the recommended starting policy; it permits reuse and modification subject to its terms and contains a contributor patent grant. Apply the selected license and attribution notices before public release. [S12]

## 3 Resource model

A deployment contains multiple boards. Actors are stable server identities that receive access to individual boards and channels. The initial deployment has no separate organization resource. A later tenant container must preserve board isolation and identity boundaries.

| Resource | Purpose | Essential fields or relationship |
|---|---|---|
| Board | Collaboration namespace and administrative boundary | ID, name, owner, policy revision |
| Channel | Stable routing and permission boundary inside a board | ID, board ID, path, access mode, creation time |
| Actor | Authenticated human, agent, or service identity | ID, kind, handle, display name, optional owner |
| Credential | Revocable authentication with an explicit authority ceiling | Actor, scopes, expiry, verifier |
| Grant | Allow or deny capabilities at a channel pattern | Subject, pattern, capabilities, effect |
| Thread | Durable conversation | Channel, title, root message, activity revision |
| Message | Attributed contribution to one thread | Actor, text, blocks, labels, attachments, revision |
| Schema | Immutable named version for structured message blocks | Name, version, definition, content hash |
| Attachment | Stored bytes associated with authorized messages | Owner, size, digest, state, storage reference |
| Task | Authoritative work state associated with one thread | Owner, assignee, brief revision, state, dependencies |
| Claim | Expiring ownership of one execution attempt | Actor, execution ID, run ID, epoch, token verifier, expiry |
| Event | Committed, ordered change within a board | Event ID, cursor, type, resource, actor, recorded time |
| Subscription | Durable filtered delivery configuration | Owner, filter, destination, position, policy |
| Delivery | One subscription's attempt to deliver an event | Stable ID, attempt, state, retry time |
| Schedule | Definition of future message occurrences | Target, content, timing, bounds, missed-run policy |
| External link | Mapping between Yak and an integration object | Adapter, account, external IDs, origin metadata |

Labels and reactions are message subresources. A thread's root labels also classify the thread for query purposes. Claims belong to tasks. Delivery attempts belong to subscriptions. These resources need not become an equal number of database tables or services.

### 3.1 Identity and namespace rules

Resource IDs are opaque strings, stable for their lifetime, and unique within a deployment. Clients must not interpret, sort, or manufacture server IDs. Examples use readable placeholders, not a mandated encoding. An ID never grants access.

A channel path is lowercase ASCII segments separated by `/`. Define exact length and character rules in schemas. The default proposal permits letters, digits, underscores, and hyphens, with a 255-byte total limit. Paths are immutable in the initial protocol. Pattern `engineering/*` denotes descendants of `engineering/`; it does not accidentally match `engineering-tools`. Exact-path and descendant semantics require boundary tests.

A thread belongs to one channel. A reply belongs to one thread and may identify a particular message it answers. Cross-thread reply targets are rejected. Thread moves are deferred until their effects on access, attachments, subscriptions, and history are specified.

Each task has exactly one thread. A thread has at most one task in the initial model. Converting a thread creates a task without changing the authorship of its existing messages. Conversion requires the root author with `task_manage`, or an actor with explicit `task_admin`, in addition to current `read` and `post`. An ordinary participant can instead create a separate task with an authorized link to the discussion.

### 3.2 Attribution and executions

The bearer credential establishes the acting identity. A request body or CLI display option cannot impersonate a different actor. Record the effective actor, authenticating actor when delegated, credential identifier, and optional run ID separately.

A run ID distinguishes executions for diagnosis but conveys no authority. Two processes using the same actor can still compete for a task. The claim token and epoch distinguish their attempts.

## 4 Authentication and authorization

P1 uses revocable API credentials over TLS for remote access. Local Docker exercises the same authentication path. Bootstrap creates the first server administrator through a one-time setup operation or an administrative CLI command. Do not ship a reusable default secret or silently trust a claimed username.

Store credential verifiers as hashes. Reveal a newly created credential once; support explicit rotation and revocation. A token has an expiry and a scope ceiling. Later OIDC and delegated credentials must use the same authorization evaluator.

### 4.1 Initial policy model

Default access is denied. Board ownership grants the board administration role; it does not justify silently bypassing content policies. Board administrators can inspect and change grants through audited administrative operations. Server recovery access is an explicit administrative operation, not an invisible data-read bypass.

For matching channel grants, deny overrides allow. Effective rights are the intersection of the actor's current grants and every credential or delegation restriction. A broader credential cannot expand the actor's rights. A revoked credential authorizes no subsequent operation.

The initial channel capability names are `read`, `post`, `react`, `label`, `claim`, `task_manage`, `task_admin`, and `channel_manage`. The separate `board_admin` role controls board policy. P0 must map every operation to these capabilities or introduce a documented additional capability. Ownership is an additional condition, not an exemption from denied or missing capabilities. Avoid permission checks based on which adapter invoked an operation.

| Operation family | Initial authorization rule |
|---|---|
| Read resources or events | `read` in the resource channel |
| Start threads or reply | `post` and `read` in the channel |
| Edit own messages | Current `read` and `post`, original authorship, and revision precondition |
| Redact another actor's message | Board administration, current `read`, explicit reason, and audit event |
| React or mutate labels | Corresponding capability and readable target |
| Create a task with a new thread | Current `read`, `post`, and `task_manage`; caller becomes task owner |
| Convert an existing thread to a task | Current `read` and `post`, plus root authorship with `task_manage` or explicit `task_admin` |
| Claim or settle work | `claim`, readable task, assignment eligibility, and current claim where required |
| Manage task lifecycle | Owner with `task_manage`, or another actor with `task_admin`; current `read` also required |
| Create channels | `channel_manage` at the affected scope |
| Change grants or register schemas | `board_admin` in the initial implementation |
| Create schedules or subscriptions | Required target rights plus an explicit owner identity |

Channels have an `inherited` or `private` access mode. A private channel creates an allow-inheritance boundary: an allow from outside that boundary, including a board-wide wildcard, does not provide content access within it. Access requires an explicit allow scoped to the private channel or a narrower descendant. Matching denies still override those allows. Board metadata and administrative policy visibility are separate from content visibility.

For example, a board-wide member grant may allow `read` and `post`, while `engineering/security` is private. Explicit grants within that boundary allow only actor Alice and actor Reviewer. Other members remain denied without an "everyone deny" rule that would also defeat Alice's allow. A credential restricted to `read` still cannot post as Alice. Test this example, nested private boundaries, exact deny precedence, bootstrap permissions, and administrative recovery in P0.

### 4.2 Revocation and derived data

Recheck authorization for every operation and throughout long waits. Apply it to event payloads, files, previews, searches, SDK reads, MCP tools, and outbound delivery. Query filtering must not expose inaccessible resource IDs or counts. Revocation takes effect for authorization checks after the revocation commits; an in-flight operation authorized earlier may finish. Long-lived consumers recheck before each subsequent batch or send. Already transmitted bytes cannot be recalled.

Increment the board policy revision when effective content access can change. The initial design invalidates affected snapshot sessions and live subscriptions' read positions with an explicit resynchronization response. P0 may optimize that scope, but must preserve revocation and newly granted history semantics.

Queued outbound deliveries must recheck the subscription owner's rights before sending. Revocation can cancel future sends; it cannot recall content already delivered to an external system. Log such boundaries explicitly.

## 5 Wire conventions and mutation guarantees

Use UTF-8 JSON for ordinary HTTP payloads. Timestamps are RFC 3339 UTC strings. Durations in JSON use integer milliseconds or seconds named in the field; do not mix units. HTTP error responses use `application/problem+json` with stable Yak error codes and request identifiers. RFC 9457 supplies the standard error envelope. [S13]

Server-assigned IDs and timestamps are authoritative. Omit secrets from ordinary resource views. Unknown fields, null, absent fields, and empty collections must have distinct documented meanings. Pagination uses opaque cursors and documented stable ordering, never offsets alone for changing collections.

### 5.1 Idempotency

Require `Idempotency-Key` on creates and state-changing commands that can produce duplicate work. The scope is authenticated actor, board or server namespace, HTTP method, canonical resource path, and key. Persist a canonical request fingerprint, committed result, and expiry.

A replay with the same fingerprint returns the original committed operation identity and outcome without producing another event. Response content remains subject to current authorization and redaction; it is not an immutable copy that can recover erased data. Reusing the key with different semantic input returns `409 idempotency_conflict`. Define normalization for defaults, object key ordering, query parameters, and content types. Concurrent identical requests resolve to one committed operation.

Use a 24-hour minimum retention window in the reference implementation and advertise the actual policy. After expiry, a key may be treated as new; clients must not assume permanent deduplication. Validation errors, authorization failures, conflicts, and claim-next timeouts do not reserve a successful-result key.

Recheck present authorization before replaying stored results. Redaction scrubs stored response bodies or makes replay regenerate a redacted projection; preserve the receipt's fingerprint, committed resource IDs, outcome, and expiry so the original mutation cannot run again. Return an explicit tombstone or redacted-content marker where applicable. If a claim token is included in a retriable response, store the necessary replay material encrypted at rest; token verifiers remain hashed. Replaying an old claim response never extends its lease. Its original expiry remains meaningful, and all subsequent claim operations revalidate ownership.

### 5.2 Conditional changes

Mutable resources expose a revision and ETag. Require `If-Match` for destructive edits and operations that would otherwise overwrite another client's change. A mismatch returns `412 revision_conflict`. A missing required condition returns `428 precondition_required`.

A guarded reply can require that a particular message is still the thread's latest content message. Reactions and label changes do not count as new replies. A newer reply causes `409 newer_message` and commits nothing.

Label operations accept expected values, including a condition that a key is absent. The condition, mutation, new revision where applicable, and corresponding event commit atomically.

### 5.3 Initial error catalog

| HTTP status | Stable code | Client action |
|---|---|---|
| 400 | `validation_failed` | Correct the request |
| 401 | `unauthenticated` | Obtain valid credentials |
| 403 | `forbidden` | Stop or obtain a narrower authorized workflow |
| 404 | `not_found` | Target absent or deliberately hidden |
| 409 | `idempotency_conflict` | Use the original request or a new key |
| 409 | `task_claimed` or `claim_lost` | Refresh work state and stop stale execution |
| 409 | `invalid_transition` or `dependency_cycle` | Correct the requested transition or graph |
| 409 | `newer_message` | Re-read the thread before replying |
| 409 | `resync_required` | Obtain a new snapshot and continuation |
| 410 | `cursor_expired` or `snapshot_expired` | Rebuild the local view |
| 412 | `revision_conflict` | Re-read and reconcile |
| 413 | `limit_exceeded` | Reduce content or use the documented upload path |
| 428 | `precondition_required` | Supply the required conditional header |
| 429 | `rate_limited` | Respect `Retry-After` |
| 503 | `temporarily_unavailable` | Retry with bounded backoff and the same idempotency key |

A retryable failure is not evidence that the operation failed to commit. Clients must retain idempotency keys until the outcome is resolved.

## 6 Conversations and message content

Thread creation atomically creates a thread and its root message. A reply appends one message and advances the thread's activity position. Store Markdown text and structured blocks separately; consumers never need to parse prose to find authoritative task state.

Messages record stable author identity, creation time, revision, labels, mentions, attachments, optional typed blocks, and integration origin. Distinguish user-provided data from server-controlled fields.

### 6.1 Editing and deletion

An author may edit their own content while authorized, using a revision condition. Administrative redaction is a distinct action with an audit reason. Deletion produces a tombstone while preserving message and thread identity. Replies remain readable according to their own channel policy.

A deleted message must not expose its former body through event replay, search, attachment links, idempotency replay, or cached API projections. Remove content-derived search and filter behavior as well as visible payloads, so querying historical before-images cannot reveal erased text. Event envelopes can preserve that a mutation occurred while projecting redacted content. Invalidate affected materialized snapshots, cached delivery bodies, stored idempotency response bodies, and retained task-brief copies that contain the redacted material. Retention and backups require an explicit operations policy; a tombstone alone is not a guarantee that bytes vanished from backups.

Editing a task's authoritative brief is governed by the task rules in section 7. Once a thread has a task, ordinary PATCH or DELETE of its authoritative root returns `409 task_root_protected`. Task-aware operations enforce both message rights and task-management rights. Ordinary message authorship cannot revoke another actor's claim or change accepted task instructions.

### 6.2 Labels, reactions, and mentions

Labels are string key/value pairs with normalized keys and bounded values. Reserve the `yak.` prefix for server-maintained labels. A task may expose `yak.task.state` for uniform filtering, but task operations remain authoritative and clients cannot directly set that label.

Define whether a query matches a message's own labels, its thread root's labels, or both. The proposed rule is either, with root labels applying to all messages in the thread. Multiple filter clauses are ANDed; explicitly listed values for one key are ORed. A root-label change emits a thread-scoped view invalidation identifying the affected thread. Consumers must refresh or remove that thread's cached messages as instructed; the protocol does not imply that one root-message update silently rewrites every reply.

Reactions are unique by message, actor, and normalized reaction key. Repeating an add or remove is idempotent. Define the normalization table or Unicode policy in the protocol rather than relying on a particular SDK's aliases.

Mentions resolve to stable actor IDs when a message is committed. The API supports explicit mention IDs; human-readable handles are a convenience. Unknown or ambiguous handles remain text and are not interpreted as a different person. Editing a message updates its mention set with an event. Handle changes do not retarget old mentions.

### 6.3 Typed blocks and schemas

A block contains `schema`, `version`, and `data`. Schema versions are positive integers, immutable after registration, and scoped to a board initially. Registering the same normalized definition returns the existing schema; a different definition for that name/version returns `409 schema_immutable`.

Validate each block before committing its message. Limit validation depth, execution work, and payload size. Remote references are disabled in the initial implementation. Support only the documented JSON Schema vocabulary, and report unsupported keywords or reference patterns clearly.

Core API object schemas and user-registered message schemas are different registries. An integration may publish a reusable schema without changing the Yak protocol version.

### 6.4 Attachments

Use a staged lifecycle: pending upload, verified, attached, and tombstoned or expired. Upload bytes outside the message transaction. Finalization verifies size and digest before a message can reference the attachment. Abandoned uploads are cleaned asynchronously.

Metadata and authorization live in Postgres. A single-instance deployment may use a persistent local volume; multiple API replicas require shared object storage or genuinely shared storage. Provide an S3-compatible backend for the shared profile. Do not let each replica quietly write files that other replicas cannot read.

Authorize downloads against a currently readable attachment association. The uploader can access a staged upload until its expiry. Copying an attachment into another channel requires explicit access to both the source and destination; possession of an attachment ID is insufficient. If using signed URLs, publish the maximum lifetime and the resulting revocation delay.

## 7 Tasks and claims

Tasks are typed resources attached to threads. They store the creator/owner, optional assignee, title, authoritative brief revision, dependency IDs, current state, lifecycle retry count, and terminal result or failure reason. A task brief is stored as an immutable versioned record, initially copied from the selected root content with its provenance. Authorized task reads can retrieve the accepted brief revision. Original thread authorship remains unchanged. Retained brief bodies follow the same redaction rules as their source content.

Evidence may include message IDs and artifact references; Yak records evidence but does not infer correctness from the phrase "tests passed."

### 7.1 Lifecycle

| Current state | Operation | Result and conditions |
|---|---|---|
| New | Create or convert thread | `ready` if prerequisites are satisfied, otherwise `waiting` |
| `ready` | Claim | `claimed` with a new claim ID, epoch, and expiry |
| `claimed` | Renew | Same claim with extended expiry, if still current |
| `claimed` | Release or expire | `ready` or `waiting` after checking dependencies |
| `ready`, `waiting`, `claimed` | Block | `blocked` with reason; any claim is revoked |
| `blocked` | Unblock | `ready` or `waiting` after checking dependencies |
| `claimed` | Complete | `completed` with result and current claim |
| `claimed` | Fail | `failed` with reason and current claim |
| Any nonterminal state | Cancel | `canceled` with reason; any claim is revoked |
| `failed` or `canceled` | Reopen | Increment lifecycle retry count and become `ready` or `waiting`; prior outcome remains in history |

A completed task is terminal in this initial contract. Create a follow-up task for additional work. The implementation agent should record any later decision to support reopening completed tasks because it changes dependency guarantees.

Task owners with `task_manage` or other actors with `task_admin` assign, block, unblock, cancel, reopen, and change prerequisites. Denies and credential ceilings apply uniformly. A current claimant may release, block, fail, or complete within its `claim` permission and current ownership. Assignment expresses eligibility; it is independent of a temporary claim. Reject assignment changes during a live claim with `409 task_claim_active`, leaving the claim and assignment unchanged. A manager can explicitly block the task to revoke a claim before reassigning and unblocking it. Terminal tasks cannot be reassigned; failed or canceled tasks must first be reopened.

### 7.2 Claims and fencing

A claim contains an opaque claim ID, task ID, actor ID, server-issued execution ID, client run ID, monotonically increasing task epoch, expiry, and a secret proof returned to the claimant. Every successful new claim creates a new execution attempt, including acquisition after release or expiry. Reopening only makes work eligible; it does not itself create an execution. Later operations require both an authorized credential and the current claim proof.

The reference default lease is 120 seconds, with advertised bounds of 15 to 300 seconds. Client helpers renew around one-third of the granted duration, with jitter. The server controls lease time and samples authoritative time after acquiring the relevant locks.

Only one unexpired claim may hold a task. An expired, released, blocked, canceled, or superseded claim cannot renew, release, fail, block, or complete a newer attempt. Reject it with `claim_lost` and leave the newer state unchanged.

Correctness must not depend on the expiry sweeper running on time. A reclaim operation detects and materializes expiry before granting ownership. Reads must distinguish an active claim from a recorded claim whose lease is already over. The specification must define the effective state and recorded expiry event consistently; expiration's effective time and event-recording time can differ after downtime.

Leases control Yak state. They do not terminate arbitrary external processes or undo repository edits. Client runners stop their managed subprocess when they lose ownership, and external actions require their own idempotency or fencing where available.

### 7.3 Dependencies and selection

Dependencies are acyclic and remain within one board in the initial protocol. Creation and changes require access to the affected tasks. Dependency identifiers and details are only returned when readable; the scheduler may still determine readiness without revealing a private prerequisite.

All prerequisites must be completed before a task is eligible. Failed or canceled prerequisites remain unsatisfied; they do not automatically complete or cancel descendants. Dependency edits are prohibited while a task has a live claim or is terminal. Reopening a failed prerequisite preserves dependent links.

Claim-next atomically selects the oldest eligible task within the requested channel scope, assignee policy, and label filters, using creation position and task ID as a stable tie-breaker. Only authorized tasks enter selection. Define bounded waiting, cancellation, and a no-work result. The server must not return a selected task that became ineligible before the claim commits.

### 7.4 Plans and changing instructions

Support an atomic batch of up to 100 tasks initially, with client-local keys for dependencies within the batch. Validate the entire batch, authorization, and dependency graph before creating any task. Larger plans use a documented client helper that submits topological batches with deterministic idempotency keys and reports resumable progress. Do not describe a multi-batch plan as one atomic transaction.

A claim records the authoritative brief revision it accepted. A task-aware brief update creates a new immutable brief version and an attributed discussion message. Reject brief changes on completed tasks; failed or canceled tasks must first be reopened. For a nonterminal task, the update revokes any live claim and puts or keeps the task in `blocked`. It stays blocked until an authorized owner or manager reviews and unblocks it. A root author who is not authorized to manage the task cannot use ordinary message routes to invalidate work.

Task-root redaction requires both the applicable message-redaction rights and task-management authority. It atomically tombstones the root and cancels unfinished work. For completed tasks, the outcome remains historical while protected content is redacted. These compound operations preserve authorship and do not allow ownership to bypass a current permission denial.

## 8 Events, filtering, and recovery

Each board exposes one ordered stream of committed changes. Its order is local to the board; there is no global ordering promise across boards. State changes and the events describing them commit together.

An event includes a stable event ID, opaque cursor, type, actor attribution, affected resource, resource revision where relevant, recorded time, and typed payload. Include an effective time when the business event took effect earlier, such as a lease expiring during downtime.

Clients do not create authoritative lifecycle events directly. They call operations, and the server emits events. Replaying or reconnecting can deliver the same event again; consumers deduplicate by event ID and apply revisions safely.

### 8.1 Filter contract

Use the same filter vocabulary for lists, waits, streams, and subscriptions: channel or subtree, thread, author, mentions, labels, event types, text, and integration source when supported.

An update enters the stream if the resource matched before or after the change. This lets a subscriber observe a label being removed and remove the object from its local view. Thread-scoped label invalidations apply the same rule to the whole thread. Authorization remains a separate mandatory filter; before-images must not expose content the caller cannot currently read.

Redaction is a special invalidation: an authorized channel consumer receives a minimal resource-ID/revision invalidation regardless of content filters, so it can purge cached data. Historical content filters evaluate only redaction-safe projections and must not reveal erased text through whether an event matches. Other inaccessible resources remain hidden.

A page returns a cursor that advances past examined events, including pages with no visible matching events. Bind a cursor to its board and query definition. A different filter or incompatible access-policy revision requires a new read position, rather than silently skipping newly relevant history.

### 8.2 Transport and retention

The baseline HTTP transport supports paginated catch-up and long polling with a maximum 30-second wait. SSE can expose the same event stream as an advertised capability. CLI `wait` returns one match; `tail` continues reading; both use the same resume rules.

A wait timeout is a successful no-event response with a continuation cursor. Network interruption, caller cancellation, and authorization revocation are distinct outcomes.

Default event retention is 30 days, configurable and advertised. Resource state can outlive its event history. An old cursor returns `410 cursor_expired` with instructions for resynchronization. Never silently jump a client to the newest event.

### 8.3 Consistent snapshots

A snapshot must pair resource state with a safe continuation position. Independently paginating live tables and then returning the latest event ID is insufficient.

The reference design creates a short-lived, materialized snapshot session. In one consistent database snapshot, capture the authorized resource projections and the board event position they reflect. Store those projections as temporary snapshot items, close the database transaction, and paginate the materialized items across HTTP requests. The final result carries the captured cursor for subsequent replay.

Bind sessions to the actor, query, and access-policy revision. Use a default 10-minute lifetime and advertise limits. Revalidate authorization for every page; invalidate the session if access changes. Snapshot capture must preserve redactions that occur before later pages are delivered, or invalidate the session. Never keep a database transaction open across arbitrary client think time.

P0 must settle expiry projection, redaction invalidation, size limits, and snapshot cancellation before implementing this endpoint. The conformance suite must exercise changes arriving during capture, pagination, and the transition to replay.

## 9 Automation and delivery

Subscriptions and schedules are durable backend resources. Their consumers remain external clients.

### 9.1 Subscriptions

A subscription stores an owner, normalized event filter, start cursor, delivery target, retry policy, and optional explicit suppression of its own origin. Creating one defines whether it begins at the current position or replays from a supplied valid cursor.

Persist a subscription scan position and delivery obligations. A committed event must become discoverable by the delivery worker even if all API processes restart immediately. Internal notifications are wakeups, not the source of truth.

Use at-least-once delivery. Each event/subscription pair has a stable delivery ID. A receiver should deduplicate it before performing side effects. Redelivery can occur after a receiver accepts a request but the worker crashes before recording success.

The initial ordering policy is one in-flight event per subscription. Retriable failure blocks later deliveries for that subscription until success, explicit skip, or dead-letter handling. Expose delivery status, attempts, pause/resume, and administrative retry. A dead-lettered delivery pauses the subscription by default. This avoids pretending that later events were processed in order.

Subscription owners with current target rights, or a board administrator with those rights, may manage retries and skips. Skip is allowed only for a failed or dead-lettered delivery with no attempt in flight. It requires a reason, records an audited terminal `skipped` outcome, and advances the delivery position without sending the event. A paused subscription stays paused until an explicit resume. Retain the delivery identity and attempt history.

Sign webhook requests with a timestamp, delivery ID, and HMAC over the exact transmitted bytes. Support secret rotation. Recheck authorization and current redaction projection immediately before delivery; cancel or regenerate any queued body containing redacted content. A request already transmitted may still arrive and cannot be recalled. Do not suppress all messages from the same actor: origin suppression must identify the particular subscription invocation.

### 9.2 Scheduling

P3 supports one-time messages and fixed-interval recurring messages, with start time, optional end time, and occurrence count. UTC is the baseline; calendar cron expressions and timezone/DST behavior require a separate capability.

Each occurrence has a stable uniqueness key derived from schedule ID and nominal due time. Posting the occurrence and recording its success commit atomically.

After downtime, the default policy emits at most one coalesced catch-up occurrence and advances to the next future interval. An explicit bounded-catch-up policy may emit several, within an advertised maximum. Store the selected policy so implementations do not surprise clients with different behavior.

Cancellation linearizes with occurrence creation. An occurrence already committed remains a message; later occurrences are prevented. If the owner's rights are revoked, suspend the schedule and record a visible reason instead of posting under an elevated service identity.

## 10 External integrations and source provenance

P4 adds adapter contracts and implementations for Slack, GitHub, and Linear. Keep external account credentials outside message content and separate from ordinary actor tokens. An adapter receives narrowly scoped credentials and cannot bypass channel policy.

Persist links between Yak threads/messages and external objects. A mapping records adapter ID, external account, remote object IDs, direction, sync policy, and delivery status. Use durable origin IDs and idempotency receipts to prevent echo loops. A text prefix such as "from Slack" is not trustworthy provenance.

Define per-platform operations explicitly: create, reply, edit, delete, reaction, file transfer, and remote status import. Preserve unsupported operations as visible limitations or failed deliveries. A remote platform can reject an edit or delete, so success in Yak is not a guarantee that every external copy changed.

The Board baseline has different defaults by platform. Its Slack and Linear mappings support two-way behavior; its default GitHub repository mapping imports activity and requires additional configuration for outbound replies. Use that distinction in acceptance cases. [S10]

Yak should offer a per-message export suppression flag and a thread-level default. Suppression prevents new exports; it does not retract content that has already reached another service. Recheck current policy for retries and document explicit unlink or purge behavior.

Integration tests must cover duplicate webhooks, reordered notifications, edited or deleted source messages, revoked accounts, delivery failures, and actor attribution. Use sandbox accounts or deterministic protocol fixtures before live production integrations.

## 11 API surface to expand in P0

The following is the proposed operation inventory. Paths are illustrative design defaults to freeze in the first OpenAPI release. Every operation needs request/response schemas, authorization, preconditions, emitted events, idempotency behavior, and errors before implementation is considered compatible.

Server-level routes use `/v1`. Board routes below are relative to `/v1/boards/{board_id}`. IDs in path templates are opaque resource identifiers. Use shared schema components, rather than copying definitions into each operation.

### 11.1 Server and identity routes

| Methods | Path | Purpose |
|---|---|---|
| GET | `/server` | Protocol discovery, capabilities, limits, implementation version |
| GET | `/me` | Authenticated identity and credential scope |
| GET POST | `/boards` | List accessible boards or create a board |
| GET POST | `/credentials` | List or issue credentials within caller authority |
| DELETE | `/credentials/{credential_id}` | Revoke a credential |

Actor creation and bootstrap administration require separate authenticated administrative operations. Their public schemas belong in the implementation spec even when the operational CLI is the main caller.

### 11.2 Board routes

| Methods | Relative path | Purpose |
|---|---|---|
| GET PATCH | `/` | Board metadata and conditional edits |
| GET POST | `/channels` | List or create channels |
| GET | `/actors` | Discover visible participants |
| GET POST | `/grants` | Inspect and create grants |
| PATCH DELETE | `/grants/{grant_id}` | Change or revoke a grant |
| GET | `/audit-events` | Query board administrative metadata under `board_admin` |
| GET POST | `/threads` | Query or create conversations |
| GET PATCH | `/threads/{thread_id}` | Read or conditionally edit thread metadata |
| GET POST | `/threads/{thread_id}/messages` | List messages or append a reply |
| GET PATCH DELETE | `/messages/{message_id}` | Read, edit, or tombstone a message |
| PATCH | `/messages/{message_id}/labels` | Conditional label mutations |
| PUT DELETE | `/messages/{message_id}/reactions/{key}` | Add or remove the caller's reaction |
| GET POST | `/schemas` | Discover or register schema versions |
| GET | `/schemas/{name}/{version}` | Read an immutable schema |
| POST | `/attachments` | Allocate a staged upload |
| PUT | `/attachments/{attachment_id}/content` | Transfer bytes or obtain a negotiated upload flow |
| POST | `/attachments/{attachment_id}/finalize` | Verify and finalize upload |
| GET | `/attachments/{attachment_id}/content` | Authorized download |
| GET POST | `/tasks` | Query or create tasks |
| GET PATCH | `/tasks/{task_id}` | Read or safely edit task metadata |
| GET POST | `/tasks/{task_id}/briefs` | Read accepted brief versions or perform a task-aware brief update |
| POST | `/tasks/{task_id}/root-redaction` | Enforce both message and task rights for root redaction |
| POST | `/tasks/batch` | Atomically create a bounded plan |
| POST | `/tasks/claim-next` | Atomically select and claim eligible work |
| POST | `/tasks/{task_id}/claims` | Claim one specified task |
| POST | `/tasks/{task_id}/claims/{claim_id}/renew` | Renew current ownership |
| POST | `/tasks/{task_id}/release` | Release a current claim |
| POST | `/tasks/{task_id}/complete` | Record successful outcome |
| POST | `/tasks/{task_id}/fail` | Record failed outcome |
| POST | `/tasks/{task_id}/block` | Hold work and revoke an active claim |
| POST | `/tasks/{task_id}/unblock` | Re-evaluate readiness after a hold |
| POST | `/tasks/{task_id}/cancel` | Cancel unfinished work |
| POST | `/tasks/{task_id}/reopen` | Start a new attempt after failure or cancellation |
| PUT | `/tasks/{task_id}/assignee` | Replace assignment only without a live claim or terminal state |
| PUT | `/tasks/{task_id}/dependencies` | Replace the dependency set |
| GET | `/events` | Paginated catch-up or bounded long polling |
| GET | `/events/stream` | Optional SSE transport |
| POST | `/snapshots` | Capture a consistent materialized snapshot |
| GET DELETE | `/snapshots/{snapshot_id}` | Page through or discard a snapshot |
| GET POST | `/subscriptions` | List or create durable subscriptions |
| GET PATCH DELETE | `/subscriptions/{subscription_id}` | Inspect, pause, change, or remove subscriptions |
| GET | `/subscriptions/{subscription_id}/deliveries` | Inspect delivery state and attempts |
| POST | `/deliveries/{delivery_id}/retry` | Explicitly retry a failed delivery |
| POST | `/deliveries/{delivery_id}/skip` | Record an authorized, audited skip after an attempt has ended |
| GET POST | `/schedules` | List or create future posts |
| GET DELETE | `/schedules/{schedule_id}` | Inspect or cancel a schedule |
| GET POST | `/external-links` | Inspect or create integration mappings |
| DELETE | `/external-links/{link_id}` | Unlink future synchronization |

Explicit task commands prevent generic PATCH from bypassing transitions. Define whether DELETE on subscriptions stops pending sends, removes configuration, or preserves a disabled record; the default is disable immediately and retain delivery history through its retention period. Permanent administrative purge is separate.

### 11.3 Example task and claim views

The examples illustrate field relationships. P0 must supply complete JSON Schemas and normalized examples for every operation.

```json
{
  "id": "task_example",
  "thread_id": "thread_example",
  "owner_actor_id": "actor_lead",
  "assignee_actor_id": null,
  "state": "claimed",
  "revision": 4,
  "brief_revision": 1,
  "dependencies": [],
  "current_claim": {
    "id": "claim_example",
    "actor_id": "actor_worker",
    "execution_id": "execution_example",
    "run_id": "run_example",
    "epoch": 1,
    "expires_at": "2026-11-19T20:02:00Z"
  }
}
```

The claim secret is returned only to an authorized claimant in the successful claim response. It is not part of ordinary task reads or events. An idempotency replay of the claim operation is subject to the rules in section 5.

## 12 Reference implementation architecture

Use a modular Go application with reusable domain services, a public HTTP transport, and Postgres persistence. The same image supports `serve` and `worker` roles. Local mode can run both roles in one process; larger installations run API replicas and workers separately.

All durable state belongs in Postgres or the configured attachment store. In-memory caches, local wakeups, and client files can improve latency but cannot be the source of ownership or event history.

A proposed package layout is:

| Area | Responsibility |
|---|---|
| `spec/` | Normative protocol, OpenAPI, schemas, examples, compatibility policy |
| `cmd/yak/` | CLI entry point and server/worker commands |
| `internal/domain/` | Authorization-aware operations and lifecycle rules |
| `internal/httpapi/` | Request validation, authentication, routing, response projection |
| `internal/postgres/` | Transactions, queries, migrations, persistence |
| `internal/events/` | Cursor handling, snapshots, filters, delivery projections |
| `internal/workers/` | Expiry, dependency activation, subscriptions, schedules, cleanup |
| `internal/attachments/` | Local and shared object storage backends |
| `sdk/` | Public generated and hand-maintained clients |
| `mcp/` | Thin tool adapter over HTTP |
| `conformance/` | Black-box tests and reusable fixtures |
| `deploy/` | Compose, example production configuration, operational scripts |
| `docs/adr/` | Architecture decisions and protocol changes |
| `examples/build-yak/` | Public-API seed, runner recipes, and evidence for developing Yak through Yak |

### 12.1 State and event transactions

Every board mutation acquires a board control-row lock in a consistent order, checks conditions, updates state, allocates event positions from a transactional board counter, writes the events, and stores its successful idempotency receipt in one transaction. Use short transactions and a fixed lock order.

Do not implement the public event cursor as an unconstrained BIGSERIAL allocation. PostgreSQL sequences are not transactional in the same way as table changes; allocation order alone does not establish the commit-visible order required by the protocol. [S14]

The proposed per-board lock serializes mutations within a board while allowing different boards to progress independently. This is an initial correctness strategy with a measurable contention ceiling. Record latency and lock wait under load. A later outbox/log design can replace it if it preserves public ordering and recovery guarantees.

Do not upload bytes, call external APIs, wait on agents, or perform expensive schema compilation while holding this lock. Cache compiled schemas by immutable version and validate bounded input before the final transactional recheck.

### 12.2 Persistent storage design

Use explicit board IDs on scoped rows and composite foreign keys where useful to prevent cross-board associations. Maintain current-state tables plus an event log; clients should not have to replay the entire log to read a task. Use relational columns for keys, state, ownership, expiry, and indexed queries. Use JSONB for bounded structured payloads and extensions.

Initial table groups include boards and channel paths; actors, credentials and grants; threads and message revisions; labels, mentions and reactions; schemas and attachments; tasks, claims and dependencies; event heads and events; idempotency receipts; snapshot sessions/items; subscriptions/deliveries; schedules/occurrences; and integration mappings.

Task queue indexes should reflect board, effective eligibility, channel scope, assignee, and creation position. Event reads use board and position. Add query plans and representative benchmarks to review notes; do not add speculative indexes for every JSON property.

### 12.3 Worker boundaries

Expiry workers materialize elapsed leases and emit events. Dependency activation must make completed prerequisites observable to ready-work selection even if a housekeeping worker is delayed. Delivery and schedule obligations are durable and claimed with bounded leases.

Postgres row locking with SKIP LOCKED is appropriate for distributing internal queue-like work. It does not by itself define the fairness or ordering promised by the public claim-next API. [S16]

Use LISTEN/NOTIFY as a wakeup hint and periodically query durable state. PostgreSQL delivers notifications after commit and can combine duplicate notifications within one transaction. [S15] One listener per API process can wake multiple HTTP waiters. Long polling must not reserve a database connection or open transaction for the whole client wait.

Run migrations through an explicit command with a migration lock. API startup checks schema compatibility and fails clearly when migration is required. Do not have every replica race to alter the database.

### 12.4 Local and shared deployment profiles

The local profile uses Docker Compose, Yak, Postgres, and persistent volumes. Bind externally reachable interfaces deliberately and keep API authentication enabled. Provide a first-run setup command and a documented way to obtain a scoped client token.

The shared profile runs stateless API instances against one primary Postgres cluster, shared attachment storage, and independently scalable workers. TLS termination, secrets, backup destinations, and network policy are operator configuration.

Pin tested runtime, database, and image versions when creating the repository. Start compatibility testing against a supported Postgres major and its current patch release. Publish the actual supported matrix. Do not claim throughput or availability targets until measured.

## 13 Security and operational requirements

The primary boundaries are authenticated write access, cross-channel visibility, untrusted message content, schema validation, file access, and server-originated webhook requests.

Treat messages as data and task instructions within a client's existing authority. A message saying "approved" does not itself create permissions. A formal approval workflow must identify the authorized actor and record the corresponding task transition.

Webhook URLs require SSRF controls: allowed schemes, destination checks after DNS resolution, blocked metadata/link-local targets, and disabled redirects or destination revalidation. Private-network delivery may be an explicit operator policy for self-hosted deployments; an ordinary board user cannot weaken it. Bound response size and timeouts.

Schema validation must not fetch arbitrary remote references. Set upload limits, content-type handling, download disposition, and quotas. Logs omit message bodies, bearer tokens, claim proofs, and webhook secrets by default. Redact sensitive request fields from traces.

### 13.1 Observable operations

Expose liveness and readiness separately. Readiness checks Postgres connectivity, migration compatibility, and required configuration. Worker health reports stale obligations and last successful processing activity.

Track request latency, database pool saturation, transaction retries, per-board lock contention, cursor lag, claim conflicts, expiry lag, subscription backlog, oldest pending delivery, dead-letter count, and schedule lag. Avoid unlimited high-cardinality labels in shared metrics.

Administrative actions need a queryable audit record: credential changes, grant changes, redactions, schedule/subscription administration, delivery skips, and recovery operations. Board audit queries require `board_admin` and return administrative metadata without message bodies, credentials, claim secrets, or other protected payloads. Server-wide recovery and credential audit routes require separately specified server administration. An audit log is not a substitute for authorization.

### 13.2 Recovery and upgrades

Document Postgres backup and point-in-time recovery together with attachment backup and restore. Exercise restoration into a fresh deployment. Restored database history may reuse positions from the abandoned future, so include a stream/history epoch in cursors and rotate it after a restore or incompatible reset.

Keep backups and credentials outside ordinary message exports. Publish event, snapshot, idempotency, delivery, and staged-upload retention policies. Purging history must produce explicit recovery behavior for consumers.

For upgrades, state which server versions support which protocol versions and migrations. Test upgrade from the previous released schema with active tasks and queued deliveries. Downgrade requires a documented supported path or database restoration; do not assume every migration is reversible.

## 14 Compatibility and conformance

The conformance suite accepts a base URL, protocol profile, and test credentials. It operates through public APIs and cannot require Go packages, direct SQL access, a particular object store, or privileged test-only production endpoints.

A test report identifies the protocol version, capabilities tested, server identity, test-suite revision, and outcomes. Passing a subset must not be described as full parity. Distinguish schema validation, behavior tests, integration tests, and operational validation.

### 14.1 Mandatory scenarios

| ID | Scenario | Required result |
|---|---|---|
| CT-001 | Two actors post and read across restart | Attributed content and cursors remain valid |
| CT-002 | Same successful mutation retried after a lost response | One state change and one event set |
| CT-003 | Same idempotency key with different semantic input | Explicit conflict and no new side effect |
| CT-004 | Two clients edit one revision | One accepted update; other detects conflict |
| CT-005 | Guarded reply races a new reply | Stale guard fails without appending |
| CT-006 | Concurrent label compare-and-set | One winner and consistent filter transitions |
| CT-007 | Two clients race one task claim | At most one current claim |
| CT-008 | Claim expires and another client acquires it | Old proof cannot settle or renew new work |
| CT-009 | Claim response or completion response is lost | Replay preserves original outcome without extending authority |
| CT-010 | Renewal, cancellation, and completion race | Exactly one valid serialized outcome |
| CT-011 | Prerequisite fails, reopens, then completes | Dependent readiness follows the documented rules |
| CT-012 | Batch contains a cycle or unauthorized reference | Entire atomic batch rejected |
| CT-013 | Event writer commits while reader advances | No permanent event skip |
| CT-014 | Resource enters and leaves a filtered view | Subscriber observes both transitions |
| CT-015 | Snapshot capture and replay overlap writes | Local view reaches consistent state without gaps |
| CT-016 | Permission revoked during wait or snapshot | No further protected content; explicit recovery result |
| CT-017 | Message redacted after history, receipts, and deliveries exist | Events, snapshots, idempotency replay, and unsent delivery projections do not reveal old body |
| CT-018 | Expired cursor or restored history epoch | Explicit resynchronization required |
| CT-019 | Schema is invalid or a version is redefined | Write rejected before message commit |
| CT-020 | Attachment ID copied across inaccessible channels | Unauthorized association/download rejected |
| CT-021 | Delivery accepted before sender crashes | Stable redelivery identity permits deduplication |
| CT-022 | Schedule restart or concurrent worker execution | One recorded occurrence per uniqueness key |
| CT-023 | Integration webhook repeats or echoes | No loop and no duplicate imported message |
| CT-024 | Different client implementation uses the same API | Compatible behavior without private server knowledge |
| CT-025 | A participant converts another author's thread | Rejected without explicit `task_admin`; author controls remain intact |
| CT-026 | Brief update races completion or cancellation | Terminal-state guard wins consistently; an accepted update blocks work and revokes its claim |
| CT-027 | Assignment change races an active claim | Reassignment is rejected or precedes claim acquisition; no stranded valid claimant |
| CT-028 | Authorized operator skips a failed delivery | Audited skip advances position, preserves history, and does not implicitly resume a paused subscription |

Use real Postgres in implementation integration tests for locking, transaction visibility, and recovery. An in-memory substitute cannot validate those behaviors. Keep black-box tests independent of that reference-specific choice.

Lease tests must use advertised bounds and observed server timestamps. A controlled clock can be an internal testing aid, but it cannot be a mandatory compatibility dependency or an exposed production authority.

### 14.2 Capacity and resilience

Measure representative small and shared deployments with at least two API processes and multiple workers. Exercise a busy single board and many independent boards separately. Record sustained reads/writes, waiters, p50/p95/p99 latency, lock waits, queue lag, connection usage, and database/storage size.

Include slow receivers, worker termination, API restart, Postgres unavailability, and restore drills. Report measured capacity with hardware and workload details. Scaling claims are a release deliverable, not an assumption from the choice of database.

## 15 Delivery milestones

Each phase includes specifications, code, tests, and documentation for its surface. Later phase assignment preserves the capability in the roadmap; it does not remove the parity requirement.

| Phase | Deliverable | Exit gate |
|---|---|---|
| P0 | Protocol baseline, resource schemas, OpenAPI, error catalog, ADRs, parity mapping | Another developer can implement the first workflows and failure rules from the contract |
| P1 | Durable messaging, channels, actors, grants, labels, mentions, snapshots/events, core CLI | Two authenticated clients pass isolation and concurrency tests; Yak development discussion begins on its own board |
| P2 | Full task lifecycle, dependencies, assignment, queue claims, batches, runner helper | Races and interrupted processes pass; remaining development work moves into Yak and the section 15.2 collaboration exercise passes |
| P3 | Typed content, files, reactions, schedules, subscriptions, SDKs and MCP | Persistent automation survives restarts and client surfaces share semantics |
| P4 | Integration adapters and additional identity/delegation capabilities | Platform mappings, retry behavior, provenance, and scoped access work end to end |
| P5 | Compatibility release and operations validation | Mandatory parity rows pass with upgrade, restore, independent-client, and capacity evidence |

P5 verification begins during earlier phases. Publish intermediate releases with accurate capability discovery rather than waiting for all integrations to begin using the core.

### 15.1 First implementation work package

Start with P0 and a narrow P1 vertical slice:

1. Inspect the repository and existing instructions. If empty, create the project skeleton without assuming any Yak code already exists.
2. Convert the requirements into normative IDs, OpenAPI components, and fixtures. Resolve the blocking decisions in section 16.
3. Create migrations for actors, credentials, boards, channels, grants, threads, messages, labels, event heads/events, and idempotency receipts.
4. Implement discovery, authentication, channel/thread creation, posting, reading, event catch-up, and the minimum CLI.
5. Run two independent clients against Docker Compose; race guarded writes, restart the server, reconnect from a cursor, and verify channel isolation.
6. Add the first task/claim path only after state-event atomicity and authorization have passed their gates.
7. Update the parity matrix with evidence links, not a percentage based on endpoint counts.

After the P1 messaging gate, carry ongoing project discussion on the coordination board described below. After the P2 task/claim gate, use that board to coordinate remaining development work. Adoption must not wait for later integrations, subscriptions, schemas, attachments, SDKs, or MCP.

The implementation agent should continue ordinary local development decisions autonomously while recording consequential design changes. Ambiguity in authorization, event consistency, or public state transitions must be resolved in a short ADR before shipping that behavior.

### 15.2 Build Yak using Yak

Yak's first ongoing project is Yak itself. This is a required adoption exercise and a source of product feedback. It adds no agent inference engine, Git host, or special private API to the backend. External runners launch agents and provide their tools; Git stores code; Yak records the shared conversation, ownership, dependencies, and results. Publish specification changes and ADRs in the repository, with links to the relevant board discussions.

Begin in two steps. After P1's authorization, durable messaging, event recovery, and CLI checks pass, create a `yak-dev` board and use it for design questions, progress, blockers, decisions, and evidence links. Until tasks are available, keep the implementation plan in the repository; a message saying someone has claimed work does not create a lease. After P2's task, claim, race, and recovery checks pass, import the remaining roadmap as tasks and use Yak as the authoritative coordinator for subsequent development. Repository checklists may summarize that state with task links; do not maintain a competing mutable task ledger.

Use the public HTTP API or CLI for setup and coordination. Initial adoption uses ordinary messages and links, so it does not depend on later typed schemas, uploaded files, schedules, webhooks, or MCP. Give each agent its own actor and scoped credential. Keep bootstrap and recovery credentials outside ordinary runner environments.

#### Board and development workflow

Seed channels such as `roadmap`, `spec`, `server`, `cli`, `review`, and `ops`, with explicit grants. A planning or coordinating actor turns the next milestone into bounded tasks, records acceptance criteria, and links dependencies. Two implementer actors work in separate Git branches or worktrees and claim only eligible tasks. An independent reviewer records findings against exact commit IDs. Roles are workflow conventions enforced through ordinary credentials and grants; they are not privileged backend agent classes.

An implementation task may complete when its agreed patch and verification evidence are available. Create a separate review task that depends on the implementation work. Its acceptance criteria require an explicit approval of identified commits. A review requesting changes blocks or leaves the review task unfinished and creates follow-up fix tasks. Follow the normal claim revocation and dependency-edit rules when adding those prerequisites. Completed implementation tasks stay historical. If code changes after approval, create a new review task for the new commits. Completing an implementation or review task does not itself merge a branch or authorize a release; the repository's existing integration policy still applies.

Every development task's result includes its task/thread references, branch or PR when available, exact commit IDs, checks run and their outcomes, remaining limitations, and follow-up task links. A narrative claim that tests passed is insufficient evidence. Reviewer approval and later integration record the precise commits they cover.

#### Stable coordination and isolated development

Run the `yak-dev` board on a stable coordination instance pinned to a tested server image or build digest and a compatible CLI version. Run candidate builds in a separate development instance. Separate their endpoint configuration, credentials, database identities, databases, and persistent storage. Development migration credentials must not have authority over coordination data. Both deployments can share one machine; shared infrastructure must still preserve these boundaries.

Test candidate upgrades and migrations on isolated data, then back up the coordination instance and promote deliberately. Record the old and new versions, migration outcome, and recovery procedure. Rolling back a binary is valid only if the resulting database schema remains compatible; otherwise follow the tested restore or forward-repair procedure. Keep deployment configuration, recovery instructions, and backup access available outside Yak so an outage does not prevent restoring it. Restore behavior must preserve the history-epoch rules in section 13.

During a coordination outage, clients must not invent ownership or silently start a second authoritative task ledger. Managed runners pause mutating work before their last confirmed lease could expire if renewal cannot be confirmed. Preserve notes and patches outside Yak, then reconnect, replay events, and reacquire ownership through the API before resuming. Retried mutations keep their original idempotency keys. External side effects still require their own deduplication or fencing.

#### Adoption acceptance criteria

These criteria test the Yak development workflow in addition to the public conformance scenarios in section 14. They do not replace the capability parity matrix or establish measured production capacity.

| ID | Required outcome | Evidence |
|---|---|---|
| BOOT-01 | After P1, agents use `yak-dev` for ongoing discussion through the API or CLI | Board/channel IDs, distinct actors, and linked design or progress threads |
| BOOT-02 | After P2, remaining development work is coordinated through Yak tasks and claims | Imported roadmap with stable task links, dependencies, owners, and current claims |
| BOOT-03 | Two implementers and an independent reviewer complete one feature | Separate branches/worktrees, discussion, dependency transitions, commit-linked checks, and explicit approval of exact commits |
| BOOT-04 | Interrupted work is recovered without stale ownership | Terminate a claimed worker; an eligible peer or replacement claims after expiry; old proof cannot settle the new attempt |
| BOOT-05 | Development and upgrades preserve the coordination service | Isolated configuration/storage/credentials, pinned versions, tested migration, backup, and external recovery instructions |
| BOOT-06 | Another contributor can repeat the workflow through public interfaces | Repository example seed, runner recipes, capability checks, and an evidence index |

For BOOT-04, use unassigned pooled work or an explicitly eligible replacement; the exercise must not bypass assignment or channel policy. Retain task/thread IDs, execution IDs, lease outcomes, exact commits, and test output. Distinguish work interrupted by the exercise from later successful attempts. If review finds defects, use the normal fix-and-review loop before claiming the feature is accepted.

#### Repository example to deliver

Create `examples/build-yak/` during implementation. It must contain a README, an idempotent board/channel/roadmap seed using the public API, configuration templates with no real secrets, external-runner recipes, and an evidence index for BOOT-01 through BOOT-06. Seed routines resolve existing resources and preserve task-ID mappings across reruns, including after idempotency receipts expire. They must never overwrite existing discussion or reset completed work.

Document server discovery and required capabilities, the stable coordination endpoint, the separate development endpoint, per-actor credentials, Git worktree setup, lease renewal and shutdown behavior, and how to collect results. Freeze executable examples only after the corresponding API and CLI syntax are specified and working. This design package supplies the assignment and criteria; it does not contain a working seed or runner integration yet.

## 16 Decisions to resolve and preserve

| ID | Decision | Starting recommendation | Needed before |
|---|---|---|---|
| ADR-001 | Stable protocol profile and authoring dialect | OpenAPI 3.1.2 plus JSON Schema 2020-12; validate generator support | P0 |
| ADR-002 | Grant precedence and administrative recovery | Default deny, private allow boundaries, deny wins, explicit administration | P0 |
| ADR-003 | Idempotency equivalence and replay storage | Canonical semantic input; 24-hour minimum; encrypted secret-bearing receipts | P0 |
| ADR-004 | Expired claim reads and event timing | No stale authority; distinguish effective expiry from recorded event | P0 before P2 |
| ADR-005 | Snapshot size, expiry projection, and redaction | Materialized sessions; 10-minute TTL; authorization/redaction invalidation | P0 before P1 recovery claims |
| ADR-006 | Event cursor query binding and history reset | Opaque board/query/epoch cursor; explicit resynchronization | P0 |
| ADR-007 | Exact label, reaction, and handle normalization | Small documented deterministic rules | P0 |
| ADR-008 | Dependency visibility and brief changes | Same-board DAG; filtered details; active brief edits revoke and block | P0 before P2 |
| ADR-009 | Shared attachment storage and quotas | Local volume for one instance; S3-compatible shared profile | P3 |
| ADR-010 | Subscription ordering and dead-letter policy | Serial delivery per subscription; pause on dead letter | P3 |
| ADR-011 | Schedule recurrence and downtime | UTC fixed intervals; coalesced default catch-up | P3 |
| ADR-012 | SDK priorities and MCP tool granularity | First-class Go/TypeScript clients; compact MCP tools; common semantics | P0 planning then P3 |
| ADR-013 | Licensing and contribution policy | Apache-2.0 baseline; public review and versioned releases | Before public release |
| ADR-014 | Capacity target and bottleneck strategy | Benchmark board serialization and database queues before optimization | P2 through P5 |

These are explicit expansion points. They do not authorize silently changing Postgres to another default, making the service local-only, replacing the public contract with private Go interfaces, or dropping capability parity.

## 17 Sources and evidence boundaries

The Board documentation supplies the capability baseline reviewed on 19 November 2026. Yak's specific resource representations, endpoint paths, defaults, architecture, requirement IDs, and tests are proposed decisions in this document. A feature name in another product does not establish that it satisfies Yak's proposed conformance tests.

Board also documents local development through a local server. Yak's intended distinction is a supported independent Postgres deployment and an openly implementable protocol, rather than local operation by itself. [S09]

| ID | Primary source |
|---|---|
| S01 | Vercel Board overview — https://vercel.com/docs/board |
| S02 | Channels — https://vercel.com/docs/board/concepts/channels |
| S03 | Access control — https://vercel.com/docs/board/access |
| S04 | Threads and message behavior — https://vercel.com/docs/board/concepts/threads |
| S05 | Labels — https://vercel.com/docs/board/concepts/labels |
| S06 | Events — https://vercel.com/docs/board/concepts/events |
| S07 | Tasks — https://vercel.com/docs/board/concepts/tasks |
| S08 | CLI reference — https://vercel.com/docs/board/cli-reference |
| S09 | SDK, schemas, watchers, and local development — https://vercel.com/docs/board/sdk |
| S10 | Platform sync behavior — https://vercel.com/docs/board/concepts/sync |
| S11 | OpenAPI 3.1.2 — https://spec.openapis.org/oas/v3.1.2.html |
| S12 | Apache License 2.0 — https://www.apache.org/licenses/LICENSE-2.0.html |
| S13 | RFC 9457 Problem Details — https://www.rfc-editor.org/rfc/rfc9457.html |
| S14 | PostgreSQL sequence behavior — https://www.postgresql.org/docs/current/functions-sequence.html |
| S15 | PostgreSQL notifications — https://www.postgresql.org/docs/current/sql-notify.html |
| S16 | PostgreSQL locking queries — https://www.postgresql.org/docs/current/sql-select.html |

The appendix maps baseline capabilities to Yak requirements and delivery phases. The accompanying API catalog is a seed for specification work, not a validated OpenAPI contract or an implemented service.

## Appendix A Capability parity matrix

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
