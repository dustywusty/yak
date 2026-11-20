# ADR-0018 Operator mode: a person writes from the browser

Status: Accepted (owner, 2026-11-20). Normative text:
`spec/operator-mode.md` (OPR-xxx). Builds on design §3.2, §4, §5, §13;
ADR-0002; ADR-0003; ADR-0015; ADR-0016; `examples/viewer`. Related:
ADR-0019 (delegation, accepted), which closes conflicts C1 and C5 and
gives the gate a server-known marker.

## Context

`examples/viewer` is read-only. It is a static page plus a same-origin Go
proxy that passes `Authorization` through. The person pastes a read-only
token, which the page keeps in `sessionStorage`.

The owner wants people to chat with agents and ask them to do things from
the UI: post, reply, mention, label, approve and unblock a human gate
(a `blocked` task with `direct_action_required: true`, ADR-0019), claim
and release as a person, and assign.

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
action uses an existing route (spec §4). Operator mode adds no route,
capability, event type, or reserved label. It uses two additions that
ADR-0019 makes: the credential flags `may_manage_credentials` and
`may_delegate` (capability `yak.credentials.restrictions`, which ships
first) and the task field `direct_action_required`.

Why: every needed operation already exists with the right authorization,
idempotency, and conditions. A new route would need conformance work and
a second implementation of rules that already exist.

### 2 How the browser gets a write credential

| Option | Summary | Verdict |
|---|---|---|
| A. Paste a short-lived scoped token minted with the CLI | `yak credential create --scopes read,post,label,claim,task_manage --boards <b> --expires 8h`, pasted into the page | **v1** |
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
OPR-212). B gives a nicer UX. The credential flags of ADR-0019 make it
possible to do safely, but v1 keeps option A.

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
`channel_manage`, or no board restriction, a token of a non-human
actor, a delegated token, and a token that may manage credentials or
delegate (OPR-101..107). This is a guard against pasting the wrong token.
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
message, react (when advertised), labels, approve a gate, request changes
on a gate, claim, renew, release, assign, create a task, and set or clear
the gate flag on a task the person manages. Not in v1: block, cancel,
reopen, complete, fail, brief update, root redaction, dependencies,
message delete (owner decisions 3 and 4).

Claim as a person: a claim is runner-shaped (lease ≤ 300 s, proof,
renewal). The page runs a renewal loop while the tab is open (OPR-340)
and releases on logout. A closed or sleeping tab lets the claim lapse.
For a durable "this is mine", assignment is the better tool. The page
offers both (owner decision 5).

### 7 Approve = unblock, then label

Approval is `POST …/tasks/{id}/unblock` by a human manager with a direct
credential, with `If-Match` and a reason that starts `gate approved`
(OPR-501), on a task that is `blocked` and has
`direct_action_required: true`. The `task.unblocked` event by a `human`
actor (actor, credential, time, reason, no `on_behalf_of_actor_id`) is
the record. An unblock by an agent, even with its own direct credential
and the reason `gate approved`, is shown as "not a person's approval"
(OPR-514). A delegated credential cannot unblock such a task (ADR-0019 §5), so an
agent cannot approve for the person. The owner confirmed F1 on
2026-11-20: delegated cancel and reopen of flagged tasks return
`403 direct_action_required`; every direct reopen lands in `blocked`
(DLG-091, DLG-099). A new block needs a new human unblock; agents wait
for that event, not state `ready` or completion alone (OPR-531). The label
`gate=approved` follows for display, with `expect: {value: "pending"}` (§6.2). Approve does not
clear `direct_action_required`; clearing it is a separate action (A13).

Order: unblock first. If the label were set first and the unblock then
failed, the label would claim an approval that did not happen. A failed
label after a good unblock is a cosmetic gap, which the UI shows and can
retry (OPR-511).

A convenience route that does both atomically was rejected for v1. It
would tie a client label convention into the task protocol, and it would
blur the rule that labels never drive or record task state (TASK-060).

### 8 The gate is a task field; the `gate` label is display only

The server-known gate marker is the task field `direct_action_required`
(ADR-0019 §5). A gate is a `blocked` task with that field `true`. The
label `gate=pending|approved|changes_requested` is a display convention.
The server does not reserve, validate, or enforce it. `yak.` keys are for
server-computed state (LBL-002). Because anyone with `label` can set
`gate`, clients never read approval or gate status from it (OPR-512,
OPR-530, OPR-532). There is no narrow "approve" capability in v1 (owner
decision 2).

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
(OPR-601..602). The page sends `Yak-Run-Id: operator/<random>`; the UI
may show it but marks it unverified (OPR-603). The one provenance fact a
reader can trust is the one the server derives from the credential:
direct, or delegated by P to A (ADR-0019 §7). The UI shows delegated
writes as "A for P" and never as an approval (OPR-606).

## Design conflicts

- **C1 — Any credential can issue and revoke its actor's credentials.**
  **Closed** by the credential flags of ADR-0019 §6 (owner decision 1),
  which ship first. An operator token has `may_manage_credentials: false`
  and `may_delegate: false` (OPR-106).
  No scope gates `POST /v1/credentials` or `DELETE /v1/credentials/{id}`
  for the caller's own actor. A stolen operator token can mint children
  (bounded: no broader, no longer, revoked with the parent) and can revoke
  the person's other credentials (for example their CLI token). The proxy
  allowlist stops this through the viewer, including direct proxy requests.
  D017 authorizes a request-scoped authenticated `GET /v1/me` to the same
  configured upstream before credential DELETE. The proxy clones the opaque
  authentication envelope without parsing or retaining bearer values, strips
  body/query/cookie/proof/mutation headers from discovery, disables redirects,
  and bounds discovery time and body size. It parses only `credential.id` and
  compares the exact decoded target: mismatch is `403`, discovery `401` stays
  `401`, and discovery failures are `502`. Only a match forwards the original
  DELETE with its allowed headers unchanged. No identity cache or token
  association survives the request, and browser-supplied IDs are not proof.
  These proxy checks do not constrain a stolen token used outside the viewer.
- **C2 — Approving needs full manager rights.** **Accepted for v1**
  (owner decision 2). The gate check now uses `direct_action_required`,
  so a delegated agent with `task_admin` still cannot approve. Unblock needs owner +
  `task_manage` or `task_admin`. Agents own most gate tasks, so an
  approver needs `task_admin`, which also allows cancel, reopen, brief
  edits, and reassignment through a stolen token. There is no narrower
  "approve" capability.
- **C3 — Unblock and label are not atomic.** **Resolved.** The gate is the
  task state plus `direct_action_required`; the label is display only
  (§8). The label can lag the task, and anyone with `label` can set
  `gate=approved`. The UI reads approval from task events only.
- **C4 — Claims fit runners, not people.** Leases are at most 300 s.
  Browsers throttle timers in background tabs and stop them in sleep, so a
  person's claim can lapse while they still work. A reload loses the
  proof (OPR-344).
- **C5 — Provenance of "from the browser" is not verifiable.** **Closed**
  by ADR-0019 §7: the provenance fact is direct or delegated, which the
  server derives from the credential; `run_id` is never proof. Events
  carry `credential_id` and `run_id`, but `run_id` is client data and
  other actors cannot read a credential's label. Readers cannot prove that
  a write came from operator mode. This matters for ADR-0019 too.
- **C6 — No server idle timeout.** Idle timeout is client-side only. A
  token taken from a closed tab whose revoke did not arrive stays valid
  until `expires_at` (≤ 12 h by the client guard; the server allows up to
  `credential_max_ttl_seconds`, 365 days). Accepted for loopback only:
  option C comes before any non-loopback deployment (owner decision 10).
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

- Operator mode itself needs no server or protocol change. Implementation
  touches `examples/viewer` (page, proxy) and adds VWR, UX, and a few CT
  tests (spec §9).
- Each operator session needs one CLI step. This is the price of keeping
  long-lived tokens out of the browser.
- The credential flags (`yak.credentials.restrictions`) ship first and
  separately (ADR-0019 Q14). The gate flow needs the task field
  `direct_action_required`; without it the page offers no Approve.
- Options B, C, and D stay open. Choosing C or D later replaces §2 only;
  the rest of the contract stays. C comes before any non-loopback
  deployment, or with OIDC (D) in P4.

## Owner decisions (2026-11-20)

The owner accepted every recommendation of the draft.

1. Credential restriction (C1): yes. Two additive credential flags,
   `may_manage_credentials` and `may_delegate`, designed in ADR-0019 §6.
   They ship first and separately as `yak.credentials.restrictions`.
   Default: the issuer's value. A child can never turn one back to
   `true`. A refusal is `403 credential_restricted`. Operator tokens have
   both `false` (OPR-106).
2. Narrow approve capability (C2): not in v1. Grant `task_admin` on narrow
   channel patterns (OPR-521). Revisit after real use. The gate itself is
   the task field `direct_action_required`, and the `gate` label is
   display only (§7, §8).
3. Request changes: in v1 (reply plus `gate=changes_requested`; the task
   stays `blocked`, A12). Reject is not in v1; use the CLI cancel.
4. Complete or fail from the UI: not in v1. Release only (OPR-343).
5. "I'm on it": offer both. Claim is labeled "Work on this now (while
   this tab is open)"; assign-to-self is for longer work (OPR-345).
6. Task creation from the UI: yes, in v1 (A11). The person becomes owner
   and manager. It needs only `task_manage`.
7. Credential storage: memory only. A reload logs the person out
   (OPR-110).
8. Idle timeout 15 minutes; maximum lifetime 12 hours; default 8 hours
   (OPR-103, OPR-113).
9. Logout revokes the token, and the button text says so (OPR-131).
10. Option C (server session endpoint with idle timeout): build it before
    operator mode runs on any non-loopback deployment, or with OIDC (D)
    in P4. Until then the operator proxy listens on loopback only
    (OPR-216).
