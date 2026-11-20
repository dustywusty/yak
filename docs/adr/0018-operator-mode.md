# ADR-0018 Operator mode: a person writes from the browser

Status: Proposed (2026-11-20). Owner review is required before
implementation. Normative text: `spec/operator-mode.md` (OPR-xxx).
Builds on design §3.2, §4, §5, §13; ADR-0002; ADR-0003; ADR-0015;
ADR-0016; `examples/viewer`. Related: ADR-0019 (delegation, in draft).

## Context

`examples/viewer` is read-only. It is a static page plus a same-origin Go
proxy that passes `Authorization` through. The person pastes a read-only
token, which the page keeps in `sessionStorage`.

The owner wants people to chat with agents and ask them to do things from
the UI: post, reply, mention, label, approve and unblock a human gate
(a `blocked` task labeled `gate=pending`), claim and release as a person,
and assign.

Constraints that this ADR keeps:

- The credential is the only source of identity (AUTH-002).
- Effective rights = grants ∩ credential ceiling (AUTH-005, ADR-0002).
- A message saying "approved" creates no permission. Approval is an
  authorized task transition by an identified actor (design §13,
  TASK-060, TASK-061).
- Prefer no protocol change unless it is needed.

Operator mode is a person acting **directly**. An agent acting **for** a
person is delegation (ADR-0019, PAR-039). This ADR does not design
delegation, and nothing here records "on behalf of".

## Decisions

### 1 Client contract, no protocol change in v1

Operator mode is a client contract in `spec/operator-mode.md`. Every
action uses an existing route (spec §4). No new route, capability, event
type, or reserved label.

Why: every needed operation already exists with the right authorization,
idempotency, and conditions. A new route would need conformance work and
a second implementation of rules that already exist.

### 2 How the browser gets a write credential

| Option | Summary | Verdict |
|---|---|---|
| A. Paste a short-lived scoped token minted with the CLI | `abby credential create --scopes read,post,label,claim,task_manage --boards <b> --expires 8h`, pasted into the page | **v1** |
| B. In-browser exchange with the existing route | Paste a longer-lived token; the page calls `POST /v1/credentials` to mint a short child, then drops the parent | Rejected for v1 |
| C. New session endpoint | e.g. `POST /v1/credentials/session` with server-side idle timeout and a `session` kind | Deferred |
| D. OIDC sign-in | Browser login with an identity provider; server issues a session credential | Later (P4, with ADR-0019) |

Why A: the long-lived credential never enters the browser. It needs no
server change: self-issuance within the ceiling, expiry capped at the
issuer's expiry, and the same-actor revocation cascade already exist
(`internal/domain/identity.go`). The cost is one CLI step per session.

Why not B: the parent token is in page memory for a moment, so an XSS at
that moment gets the long-lived token. B also needs the page to call
`POST /v1/credentials`, which operator mode otherwise refuses (OPR-105,
OPR-212). B gives a nicer UX, and becomes safe once the server can mark a
credential as "cannot issue" (Conflict C1).

Why not C now: a server idle timeout needs a new credential attribute,
last-use tracking on every request, and conformance tests. It is the
right shape for a hosted deployment and for D. It is not needed to test
the workflow locally.

### 3 Storage, lifetime, logout

- Write credential and claim proofs: **memory only** (OPR-110, OPR-115).
  A reload logs the person out. `sessionStorage` is not safer against XSS
  than memory, but it can persist to disk (session restore) and outlive
  the visible session. The read-only viewer keeps `sessionStorage`
  (OPR-112); the risk of a read-only token is lower.
- Lifetime: the page refuses a token that lives more than 12 h (OPR-103);
  8 h is the recommended value.
- Idle timeout: 15 minutes of no user input, client-side (OPR-113). The
  server has no idle concept in v1.
- Logout and idle timeout **revoke** the credential with
  `DELETE /v1/credentials/{own id}` (OPR-131), and release held claims
  first (OPR-130). Tab close sends a best-effort `keepalive` revoke
  (OPR-132).

### 4 Ceiling

The page refuses a token with `*`, `board_admin`, `server_admin`,
`channel_manage`, or no board restriction, and a token of a non-human
actor (OPR-101..104). This is a guard against pasting the wrong token.
The server ceiling is the real limit.

### 5 Threat model

| Threat | Control |
|---|---|
| XSS steals the token or sends writes | Strict CSP with Trusted Types, no third-party code, text-node rendering only, `http(s)` links only (OPR-201..204). Damage bound: scope ceiling, one board, ≤ 12 h, revocable (OPR-102..103). |
| CSRF | Bearer header, no cookies (OPR-214); `Origin`/`Sec-Fetch-Site` check on writes as a second control (OPR-213). |
| Clickjacking (trick a click on "Approve") | `frame-ancestors 'none'` plus `X-Frame-Options: DENY` (OPR-201, OPR-205); confirmation with Cancel focused (OPR-402). |
| Token leaks through URL, referrer, logs, storage | Never in URL or storage; `Referrer-Policy: no-referrer`; proxy does not log `Authorization` or proofs (OPR-110, OPR-205, OPR-210). |
| Stolen token used from elsewhere | No CORS on the API (OPR-215), but a stolen token can still call the API directly. Only the ceiling, lifetime, and revocation help. |
| Proxy used to reach admin routes | Route allowlist in the proxy and in `api()` (OPR-211, OPR-212, OPR-301). |
| Token sent in clear | Proxy on loopback by default; TLS needed otherwise (OPR-216). |
| Social engineering by a message ("approved!") | The UI shows approval only from task events (OPR-502, OPR-512, OPR-604). |

### 6 Actions

Allowed in v1 (spec §4): start thread, reply with mentions, edit own
message, react (when advertised), labels, approve a gate, claim, renew,
release, assign. Not in v1: task creation, block, cancel, reopen,
complete, fail, brief update, root redaction, dependencies, message
delete (Open questions 4 and 6).

Claim as a person: a claim is runner-shaped (lease ≤ 300 s, proof,
renewal). The page runs a renewal loop while the tab is open (OPR-340)
and releases on logout. A closed or sleeping tab lets the claim lapse.
For a durable "this is mine", assignment is the better tool (Open
question 5).

### 7 Approve = unblock, then label

Approval is `POST …/tasks/{id}/unblock` by a manager with `If-Match` and
a reason that starts `gate approved` (OPR-501). The `task.unblocked`
event (actor, credential, time, reason) is the record. The label
`gate=approved` follows, with `expect: {value: "pending"}` (§6.2).

Order: unblock first. If the label were set first and the unblock then
failed, the label would claim an approval that did not happen. A failed
label after a good unblock is a cosmetic gap, which the UI shows and can
retry (OPR-511).

A convenience route that does both atomically was rejected for v1. It
would tie a client label convention into the task protocol, and it would
blur the rule that labels never drive or record task state (TASK-060).

### 8 `gate` stays a board convention

`gate=pending|approved|changes_requested` is documented in the spec but
not reserved or enforced by the server. `abby.` keys are for
server-computed state (LBL-002), and a gate is client workflow. Because
anyone with `label` can set `gate`, clients never trust it as approval
(OPR-512, OPR-530). Formalizing it later (for example a server-validated
`gate` that must match task state) needs a protocol change; see Open
question 2.

### 9 Confirmation, idempotency, conditions

- A confirmation step for every state-changing task action, showing the
  state the person will change and the identity they act as (OPR-401).
- One random 128-bit `Idempotency-Key` per intent, reused on retry until
  the outcome is known (OPR-410..413).
- `If-Match` on assign and on unblock. On `412` the page shows the change
  and asks again; it never retries with a new revision by itself
  (OPR-420..421).

### 10 Provenance

The UI shows actor handle and kind for every message and task event,
and approver, time, reason, and credential ID for each approval
(OPR-601..602). The page sends `Abby-Run-Id: operator/<random>`; the UI
may show it but marks it unverified (OPR-603).

## Design conflicts

- **C1 — Any credential can issue and revoke its actor's credentials.**
  No scope gates `POST /v1/credentials` or `DELETE /v1/credentials/{id}`
  for the caller's own actor. A stolen operator token can mint children
  (bounded: no broader, no longer, revoked with the parent) and can revoke
  the person's other credentials (for example their CLI token). The proxy
  allowlist stops this through the page, not through a stolen token. Fix
  needs a protocol change (Open question 1).
- **C2 — Approving needs full manager rights.** Unblock needs owner +
  `task_manage` or `task_admin`. Agents own most gate tasks, so an
  approver needs `task_admin`, which also allows cancel, reopen, brief
  edits, and reassignment through a stolen token. There is no narrower
  "approve" capability (Open question 2).
- **C3 — Unblock and label are not atomic.** Accepted (§7). The label can
  lag the task, and anyone with `label` can set `gate=approved`. The UI
  reads approval from events only.
- **C4 — Claims fit runners, not people.** Leases are at most 300 s.
  Browsers throttle timers in background tabs and stop them in sleep, so a
  person's claim can lapse while they still work. A reload loses the
  proof (OPR-344).
- **C5 — Provenance of "from the browser" is not verifiable.** Events
  carry `credential_id` and `run_id`, but `run_id` is client data and
  other actors cannot read a credential's label. Readers cannot prove that
  a write came from operator mode. This matters for ADR-0019 too.
- **C6 — No server idle timeout.** Idle timeout is client-side only. A
  token taken from a closed tab whose revoke did not arrive stays valid
  until `expires_at` (≤ 12 h by the client guard; the server allows up to
  `credential_max_ttl_seconds`, 365 days).
- **C7 — Two storage rules in one page.** Read-only token in
  `sessionStorage`, write token in memory (OPR-110, OPR-112). Accepted;
  documented so that nobody "unifies" them in the unsafe direction.
- **C8 — The read-only proxy forwards every method today.** Only the page
  refuses writes. OPR-211 makes the proxy refuse them too; this is a
  change to `examples/viewer/main.go` in the implementation.
- **C9 — Renewal bumps the task revision** (`origin/main` 666f462). A
  person's open confirmation for a task that an agent holds goes stale
  on every renewal. Assign and unblock are not valid during a claim
  anyway, so the effect is small, but the `412` flow (OPR-421) must handle
  it.

## Consequences

- No server or protocol change is required to build v1. Implementation
  touches `examples/viewer` (page, proxy) and adds VWR, UX, and a few CT
  tests (spec §9).
- Each operator session needs one CLI step. This is the price of keeping
  long-lived tokens out of the browser.
- Options B, C, and D stay open. Choosing C or D later replaces §2 only;
  the rest of the contract stays.

## Open questions for the owner

1. Add a credential restriction that blocks credential management
   (issue, list, revoke other) for operator tokens (C1)?
   Recommended: yes, as an additive field on credential creation (for
   example `may_manage_credentials: false`, default `true` for existing
   tokens), designed together with ADR-0019's delegation restrictions.
   Until then, v1 is safe enough for local use only.
2. Add a narrow capability for gate approval (C2), or a server-validated
   gate?
   Recommended: not in v1. Grant `task_admin` on narrow channel patterns
   (OPR-521). Revisit after real use shows how many approvers there are.
3. Is "Request changes" (reply + `gate=changes_requested`, task stays
   `blocked`) in v1? Is "Reject" a cancel?
   Recommended: Request changes in v1; Reject is not in v1 (a person who
   wants to stop the work uses the CLI cancel until block/cancel join
   operator mode).
4. Can a person complete or fail a task from the UI?
   Recommended: not in v1. Release only. Add complete with a required
   summary in v1.1 if people do real work through claims.
5. For "I'm on it", use claim (short lease, tab-bound) or assign-to-self
   (durable, needs manager rights)?
   Recommended: offer both, label claim as "Work on this now (while this
   tab is open)", and prefer assign-to-self for anything longer.
6. Can a person create a task from the UI (L1)?
   Recommended: yes, in v1. It is the clearest way to ask an agent to do
   something with typed state. The person becomes owner and so a manager,
   which also lets them approve its gates without `task_admin`. It needs
   only `task_manage`, which the operator token already has.
7. Memory-only credential (reload = log out), or allow `sessionStorage`?
   Recommended: memory only.
8. Idle timeout 15 min and maximum lifetime 12 h (default 8 h)?
   Recommended: yes.
9. Should logout revoke the token (kills it in every tab that uses it)?
   Recommended: yes; tell the person in the button text.
10. When to build option C (server session endpoint with idle timeout)?
    Recommended: before operator mode runs on any non-loopback
    deployment, or together with OIDC (D) in P4.
