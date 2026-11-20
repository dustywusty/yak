# ADR-0019 Delegated credentials: an agent acts on behalf of a person

Status: Accepted (owner, 2026-11-20). Normative text: `spec/delegation.md`
(DLG-001..140, CT-DLG-01..18). Builds on design §3.2, §4, §4.2, §13,
PAR-039; ADR-0002; ADR-0003; ADR-0016. Designed together with ADR-0018
(operator mode, accepted) and closes its conflicts C1 and C5. Abby task:
`task_sa3li42ffdox4bf5augfdus3he`.

## Context

The owner wants to chat in the viewer and ask agents to do things for
them. Agents are external processes with their own actors and
credentials (`examples/build-abby`). An agent bridge will listen for
mentions and hand each request to an agent command. ADR-0018 covers a
person who writes directly from the browser. This ADR covers an agent
that writes **as the person's agent**.

Without delegation, the bridge acts with its own rights. Its rights are
usually broader than the person's. Then a person with read-only access
can ask the bridge to post or to complete a task, and the bridge does it.
That is a confused deputy. The design already asks for the fix: "effective
rights are the intersection of the actor's current grants and every
credential or delegation restriction", "later … delegated credentials
must use the same authorization evaluator", and "record the effective
actor, authenticating actor when delegated, credential identifier, and
optional run ID separately".

Current code: a credential has `scopes`, `board_ids`, `expires_at`, and
`issued_by_credential_id`. Self-issuance cannot widen the ceiling or
outlive the issuer. Revoking a credential revokes what its actor minted
from it (same-actor chain in `Authenticate`). `internal/authz.Policy.Has`
is a pure evaluator of one actor's grants plus one ceiling.

## Decisions

### 1 A delegation is a resource; a delegated credential presents it

The person creates a **delegation** (no secret): delegator, delegate,
boards, channel patterns, capability scopes, operation classes, expiry.
The agent then mints a **delegated credential** from it with its own
direct credential. Every request with that token is on behalf of the
person. Nothing in a request header or body can select a principal.

| Option | Summary | Verdict |
|---|---|---|
| A. Header on the agent's own token (`Abby-On-Behalf-Of: dlg_…`) | No new secret; one bridge token serves many people | Rejected |
| B. Delegated credential bound to a delegation | Ordinary bearer token; the delegation is in the credential | **Chosen** |
| C. Delegator mints a token for the agent | Person's client creates the secret and hands it over | Rejected |
| D. RFC 8693 token exchange / OIDC | Standard, external IdP | Later (P4) |

Why B: the bridge can give each spawned agent command a short, narrow
token (one channel, `task.claim` only, 30 minutes). With A, the command
would get the bridge's own token; it could drop the header and act with
the bridge's full rights. B keeps the token format and AUTH-001 path
unchanged, and revocation, expiry, and issuance chains already work for
credentials. Why not C: the secret would have to travel from the person's
client to the agent (through chat, a file, or a paste). In B, the secret
is created in the process that uses it.

Consent is two-sided: the person consents by creating the delegation;
the agent consents by minting. A delegation without a minted credential
authorizes nothing.

### 2 Effective authority = both actors' current rights ∩ both ceilings

| Option | Verdict |
|---|---|
| Delegator only: P's grants ∩ ceilings | Rejected |
| **Intersection: P's grants ∩ A's grants ∩ D ∩ C** | **Chosen** |
| Delegator's allows, plus delegate's denies only | Rejected (two evaluators, hard to explain) |

Why intersection:

1. **Board policy about agents stays true.** If a board administrator
   denies agent A on `security`, a member cannot route around it with a
   delegation. A private channel that admits only P does not admit A.
2. **No exfiltration path.** A member cannot give an outside agent (no
   grants on the board) access to board content that board admins never
   approved.
3. **Same evaluator.** The reference server evaluates
   `Policy.Has` for P and for A, with the ceilings, and ANDs the results.
   No new policy language.
4. **Fails closed.** When in doubt, both must agree.

Cost: an agent needs its own grants wherever it acts for someone. That is
already true for the build-abby agents. The value of delegation is then
(a) the confused-deputy guard (the agent never exceeds the person), (b)
the person's ownership, assignment, and manager status, and (c) honest
attribution.

The channel ceiling uses the private-boundary rule (AUTH-013): `*` in a
delegation never reaches a private channel. The person names it.

### 3 Operation classes, and what is never delegable

Capabilities are too coarse for consent: `claim` covers both "work on
it" and "complete it"; `post` covers posting and deleting own messages.
A delegation therefore lists **operation classes** too (`read`,
`message.post`, `message.edit`, `message.delete`, `message.label`,
`channel.create`, `task.create`, `task.claim`, `task.settle`,
`task.manage`). Scopes keep the existing evaluator; classes are one extra
check. A new operation is never delegable until it names a class.

Never delegable: server administration; the `board_admin` role (grants,
board edits, audit, redaction of others, archive); credential issue,
list, revoke; delegation create, mint, revoke; task root redaction; task
owner change; and the gate-reserved operations (§5). No re-delegation.

Rejected: `max_uses`. A "use" is ill-defined for reads, long polls, and
idempotent replays. Expiry, operation classes, and revocation are enough.

### 4 Attribution: `actor_id` stays the authenticating actor

| Option | Old client shows | Verdict |
|---|---|---|
| `actor_id` = person (effective), `authenticated_actor_id` = agent | "Dusty: approved, ship it" when the agent wrote it | Rejected |
| **`actor_id` = agent, `on_behalf_of_actor_id` = person, `delegation_id`** | "abby-bridge: …" | **Chosen** |

Why: WIRE-001 tells clients to ignore unknown fields. Every P1 client and
the current viewer would show a delegated write as the person's own words
if `actor_id` became the person. Showing the agent understates authority;
showing the person invents a human act. Design §13 warns that people
read "approved" in messages. The chosen shape also keeps `author_id`
filters and the bridge's "skip my own messages" check correct.

The design's terms still hold: **effective actor** = `on_behalf_of_actor_id`
if set, else `actor_id`; **authenticating actor** = `actor_id`. Rules
about ownership, authorship, assignment, and manager status use the
effective actor (spec §11). This reverses which field is "optional"
compared with the design §3.2 wording; see conflict D1 and Q1.

Wire fields added (all `null` when direct): `Event.on_behalf_of_actor_id`,
`Event.delegation_id`, `Message.on_behalf_of_actor_id`,
`Message.attribution.delegation_id`, `Claim.on_behalf_of_actor_id`,
`Claim.delegation_id`, `Task.created_on_behalf_of_actor_id`,
`Brief.created_on_behalf_of_actor_id`, `Task.block.on_behalf_of_actor_id`,
`AuditEvent.on_behalf_of_actor_id`, `AuditEvent.delegation_id`. New event
filter `on_behalf_of`.

### 5 Human gates: one server-known task flag

ADR-0018 defines a gate as a `blocked` task with root label
`gate=pending`, approved by a manager's **unblock**. BUILD_ABBY BOOT-03
approves by **completing** a review task. Both are ordinary task
transitions, so a delegation with `task.manage` or `task.settle` could
approve. ADR-0018 keeps `gate` as a label convention the server does not
know, and a server cannot protect a convention it does not know.

Decision: add `direct_action_required: true` on a task (set and cleared
only with a direct credential). For such a task, a delegated credential
cannot unblock, claim, renew, release, complete, fail, update the brief,
change root labels, or remove it from another task's prerequisites
(`403 direct_action_required`). A delegated credential also cannot
create a task with the flag or change the flag (DLG-098). Block, cancel,
reopen, and assign stay allowed; none of them can approve.

<!-- TODO(F1): pending owner confirmation of the F1 fix (review
msg_y7eqfu7bba4u3lw6vxuvhyinuq). Cancel then reopen of a gate task by a
delegated credential reaches `ready` with no `task.unblocked`, so "none
of them can approve" is not true for that pair. Replace this paragraph
with the confirmed wording, and update owner decision 7. --> Clients never show a delegated
`task.unblocked` or `task.completed` as an approval (DLG-096), even on
tasks that were not marked.

"Direct" means "not delegated". It does not prove a human was present
(§7).

### 6 Credential-management restriction (closes ADR-0018 C1)

ADR-0018 C1: any credential can issue child credentials and revoke the
person's other credentials. A stolen operator token can lock the person
out of the CLI, and with this ADR it could also create delegations.

Decision: two additive booleans on every credential,
`may_manage_credentials` and `may_delegate`. Default = the issuer's value
(so every existing credential keeps P1 behavior). A child can never turn
one to `true` if the issuer has `false`. `false` refuses issue, list,
revoke-other (revoke-self stays allowed for logout), or delegation
create, mint, revoke, with `403 credential_restricted`. Delegated
credentials always have both `false`. The pair ships first, alone, as
the capability `abby.credentials.restrictions` (Q14). Operator-mode
tokens have both `false` (Q2).

Rejected: new scopes (`credentials`, `delegate`) in the scope list.
Scopes are channel capabilities plus two roles and are intersected with
grants; these attributes are not grant-backed, and putting them in
`scopes` would make every existing `*` token unable to manage itself, or
make `*` silently include them.

### 7 Provenance that a reader can trust (closes ADR-0018 C5)

ADR-0018 C5: a reader cannot prove that a write came from the browser,
because `run_id` is client data and credential labels are private.

Decision: the only provenance fact Abby guarantees is the one the server
derives from the authenticated credential: **direct, or delegated by P to
A through D**. `on_behalf_of_actor_id` and `delegation_id` cannot be set
by any client input. `run_id` stays diagnostic; clients never show an
`operator/…` run ID as proof. There is no "from the browser" or "human
present" fact, because the server cannot see where a direct token lives.
There is no issuer-declared credential purpose either (Q13); it would
still be the person's claim, not proof.

### 8 Claims, ownership, idempotency, revocation

- Task created under delegation: owner = P (accountable), created by A.
  Manager = P is owner and both P and A hold `task_manage`.
- Assignment eligibility uses P. A task assigned to A is not claimable by
  A-for-P; A uses its own direct credential for that.
- Claim records A, P, D, C. Claimant operations need the same pair (A,
  P), through any active delegation from P to A. P's own credential with
  the proof gets `403` (TASK-043 still holds: the person did not take the
  lease and holds no running process).
- Edit/delete under delegation: only messages written by A for P. The
  person may edit or delete messages written for them.
- Idempotency scope adds the effective actor: (A, P, namespace, method,
  path, key). A-direct and A-for-P never share a receipt; a retry across
  a delegation rotation does not run twice.
- Revocation by the delegator, renounce by the delegate, expiry, revocation
  of the person's issuing credential (`issuer_revoked`), revocation of the
  agent's minting credential, or disabling either actor: the next check
  gives `401`, also in long polls. Loss of grants by either actor narrows
  at the next check. Claims are not revoked; they lapse (TASK-042).
- Cursors and snapshots bind to a fingerprint of both actors' grants and
  both ceilings.

### 9 Audit and discovery

Server audit for every delegation and delegated-credential change. Board
audit on each board in the delegation for create and revoke (without the
other boards), so board admins see which agents can act for which
members. Every board audit record made under delegation carries
`on_behalf_of_actor_id` and `delegation_id`. Discovery: profile
`abby.delegation`, limits, and a `delegation` object listing operation
classes and never-delegable operations, for consent screens.

### 10 How the agent bridge uses this (informative)

1. Person P creates a delegation to the bridge actor B (CLI in v1,
   Q2), for example `spec`, `server/*`, classes `read, message.post,
   task.create, task.claim`, 24 h.
2. B (direct token, `may_delegate: true`) mints, per request, a delegated
   credential narrowed to the thread's channel and 30 minutes.
3. B hands only that token to the agent command. The command cannot
   exceed P, cannot exceed B, and cannot approve a gate.
4. Every write shows "abby-bridge for P". P revokes the delegation to
   stop all of it at the next request.

## Design conflicts

- **D1 — Design §3.2 wording.** It says to record "the effective actor"
  and the "authenticating actor when delegated", which suggests the
  always-present field is the effective actor. This ADR keeps `actor_id`
  = authenticating actor and adds `on_behalf_of_actor_id` (§4). Both
  actors are recorded, as the design requires, but the shape differs.
  **Resolved:** the owner chose this shape (Q1).
- **D2 — ADR-0018 gate is a label convention.** This ADR needs a
  server-known flag to enforce the human gate (§5). It amends ADR-0016
  (Task, CreateTaskRequest, PatchTaskRequest) and adds a step to the
  ADR-0018 gate flow (set the flag when opening a gate). **Resolved**
  (Q7): the field is added. Accepted ADR-0018 defines a gate as a
  `blocked` task with `direct_action_required: true`; its approve flow
  checks the flag and does not clear it, and the `gate` label is display
  only (OPR-501, OPR-513, OPR-520, OPR-533).
- **D3 — ADR-0018 C1/C5** are closed here (§6, §7). **Resolved:**
  accepted ADR-0018 marks C1 and C5 closed. Its operator tokens have both
  credential flags `false` (OPR-106). ADR-0018 keeps option A (pasted
  token) for v1; option B stays out of v1.
- **D4 — Amended rules.** AUTH-001 (DLG-081), AUTH-002, AUTH-005,
  AUTH-016 (DLG-073), AUTH-018 (DLG-131, DLG-132), the AUTH-020 rows
  "Edit own message", "Delete own message", and "Edit thread title"
  (DLG-111, DLG-112), the "own credentials only" authorization of
  `DELETE /v1/credentials/{id}` in `spec/openapi.yaml` (DLG-083),
  IDEM-002, EVT-003/005, EVT-009 and SNAP-003 (DLG-075), TASK-041
  (DLG-110), TASK-042/043, TASK-052, SEL-002 (DLG-091), P2 L2 root
  authorship (DLG-112), and BATCH-003 (DLG-098) are amended by ID (spec
  §17). P1/P2 behavior for direct credentials does not change.
- **D5 — PAR-039 is P4** in the design milestone table. The owner pulled
  it forward; §15 and A3 of the design need an update with the
  implementation.
- **D6 — Self-issued chain stops at a different actor** in
  `Authenticate`. Delegation adds a cross-actor link (delegated credential
  → delegation → person's issuing credential). The cascade must be
  explicit (DLG-024), not a side effect of the existing query.
- **D7 — "Direct" is not "human".** The human gate rests on where the
  person keeps direct tokens. Today, coordinator scripts and agents may
  hold tokens of human actors (for example the bootstrap admin token).
  Such use defeats the gate. Operational rule, not protocol.
- **D8 — Approvers need `task_admin`** (ADR-0018 C2). Under intersection
  the delegate would need it too to manage the person's non-owned tasks.
  Not a problem for gates (never delegable); noted for other manager work.

## Consequences

- One new server resource (delegation), two credential attributes, one
  task field, new attribution fields, one filter, three error codes.
- Delivery order: the credential flags ship first and alone
  (`abby.credentials.restrictions`, board task
  `task_ej3tgn5syl5fqyesin6ge4f2pm`). The task field
  `direct_action_required` is board task
  `task_hm7cvzd6z553n4josm2vfypj54`.
- Board archives (ADR-0017) export `direct_action_required` and the
  attribution fields, and never export delegations or the credential
  flags (ARC-031..033, ARC-040).
- The authz package gains a combinator over two `Policy` values and an
  operation-class check. Every route needs a class in a table that a
  reference test checks for completeness.
- Viewers must show "A for P". Old viewers show "A" — safe by default.
- The bridge example can be built with least privilege per request.

## Owner decisions (2026-11-20)

The owner accepted every recommendation of the draft.

1. Attribution shape (D1): `actor_id` stays the agent (authenticating
   actor). New `on_behalf_of_actor_id` names the person, with
   `delegation_id` (DLG-100).
2. Operator-mode browser token and `may_delegate`: no in v1. Create
   delegations with the CLI. Allow it from the browser only with the
   server session endpoint (ADR-0018 option C) or OIDC, and a consent
   screen (DLG-039, OPR-106).
3. Delegation lifetime: tied to the person's issuing credential
   (cascade, DLG-024). Revoking that token stops every agent.
4. Effective authority: the person's current grants AND the agent's
   current grants AND both ceilings. A deny on either actor wins
   (DLG-070, DLG-071).
5. Claims when a delegation ends: they lapse (≤ 300 s), as in TASK-042.
   The runner stops on the first `401` (DLG-116).
6. OR filter "author or on behalf of P": no. Two reads in v1 (DLG-103).
7. Gate marker: add the task field `direct_action_required`. A delegated
   credential cannot unblock, claim, renew, release, complete, fail,
   change the brief or root labels, or remove the task as a
   prerequisite (DLG-090..093). ADR-0018's approve flow uses the field.
8. Subscriptions and schedules (P3) under delegation: not in v1
   (DLG-095).
9. Delegators: `human` actors only in v1 (DLG-011). No chains.
10. Delegate ownership: not required. The delegate need not be owned by
    the delegator (DLG-012).
11. Expiry limits: default 24 h, maximum 30 days, delegated credential
    maximum 24 h. No `max_uses` (DLG-002).
12. Board admins and delegations: no list or revoke in v1. They see
    create and revoke in board audit and can cut an agent off with a deny
    grant (DLG-131, DLG-133).
13. Issuer-declared credential purpose: no. The trusted fact is direct or
    delegated (DLG-106..108).
14. Credential flags first: yes. `may_manage_credentials` and
    `may_delegate` ship first and separately as
    `abby.credentials.restrictions`. Default: the issuer's value. A child
    can never turn one back to `true`. A refusal is
    `403 credential_restricted` (DLG-004, DLG-036..038).
