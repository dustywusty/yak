# Abby delegated credentials, version 1 (draft) — optional profile `abby.delegation`

Status: draft for owner review (2026-11-20). Not approved for
implementation. This document will be normative for the `abby.delegation`
profile when the owner accepts it. It extends `spec/protocol.md`
(`abby.p1.core`) and `spec/p2-tasks.md` (`abby.p2.tasks`). Every P1 and P2
requirement still applies, unless a requirement here amends it by ID. The
decision record is ADR-0019; "Q1..Q14" below are the open questions
in ADR-0019. Abby task: `task_sa3li42ffdox4bf5augfdus3he`.

The key words MUST, MUST NOT, SHOULD, and MAY are used as in RFC 2119.
Every MUST maps to a check in the traceability table (§16).

Design baseline: `docs/design/Abby_Design.md` §3.2 (attribution), §4
(authorization, "later OIDC and delegated credentials must use the same
authorization evaluator", "effective rights are the intersection of the
actor's current grants and every credential or delegation restriction"),
§4.2 (revocation), §13 (approval is a task transition by an authorized
actor), Appendix A3 PAR-039 ("delegation only narrows authority;
provenance is retained; revocation affects later API access and
delivery"). PAR-039 is a P4 item; the owner pulled it forward for the
chat-with-agents feature.

## 0 Purpose and limits

A person chats in a viewer and asks an agent to do work for them. The
agent is an external process. It has its own actor and its own
credentials. This profile lets that agent act **on behalf of** the person,
with authority that is never broader than the person's, and with a record
of both identities on every write.

Owner constraints that this profile keeps:

1. Delegation only narrows. No request made under a delegation can do
   something that the person could not do at that moment, or that the
   agent could not do at that moment.
2. Both identities are always recorded. No view, event, or audit record
   shows a delegated write as if the person typed it.
3. Revocation is effective for the next authorization check, including
   later iterations of a long poll (AUTH-004).
4. Some actions need the person's own direct action. A delegation cannot
   do them, whatever its ceiling says.
5. Messages never grant authority (TASK-060, TASK-061). A message that says
   "do this for me" is a request. It is not a delegation.

Out of scope: OIDC and external identity providers, token exchange with
third parties, delegation across servers, and delegation chains (an agent
that delegates to a second agent). Durable automation (subscriptions and
schedules, P3) under delegation is deferred (DLG-095).

## 1 Terms

| Term | Meaning |
|---|---|
| Delegator | The actor who gives authority. In this profile, an actor of kind `human` (DLG-011). Also called the principal. |
| Delegate | The actor who receives authority. An actor of kind `agent` or `service` (DLG-012). |
| Delegation | A server resource that records one delegator, one delegate, a ceiling, an expiry, and a revocation state. It holds no secret. |
| Delegated credential | A credential of the delegate actor that is bound to one delegation. Every request with it is on behalf of the delegator. |
| Direct credential | Any credential that is not bound to a delegation. All P1 and P2 credentials are direct. |
| Authenticating actor | The actor that owns the credential of the request. For a delegated credential, the delegate. |
| Effective actor | The actor whose identity is used for ownership, assignment, and authorship rules. For a delegated credential, the delegator. For a direct credential, the authenticating actor. |
| Ceiling | The limits of a delegation or a delegated credential: boards, channel patterns, scopes, operation classes, expiry. |
| Operation class | A named group of protocol operations (§5). A ceiling lists the classes it permits. |
| Gate task | A task with `direct_action_required: true` (§9). Only a direct credential can settle it. |

## 2 Discovery

- **DLG-001** A server that implements this profile MUST list
  `abby.delegation` in `protocol.profiles` of `GET /v1/server`.
- **DLG-002** Discovery MUST add these `limits`:
  `delegation_max_seconds` (reference 2592000, 30 days),
  `delegation_default_seconds` (reference 86400),
  `delegated_credential_max_seconds` (reference 86400), and
  `max_active_delegations_per_delegator` (reference 100).
- **DLG-003** Discovery MUST add an object `delegation` with
  `operation_classes` (the list in §5, in that order) and
  `never_delegable` (the list of operation names in §6). Clients use it to
  build consent screens. It MUST NOT contain secrets or board data
  (DISC-002).

## 3 The delegation resource

- **DLG-010** A delegation view MUST have these fields:

  | Field | Type | Meaning |
  |---|---|---|
  | `id` | string | Opaque ID (prefix is not normative). |
  | `delegator_actor_id` | string | The person. |
  | `delegate_actor_id` | string | The agent or service. |
  | `label` | string | 0–200 bytes, for people. |
  | `board_ids` | string[] | 1..n boards. Never empty, never "all boards". |
  | `channels` | string[] | 1..n grant patterns (AUTH-011 syntax). |
  | `scopes` | string[] | Channel capabilities (AUTH-010) only. |
  | `operations` | string[] | Operation classes (§5). 1..n. |
  | `expires_at` | timestamp | Required. |
  | `created_at` | timestamp | |
  | `revoked_at` | timestamp \| null | |
  | `revoked_by_actor_id` | string \| null | |
  | `revocation_reason` | string \| null | `revoked`, `renounced`, `issuer_revoked`, `delegator_disabled`, `delegate_disabled`, `administrative`. |
  | `status` | string | Effective: `active`, `expired`, or `revoked`. Computed at read time, like a lapsed claim (CLAIM-050). |

  `scopes` MUST NOT contain `*`, `board_admin`, or `server_admin`
  (DLG-060). Sets are sorted and de-duplicated (ADR-0003 normalization).
- **DLG-011** The delegator MUST be an actor of kind `human`. A request
  whose effective actor is of another kind MUST get `403 forbidden`.
- **DLG-012** The delegate MUST be an existing, enabled actor of kind
  `agent` or `service`, and MUST NOT be the delegator. Otherwise `400
  validation_failed`. The server MUST NOT reveal whether the delegate can
  read any board (no membership check, no error that depends on it).
- **DLG-013** A delegation is immutable after creation, except for
  revocation. To change a ceiling, revoke and create a new delegation.
  There is no `PATCH`.
- **DLG-014** A delegation is a server-level resource, not a board
  resource. It does not appear in any board event stream. It appears in
  board audit (DLG-131).

## 4 Creation, consent, and presentation

### 4.1 Creating a delegation

- **DLG-020** `POST /v1/delegations` creates a delegation. The caller
  MUST authenticate with a **direct** credential that has `may_delegate:
  true` (DLG-036). The delegator is the caller; a body field cannot name another delegator (AUTH-002). Body:
  `{delegate_actor_id, label?, board_ids, channels, scopes, operations,
  expires_in_seconds?}`. It MUST carry `Idempotency-Key` (IDEM-001).
  Response `201` with the delegation view.
- **DLG-021** The ceiling MUST be within the calling credential's ceiling:
  every scope in the caller's scopes (or caller has `*`), every board in
  the caller's `board_ids` (if the caller is board-restricted). Otherwise
  `403 forbidden` (the AUTH-005 rule, applied to delegation).
- **DLG-022** Each listed board MUST be a board the caller can see
  (AUTH-016). A board the caller cannot see MUST give `404 not_found`, the
  same as for any hidden board. Channel patterns are checked for syntax
  only. The server MUST NOT check that a pattern matches an existing
  channel (that would leak paths).
- **DLG-023** `expires_in_seconds` defaults to
  `delegation_default_seconds` and MUST be 60..`delegation_max_seconds`.
  The stored `expires_at` MUST NOT be later than the calling credential's
  `expires_at` (same rule as a self-issued credential).
- **DLG-024** The delegation records the calling credential as its
  issuing credential. If that credential is revoked, or its own issuance
  chain is broken, the delegation MUST stop authorizing at the next check,
  as if revoked with reason `issuer_revoked`. (Open question Q3.)
- **DLG-025** Consent. The delegator consents by creating the delegation.
  The delegate consents by minting a delegated credential (§4.2). Until
  then, the delegation authorizes nothing. A client that offers delegation
  in a viewer SHOULD show the full ceiling in plain words before it sends
  the request, using DLG-003 for the vocabulary.
- **DLG-026** A delegated credential MUST NOT create a delegation
  (`403 forbidden`, code `not_delegable`). There is no re-delegation.
- **DLG-027** More than `max_active_delegations_per_delegator`
  unrevoked, unexpired delegations → `413 limit_exceeded`.

### 4.2 Minting a delegated credential

- **DLG-030** `POST /v1/delegations/{id}/credentials` issues a delegated
  credential. The caller MUST be the delegate and MUST authenticate with a
  direct credential that has `may_delegate: true`. The delegation MUST be `active`. Otherwise `404
  not_found` (the delegation is hidden from other actors) or `409
  invalid_transition` (expired or revoked). It MUST carry
  `Idempotency-Key`. Body: `{label?, board_ids?, channels?, scopes?,
  operations?, expires_in_seconds?}`. Absent fields copy the delegation.
- **DLG-031** Each given field MUST be a subset of the delegation's field.
  A channel pattern is a subset if every path it can match is matched by
  some delegation pattern. Otherwise `403 forbidden`. The delegated
  credential's scopes and boards MUST also be within the minting
  credential's ceiling (AUTH-005).
- **DLG-032** `expires_at` = min(now + `expires_in_seconds`, delegation
  `expires_at`, minting credential `expires_at`). `expires_in_seconds`
  MUST be 60..`delegated_credential_max_seconds`.
- **DLG-033** The response is `201` with an `IssuedCredential` (AUTH-003:
  token shown once; replay → `token_unavailable: true`). The token format
  is the ordinary bearer token. The `Credential` view MUST add
  `delegation_id`, `on_behalf_of_actor_id`, `channels`, and `operations`.
  For a direct credential these four fields are `null`.
- **DLG-034** The minting credential is the delegated credential's
  issuer. Revoking the minting credential MUST revoke the delegated
  credential at the next check (the existing same-actor issuance chain).
- **DLG-035** `POST /v1/credentials` with a delegated credential MUST
  return `403 forbidden`, code `not_delegable`. A delegated credential
  cannot mint, list other credentials, or revoke credentials other than
  itself.

### 4.3 Credential-management restriction (shared with ADR-0018, conflict C1)

ADR-0018 found that any credential can issue child credentials and revoke
the person's other credentials (its conflict C1). A stolen operator token
can therefore revoke the person's CLI token. The same gap would let a
stolen operator token create delegations. This profile closes both with
one additive pair of credential attributes. They are part of this profile
so that ADR-0018 and ADR-0019 use one design. A server MAY implement them
without the rest of `abby.delegation`; it then lists the capability
`abby.credentials.restrictions` in discovery.

- **DLG-036** A credential has two boolean attributes:
  `may_manage_credentials` and `may_delegate`. Both appear in every
  `Credential` view. `POST /v1/credentials` accepts both as optional
  fields.
  - `may_manage_credentials: false` → the credential MUST NOT issue
    (`POST /v1/credentials`), list (`GET /v1/credentials`), or revoke a
    credential other than itself. It MAY revoke itself (logout).
  - `may_delegate: false` → the credential MUST NOT create or revoke a
    delegation, or mint a delegated credential (§4.1, §4.2, §8). It MAY
    read the delegations of its actor.
  - A refusal returns `403 forbidden` with code `credential_restricted`.
- **DLG-037** Defaults keep P1 behavior: a credential created without the
  fields gets the issuer's values (`true` for every credential that
  exists before this change). A credential MUST NOT be issued with
  `true` for an attribute that the issuing credential has as `false`
  (`403 forbidden`; the AUTH-005 narrowing rule).
- **DLG-038** Every delegated credential has both attributes `false`. A
  request to mint one with `true` MUST return `400 validation_failed`.
- **DLG-039** Operator-mode tokens (ADR-0018 option A) SHOULD be minted
  with `may_manage_credentials: false`. Whether they get `may_delegate:
  true` is open question Q2 in ADR-0019.

### 4.4 Presenting a delegation

- **DLG-040** A request presents a delegation only by its bearer token.
  There is no header, query parameter, or body field that selects a
  delegation or a principal. A request with a direct credential is never
  on behalf of anyone. A server MUST reject any request header named
  `Abby-On-Behalf-Of` with `400 validation_failed`, so that clients do not
  assume a header model.
- **DLG-041** `GET /v1/me` with a delegated credential MUST return
  `actor` (the delegate), `credential` (with `delegation_id`),
  `delegation` (the view), and `on_behalf_of` (the delegator's actor
  view). With a direct credential, `delegation` and `on_behalf_of` are
  `null`.

## 5 Operation classes

Every operation in P1 and P2 belongs to exactly one class, or is never
delegable (§6). A request under delegation MUST be rejected with `403
forbidden` if its class is not in both the delegation's and the delegated
credential's `operations` (**DLG-050**). Reads of hidden resources stay
`404` (AUTH-015); the class check applies after visibility.

| Class | Operations (P1, P2) | Capabilities still required |
|---|---|---|
| `read` | Every `GET` of board content; `POST …/snapshots`; `GET …/permissions`; `GET …/actors` (participants); event long polls | `read` |
| `message.post` | Create thread; reply | `read`, `post` (+ `channel_manage` and `channel.create` for an implicit channel) |
| `message.edit` | Edit message; edit thread title (non-task thread) | as P1 |
| `message.delete` | Delete own message | as P1 |
| `message.label` | Change labels | `read`, `label` |
| `channel.create` | `POST …/channels` | `channel_manage` |
| `task.create` | Create task; batch; convert thread | as L1, L2 |
| `task.claim` | Claim; claim-next; renew; release; claimant block (L10 as C) | `read`, `claim` |
| `task.settle` | Complete; fail | `read`, `claim` |
| `task.manage` | Assign; replace dependencies; manager block, unblock, cancel, reopen; brief update; edit task title | as P2 manager |

- **DLG-051** Reactions (P3) will be class `message.react`. A new
  operation added by a later profile MUST name its class or be listed as
  never delegable. Until it does, it MUST be never delegable (fail closed).
- **DLG-052** `operations` MUST include `read`. A delegation without
  `read` → `400 validation_failed` (every write needs current `read`).

## 6 Never delegable

- **DLG-060** These operations MUST return `403 forbidden` with code
  `not_delegable` for every delegated credential, whatever its ceiling and
  whatever rights both actors hold:

  1. Server administration: create actors, create boards, any server
     admin route, recovery, bootstrap. A delegated credential MUST NOT have
     `server_admin`, even when both actors are server administrators.
  2. The `board_admin` role: grants, board edits, board audit, redaction of
     another actor's message, archive export and import (ADR-0017).
  3. Credentials: issue (`POST /v1/credentials`), list, and revoke, except
     revoke of the calling delegated credential itself.
  4. Delegations: create, mint, list (except `GET /v1/me`), revoke (except
     renounce by the delegate, DLG-080, which needs a direct credential).
  5. Task root redaction (L18). It destroys content and cancels work.
  6. Task owner change (`PATCH …/tasks/{id}` with `owner_actor_id`). It
     moves accountability away from the person.
  7. Any operation on a gate task that §9 reserves for direct action.

- **DLG-061** A rejection under DLG-060 MUST commit nothing and MUST NOT
  reserve an idempotency key (IDEM-004).

## 7 Effective authority

- **DLG-070** For a request with delegated credential C, bound to
  delegation D, delegate A, delegator P, an operation op on board b and
  channel path x is allowed only if **all** of these hold at the time of
  the check:

  1. C is valid (AUTH-001), not expired, not revoked, and its issuance
     chain is intact (DLG-034).
  2. D is `active`: not expired, not revoked, its issuing credential and
     chain intact (DLG-024), P and A both enabled.
  3. op is not never delegable (DLG-060) and not reserved by the gate rule
     (§9).
  4. class(op) ∈ D.operations ∩ C.operations.
  5. b ∈ D.board_ids ∩ C.board_ids.
  6. x matches some pattern in D.channels and some pattern in C.channels,
     under the private-boundary rule (DLG-072).
  7. For each capability cap that op needs: cap ∈ D.scopes ∩ C.scopes,
     **and** P holds cap on x under P's current grants (AUTH-010..014),
     **and** A holds cap on x under A's current grants.
  8. Every ownership, authorship, eligibility, and manager condition of
     op is evaluated for the effective actor P, with the amendments in
     §10 and §11.

  The reference server MUST implement this with the same evaluator as
  direct credentials (design §4): evaluate P's policy and A's policy with
  the existing function and combine them with AND.
- **DLG-071** **Both actors' grants.** Step 7 is the intersection of the
  delegator's and the delegate's current rights. A deny on either actor
  wins. A private boundary that admits only one of them admits neither.
  (ADR-0019 explains why this is not "delegator only".)
- **DLG-072** **Private boundary in a ceiling.** A delegation channel
  pattern reaches into a private channel B only if the pattern's base path
  is within subtree(B) (the AUTH-013 rule). So `*` never reaches a private
  channel. To delegate inside a private channel, the person names it or a
  descendant pattern.
- **DLG-073** Board metadata visibility (AUTH-016) under delegation: a
  board is visible only if it is in the ceiling and both P and A hold an
  allow grant on it. Other boards are `404`.
- **DLG-074** `GET …/permissions?channel=P` with a delegated credential
  MUST return the capabilities that DLG-070 steps 5–7 allow, plus
  `delegation_id` and the effective `operations` (D ∩ C). `board_admin`
  MUST be `false`.
- **DLG-075** The policy fingerprint that binds cursors and snapshot
  sessions (EVT-009, SNAP-003) MUST cover P's grants, A's grants, D's and
  C's ceilings, and D's state. A change in any of them MUST give `409
  resync_required` with `reason: "policy_changed"`. Revocation or expiry
  of D or C MUST give `401 unauthenticated` (AUTH-004).

## 8 Revocation and lifetime

- **DLG-080** `DELETE /v1/delegations/{id}` revokes a delegation. The
  delegator (direct credential) and a server administrator MAY revoke.
  The delegate MAY renounce it with a direct credential (reason
  `renounced`). Others get `404`. Repeating it returns the revoked view.
  Revocation MUST be audited (DLG-130, DLG-131).
- **DLG-081** Revocation, expiry, `issuer_revoked`, and disabling of
  either actor MUST take effect for every authorization check that starts
  after the change commits, including later wakes of a long poll (AUTH-004,
  EVT-011). Every delegated credential of the delegation fails with `401
  unauthenticated`. In-flight operations authorized earlier MAY finish.
- **DLG-082** Narrowing by loss of rights: if P or A loses a grant, gains a
  deny, or a channel becomes private, the effective authority narrows at
  the next check. No delegation state changes.
- **DLG-083** The delegator MAY revoke a single delegated credential of
  their delegation with `DELETE /v1/credentials/{id}`. This is an
  exception to the "own credentials only" rule of P1.
- **DLG-084** Expired and revoked delegations MUST stay readable to the
  delegator and delegate for at least `retention.events_seconds`, so that
  attribution in history can be explained.

## 9 Gate tasks and the human-gate rule

The P2 profile has no approval resource. Two approval shapes are in use or
in draft, and both are ordinary task transitions:

- Review task (BUILD_ABBY_WITH_ABBY BOOT-03): approval is the
  **completion** of a review task by the reviewer.
- Operator-mode gate (ADR-0018 §7, OPR-501): a `blocked` task with root
  label `gate=pending`; approval is the **unblock** by a manager, then the
  label `gate=approved`.

ADR-0018 keeps `gate` as a board label convention that the server does not
know. A server cannot protect a convention it does not know. So this
profile adds one task field that marks a task as a gate for both shapes.
(Open question Q7 covers the label-only alternative.)

- **DLG-090** A task MAY have `direct_action_required: true` (a "gate
  task"). It is set at creation (`POST …/tasks`, batch item) or by
  `PATCH …/tasks/{id}` by a manager. Setting or clearing it MUST need a
  direct credential. The default is `false`. This amends ADR-0016
  (`Task`, `CreateTaskRequest`, `PatchTaskRequest`). The field is in
  every task view and in `task.created` / `task.updated` payloads. An
  agent that opens a gate (ADR-0018 §6.4) SHOULD set the field when it
  creates the task, or ask the owner to set it.
- **DLG-091** For a gate task, these operations MUST return `403
  forbidden` with code `direct_action_required` when the credential is
  delegated: **unblock**, claim, renew, release, complete, fail,
  claimant block, brief update, `PATCH` of the flag, and any change to the
  root message labels. Claim-next under a delegated credential MUST NOT
  select a gate task (it is not a candidate, SEL-002).
- **DLG-092** Under a delegated credential, a dependency replacement
  (L15) that removes a gate task from a task's prerequisites MUST return
  `403 forbidden` with code `direct_action_required`. Adding a gate
  prerequisite is allowed.
- **DLG-093** Under a delegated credential, manager block, cancel,
  reopen, and assignment of a gate task are allowed. None of them can
  approve: a blocked or canceled gate never satisfies a dependent
  (DEP-004), and a reopened one is still a gate.
- **DLG-094** "Direct" means "not delegated". It does not prove that a
  human pressed a button (see §10.1). A deployment that wants that
  guarantee keeps the person's direct credentials in the person's own
  client (for example the operator-mode page of ADR-0018), not in agent
  processes.
- **DLG-096** A client that shows approvals (OPR-502, OPR-602) MUST NOT
  show a `task.unblocked` or `task.completed` event as a person's approval
  if its `on_behalf_of_actor_id` is not `null`. It shows "A for P" and no
  approval badge. (This rule protects tasks that were not marked as gates.)
- **DLG-097** Until a server implements DLG-090, a delegator SHOULD NOT
  put `task.manage` or `task.settle` in a delegation whose channels hold
  gates or review tasks. Clients that create delegations SHOULD warn.

Durable automation (P3) is not delegable in this version:

- **DLG-095** When P3 is present, creating a subscription or schedule with
  a delegated credential MUST return `403 forbidden`, code
  `not_delegable`. (Open question Q8.)

## 10 Attribution

The design says: "record the effective actor, authenticating actor when
delegated, credential identifier, and optional run ID separately" (§3.2).
This profile records all four. The field that already exists on every
record (`actor_id`, `author_actor_id`) keeps its P1 meaning: **the actor
that authenticated**. A new field names the principal. ADR-0019 explains
the choice (old clients must never show an agent's words as the person's).

- **DLG-100** Every record of a write made under delegation MUST carry
  `on_behalf_of_actor_id` = P and `delegation_id` = D, next to the
  existing actor field = A and `credential_id` = C. For a direct write both
  new fields are `null`. This applies to:

  | Record | Existing field (= A) | New fields |
  |---|---|---|
  | `Event` | `actor_id` | `on_behalf_of_actor_id`, `delegation_id` (top level) |
  | `Message` | `author_actor_id` | `on_behalf_of_actor_id` (top level); `attribution.delegation_id` |
  | `Claim` | `actor_id` | `on_behalf_of_actor_id`, `delegation_id` |
  | `Task` | `created_by_actor_id` | `created_on_behalf_of_actor_id` |
  | `Brief` | `created_by_actor_id` | `created_on_behalf_of_actor_id` |
  | `Task.block` | `by_actor_id` | `on_behalf_of_actor_id` |
  | `AuditEvent` | `actor_id` | `on_behalf_of_actor_id`, `delegation_id` |

- **DLG-101** The fields MUST be present (as `null` when direct) in every
  view, list item, snapshot item, and event payload that embeds the
  record. They MUST NOT be removed by redaction: a tombstone keeps its
  attribution (MSG-003).
- **DLG-102** A message edit under delegation MUST record its own
  attribution on the `message.edited` event. The message keeps the
  attribution of its creation.
- **DLG-103** The event filter vocabulary (EVT-005) gains
  `on_behalf_of` (actor ID). `author_id` keeps matching the authenticating
  actor only. A viewer that wants "everything for Dusty" uses
  `author_id=Dusty` and `on_behalf_of=Dusty` in two reads. (Open question
  Q6: an OR filter.)
- **DLG-104** A client that renders attribution SHOULD show both actors,
  for example "abby-bridge for Dusty". It MUST NOT show the delegator
  alone.
- **DLG-105** Mentions (MEN-001) resolve as today. A delegated write MUST
  NOT resolve a mention that the effective authority could not resolve
  (both P and A must be able to read the channel for the mention to be
  checked; eligibility of the mentioned actor is unchanged).

### 10.1 What a reader can verify (ADR-0018 conflict C5)

ADR-0018 found that a reader cannot prove that a write came "from the
browser": `run_id` is client data, and other actors cannot read a
credential's label. The same question applies here: "did the person do
this, or an agent for the person?"

- **DLG-106** The server derives `on_behalf_of_actor_id` and
  `delegation_id` only from the credential that it authenticated (DLG-040).
  No client input can set, clear, or change them. A reader can therefore
  trust exactly one provenance fact: **a write was direct, or it was
  delegated by P to A through D.** This is the only provenance fact that
  this profile guarantees.
- **DLG-107** `run_id` (`Abby-Run-Id`) stays client data with no
  authority (AUTH-002). Clients MUST NOT show a `run_id` prefix (for
  example `operator/…` from OPR-603) as proof of device, client, or human
  presence. A client MAY show it marked "unverified".
- **DLG-108** The profile does not add a "from the browser" or "human
  present" fact. Any such fact is either client data or a property of
  where a person keeps a direct token, which the server cannot see. (Open
  question Q13 covers a server-stored, issuer-declared credential
  purpose.)

## 11 Ownership, authorship, claims

- **DLG-110** **Task owner.** A task created under delegation has
  `owner_actor_id` = P (the effective actor is accountable) and
  `created_by_actor_id` = A. Manager status (P2 §1) is evaluated for P:
  P is owner, and both P and A hold `task_manage` (DLG-071).
- **DLG-111** **Message authorship.** Under delegation, edit and delete
  are allowed only on messages with `author_actor_id` = A and
  `on_behalf_of_actor_id` = P. The delegate cannot edit the person's own
  direct messages. A direct request by P MAY edit or delete a message with
  `on_behalf_of_actor_id` = P (the person is accountable for it). A direct
  request by A MUST NOT edit a message written on behalf of anyone.
- **DLG-112** **Root authorship** (thread conversion L2, thread title)
  follows DLG-111: the "root author" for a delegated caller is the pair
  (A, P).
- **DLG-113** **Assignment eligibility** (TASK-052) uses the effective
  actor P. A task assigned to P is eligible for A acting for P. A task
  assigned to A is not eligible for A acting for P; A uses a direct
  credential for that work.
- **DLG-114** **Claim identity.** A claim made under delegation records
  `actor_id` = A, `on_behalf_of_actor_id` = P, `delegation_id` = D,
  `credential_id` = C. The proof goes to the caller (A's process).
- **DLG-115** **Claimant operations** (renew, release, complete, fail,
  claimant block) MUST need the same pair (A, P): A's credential, delegated
  for P, through any active delegation from P to A. This amends TASK-042
  and TASK-043 ("same actor" means "same authenticating actor and same
  effective actor"). P's direct credential with a valid proof MUST get
  `403 forbidden` (TASK-043). A's direct credential with a valid proof
  MUST get `403 forbidden`.
- **DLG-116** A claim is not revoked when its delegation ends. It lapses at
  `expires_at` (TASK-042), or a manager blocks the task. Runners MUST stop
  their managed process on `401` from a renew (CLAIM-055). (Open question
  Q5.)
- **DLG-117** `assignee=me` in claim-next and task lists means the
  effective actor P under delegation.

## 12 Idempotency

- **DLG-120** The idempotency key scope (IDEM-002) becomes
  (authenticating actor, effective actor, namespace, method, path, key).
  For a direct credential the two actors are equal, so P1 and P2 scopes
  are unchanged. A's direct requests and A's requests for P never share a
  receipt. Two delegations from the same P to the same A share a scope, so
  a retry across a credential rotation does not run twice.
- **DLG-121** A replay (IDEM-005, CLAIM-041) MUST recheck the current
  authority of the replaying credential under DLG-070. If it is no longer
  allowed, the replay returns that error and does not execute again.
- **DLG-122** Delegation creation and delegated credential issuance are
  server-namespace creates. Their replay returns the current view; a
  delegated credential replay returns `token_unavailable: true`.

## 13 Audit

- **DLG-130** These MUST write a server-level audit record: delegation
  create, revoke, renounce, `issuer_revoked` cascade, and delegated
  credential issue and revoke. Metadata: delegator, delegate, ceiling,
  expiry, reason. No token, no verifier.
- **DLG-131** Delegation create and revoke MUST also write one board audit
  record on each board in `board_ids`, so that board administrators can
  see which agents can act for which members. The record MUST NOT list
  the other boards of the delegation.
- **DLG-132** Every board audit record for an action made under
  delegation MUST carry `on_behalf_of_actor_id` and `delegation_id`
  (DLG-100).
- **DLG-133** `GET /v1/delegations?role=delegator|delegate` lists the
  caller's delegations (direct credential only) in `(created_at, id)`
  order with keyset tokens (PAGE-001). `GET /v1/delegations/{id}` reads
  one. Other actors get `404`. Server administrators MAY list all with
  `GET /v1/delegations?all=true`.

## 14 Errors (additions)

| Status | Code | Meaning |
|---|---|---|
| 403 | `not_delegable` | The operation can never be done with a delegated credential (§6). |
| 403 | `credential_restricted` | The credential has `may_manage_credentials` or `may_delegate` set to `false` (DLG-036). |
| 403 | `direct_action_required` | The operation on a gate task needs a direct credential (§9). |
| 403 | `forbidden` | Outside the ceiling, or one of the two actors lacks the capability. |
| 401 | `unauthenticated` | Delegated credential, its delegation, or either actor is no longer valid. |

- **DLG-140** A ceiling miss MUST NOT reveal which part of DLG-070 failed
  in a way that exposes the other actor's grants. The `detail` MAY say
  "outside the delegation" or "not permitted"; it MUST NOT say "the
  delegate lacks read on channel X" when the caller cannot otherwise know
  that X exists.

## 15 HTTP routes

| Method | Path | Who | Body / result |
|---|---|---|---|
| POST | `/v1/delegations` | Delegator, direct credential, `Idempotency-Key` | `CreateDelegationRequest` → 201 `Delegation` |
| GET | `/v1/delegations` | Delegator or delegate, direct credential | query `role`, `status`, `page_token`, `limit` → `DelegationList` |
| GET | `/v1/delegations/{id}` | Delegator, delegate, server admin | `Delegation` |
| DELETE | `/v1/delegations/{id}` | Delegator, delegate (renounce), server admin; direct credential | → 200 `Delegation` (revoked) |
| POST | `/v1/delegations/{id}/credentials` | Delegate, direct credential, `Idempotency-Key` | `CreateDelegatedCredentialRequest` → 201 `IssuedCredential` |

Example: a person delegates to the agent bridge for one board, two
channels, post and task work, but not settle.

```json
POST /v1/delegations
Idempotency-Key: 6f1c0b9e-delegate-bridge
{
  "delegate_actor_id": "act_bridge",
  "label": "chat requests from the viewer",
  "board_ids": ["brd_dev"],
  "channels": ["spec", "server/*"],
  "scopes": ["read", "post", "label", "claim", "task_manage"],
  "operations": ["read", "message.post", "message.label", "task.create", "task.claim"],
  "expires_in_seconds": 86400
}
```

An event written by the bridge for the person:

```json
{
  "id": "evt_…", "type": "message.created",
  "actor_id": "act_bridge",
  "on_behalf_of_actor_id": "act_dusty",
  "delegation_id": "dlg_…",
  "credential_id": "cred_…",
  "run_id": "bridge-req-42",
  "resource": {"type": "message", "id": "msg_…", "revision": 1},
  "…": "…"
}
```

## 16 Traceability

`CT` = black-box conformance test in `conformance/` (planned names). `REF`
= reference-server test in `internal/` (planned). Scenario IDs use the
prefix `CT-DLG-` so that they cannot collide with numbers that other
drafts take (ADR-0017 uses CT-039..048; ADR-0018, PR #6, is a draft); the
owner can renumber them on acceptance.

| ID | Scenario | Required result |
|---|---|---|
| CT-DLG-01 | Narrow, never wider | Delegation with more scopes or boards than the issuing credential → 403; delegated credential wider than delegation → 403 |
| CT-DLG-02 | Intersection of both actors | Agent with no grant on channel X, person with grant → 404 on X; person denied, agent allowed → 404; both allowed → 200 |
| CT-DLG-03 | Private boundary | `*` channel pattern does not reach private channel; explicit pattern does, when both actors are allowed inside |
| CT-DLG-04 | Operation classes | `task.settle` absent → complete 403; `message.post` present → reply 201 |
| CT-DLG-05 | Never delegable | Each §6 route with a delegated credential → 403 `not_delegable`, nothing committed, key not reserved |
| CT-DLG-06 | Revocation | Revoke delegation during a 30 s long poll → next wake 401; next request 401; delegator grant removal → 403/404 at next check |
| CT-DLG-07 | Cascades | Revoke delegator's issuing credential → delegated credential 401; revoke delegate's minting credential → 401; disable either actor → 401 |
| CT-DLG-08 | Attribution | Message, event, claim, task, brief, audit carry `on_behalf_of_actor_id` and `delegation_id`; direct writes carry nulls; tombstone keeps them |
| CT-DLG-09 | Authorship | Delegate cannot edit person's direct message; can edit its own on-behalf message; person can edit on-behalf message; delegate direct cannot |
| CT-DLG-10 | Claims under delegation | Claim records A, P, D; P's direct credential with proof → 403; A direct with proof → 403; same pair through a new delegation → 200 |
| CT-DLG-11 | Assignment | Task assigned to P claimable by A-for-P; task assigned to A not claimable by A-for-P (403 `not_assignee`) |
| CT-DLG-12 | Gate task | Delegated unblock/claim/complete/fail/brief/root-label on gate → 403 `direct_action_required`; claim-next skips it; removing gate prerequisite → 403; block, cancel allowed |
| CT-DLG-13 | Idempotency scope | Same key from A direct and A-for-P → two independent operations; same key across two delegations P→A → one operation |
| CT-DLG-14 | No header impersonation | `Abby-On-Behalf-Of` header → 400; body field naming a delegator → 400 |
| CT-DLG-15 | Discovery and /me | Profile, limits, `delegation` object; `/me` shows delegate, delegation, principal |
| CT-DLG-16 | Resync | Change of A's grants invalidates a cursor bound to a delegated credential → 409 `resync_required` |
| CT-DLG-17 | Visibility of delegations | Third actor → 404 on read and revoke; board audit on each listed board shows create and revoke without other board IDs |
| CT-DLG-18 | Credential restrictions | `may_manage_credentials: false` → issue, list, revoke-other 403 `credential_restricted`, revoke-self 200; `may_delegate: false` → delegation create and mint 403; child cannot widen either flag |

| Requirement | Verification |
|---|---|
| DLG-001..003 | CT-DLG-15 (TestDelegationDiscovery); schema checks |
| DLG-010..014 | CT TestDelegationCreateView; schema checks; CT-DLG-17 |
| DLG-020..023, DLG-027 | CT-DLG-01 (TestDelegationCeiling); CT TestDelegationLimits |
| DLG-024 | CT-DLG-07 (TestDelegationCascades) |
| DLG-025 | CT TestDelegationInertUntilMinted (no credential → nothing authorized); viewer review of consent screen (manual) |
| DLG-026, DLG-035 | CT-DLG-05 (TestNeverDelegable) |
| DLG-030..034 | CT-DLG-01; CT-DLG-07; CT TestDelegatedCredentialIssue |
| DLG-036..039 | CT-DLG-18 (TestCredentialRestrictions) |
| DLG-040..041 | CT-DLG-14 (TestNoHeaderImpersonation); CT-DLG-15 |
| DLG-050..052 | CT-DLG-04 (TestOperationClasses); REF TestEveryRouteHasClass (route table exhaustive) |
| DLG-060..061 | CT-DLG-05 |
| DLG-070..071 | CT-DLG-02 (TestDelegationIntersection); REF internal/authz TestDelegatedPolicy* |
| DLG-072 | CT-DLG-03 (TestDelegationPrivateBoundary) |
| DLG-073..074 | CT TestDelegatedBoardVisibility; CT TestDelegatedPermissions |
| DLG-075 | CT-DLG-16 (TestDelegatedCursorResync) |
| DLG-080..084 | CT-DLG-06 (TestDelegationRevocation); CT-DLG-07; CT-DLG-17 |
| DLG-090..094 | CT-DLG-12 (TestGateTaskDirectAction) |
| DLG-096..097 | Viewer review (operator mode approval list); VWR test that a delegated `task.unblocked` shows no approval badge |
| DLG-095 | CT TestNoDelegatedAutomation (P3 servers only) |
| DLG-100..102 | CT-DLG-08 (TestDelegatedAttribution); schema checks |
| DLG-106..108 | CT-DLG-14 (no client input changes provenance); viewer review of `run_id` display |
| DLG-103 | CT TestOnBehalfOfFilter |
| DLG-104 | Viewer and CLI review (manual); `abby read` shows "A for P" |
| DLG-105 | CT TestDelegatedMentions |
| DLG-110..113, DLG-117 | CT-DLG-09 (TestDelegatedAuthorship); CT-DLG-11 (TestDelegatedAssignment) |
| DLG-114..116 | CT-DLG-10 (TestDelegatedClaims) |
| DLG-120..122 | CT-DLG-13 (TestDelegatedIdempotencyScope) |
| DLG-130..133 | CT-DLG-17 (TestDelegationAudit) |
| DLG-140 | CT TestDelegatedErrorsNoLeak |

## 17 Amendments to other documents (on acceptance)

| Document | Change |
|---|---|
| `spec/protocol.md` AUTH-002 | "The acting identity comes only from the credential" now includes the delegation bound to it. |
| `spec/protocol.md` AUTH-005 | Effective rights add the delegation ceiling and the delegate's grants (DLG-070). |
| `spec/protocol.md` IDEM-002 | Key scope adds the effective actor (DLG-120). |
| `spec/protocol.md` EVT-003, EVT-005 | New event fields (DLG-100) and the `on_behalf_of` filter (DLG-103). |
| `spec/p2-tasks.md` TASK-042, TASK-043, TASK-052 | Same-pair rule (DLG-115); eligibility by effective actor (DLG-113). |
| `spec/protocol.md` AUTH-005, `POST /v1/credentials` | `may_manage_credentials`, `may_delegate` (DLG-036..038); closes ADR-0018 C1. |
| `spec/operator-mode.md` OPR-502, OPR-602, OPR-603 | Approval display rule DLG-096; `run_id` is never proof (DLG-107). |
| ADR-0016 | `direct_action_required` on Task; attribution fields on Task, Brief, Claim, block. |
| `docs/design/Abby_Design.md` §15, A3 | PAR-039 moves from P4 to the chat-with-agents milestone. |
