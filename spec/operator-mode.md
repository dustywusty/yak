# Abby operator mode, version 1 — browser write contract

Status: accepted (owner, 2026-11-20). The decisions, the owner's answers,
and the alternatives are in [ADR-0018](../docs/adr/0018-operator-mode.md).

Operator mode lets a **person** use a browser client (the reference is
`examples/viewer`) to write to Abby: chat, mention agents, change labels,
approve a human gate, claim and release work, and assign work. The person
acts directly, with their own human actor and their own credential.

Operator mode is a **client contract**. It adds no route, no capability,
no event type, and no change to `spec/protocol.md` or `spec/p2-tasks.md`.
Every write uses an existing route with the existing authorization,
idempotency, and condition rules. Where a client rule depends on a server
rule, this document names the server requirement. Two server rules come
from the delegation contract (`spec/delegation.md`, ADR-0019): the
credential flags `may_manage_credentials` and `may_delegate` (DLG-036..039,
capability `abby.credentials.restrictions`) and the task field
`direct_action_required` (DLG-090..093).

Out of scope:

- An agent that acts **for** a person (delegation). That is the delegation
  contract (ADR-0019, accepted; design §3.2, PAR-039). Operator mode never
  records "on behalf of". The person is the actor.
- OIDC and browser sign-in. v1 uses a pasted, short-lived token (§2).
- Administration: grants, channels, boards, actors, schemas, recovery.

The key words MUST, MUST NOT, SHOULD, and MAY are used as in RFC 2119.
Every MUST maps to a planned test in the traceability table (§10).

## 1 Terms

| Term | Meaning |
|---|---|
| Operator client | A browser page that implements this contract. The reference is `examples/viewer` started with `-operator`. |
| Operator proxy | The same-origin server that serves the page and forwards `/v1/*` to Abby (`examples/viewer/main.go`). |
| Operator credential | The write-capable bearer token that the person pastes into the page. |
| Operator session | The period from a successful paste to logout, idle timeout, expiry, revocation, or tab close. |
| Gate | A task that waits for a person's decision: state `blocked` and `direct_action_required: true` (DLG-090). The root label `gate=pending` is for display only (§6.4). |
| Approve | The person's authorized `unblock` of a gate task (L11) with a direct credential, followed by the display label `gate=approved`. |
| State-changing task action | Claim, release, unblock (approve), assign, create a task, request changes, set or clear the gate flag. In later versions also block, cancel, reopen, complete, fail. |

## 2 Operator credential

v1 decision: the person mints a short-lived, board-limited, scope-limited
credential for their own human actor with the CLI, and pastes it into the
page. The browser never sees a long-lived credential. See ADR-0018 §2 for
the options that were not chosen (in-browser exchange, a new session
endpoint, OIDC).

Recommended mint command (existing CLI and route, `POST /v1/credentials`):

```sh
abby credential create --label operator-session \
  --scopes read,post,label,claim,task_manage \
  --boards <board_id> --expires 8h
```

Add `task_admin` only for a person who must approve gates on tasks that
they do not own (§6.3).

When the server lists `abby.credentials.restrictions`, the person mints
the credential with `may_manage_credentials: false` and
`may_delegate: false` (DLG-036, DLG-039). The CLI flags for these fields
ship with that feature.

- **OPR-101** The operator credential MUST belong to an actor of kind
  `human`. After paste, the client MUST call `GET /v1/me`. If
  `actor.kind` is not `human`, the client MUST stay read-only and MUST
  show why. (A delegated credential belongs to an agent or service,
  DLG-012, so this check also refuses it. OPR-107 checks it directly.)
- **OPR-102** The client MUST refuse to enter operator mode if the
  credential (from `GET /v1/me`) has any of these properties:
  scope `*`, scope `board_admin`, scope `server_admin`, scope
  `channel_manage`, or `board_ids` null (all boards). The client MUST show
  which property caused the refusal. (This is a client guard against a
  pasted administrator token. The server ceiling, AUTH-005, stays the real
  limit.)
- **OPR-103** The client MUST refuse a credential whose `expires_at` is
  more than 12 hours after the current time, as reported by the server
  (`expires_at` − `Date` response header). The default and recommended
  lifetime is 8 hours.
- **OPR-104** The client MUST accept only the scopes `read`, `post`,
  `react`, `label`, `claim`, `task_manage`, `task_admin`. A missing scope
  disables the actions that need it (§4); it is not an error.
- **OPR-105** The client MUST NOT call `POST /v1/credentials`,
  `GET /v1/credentials`, or `DELETE /v1/credentials/{id}` for any
  credential other than its own (OPR-131). It MUST NOT issue, list, or
  revoke other credentials. It MUST NOT call any `/v1/delegations` route.
- **OPR-106** If the server lists the capability
  `abby.credentials.restrictions`, the client MUST refuse to enter
  operator mode when the credential has `may_manage_credentials: true` or
  `may_delegate: true`, and MUST show which flag caused the refusal
  (ADR-0018 decision 1; ADR-0019 Q2: no delegation from the browser in
  v1). If the server does not list it, the client MUST show a warning
  that a stolen token could issue and revoke the person's credentials
  (ADR-0018 conflict C1).
- **OPR-107** The operator credential MUST be direct. If `GET /v1/me`
  returns a non-null `delegation` or `credential.delegation_id`
  (DLG-041), the client MUST stay read-only and MUST show why.

### 2.1 Storage and lifetime

- **OPR-110** The client MUST keep the operator credential in JavaScript
  memory only. It MUST NOT write it to `localStorage`, `sessionStorage`,
  IndexedDB, cookies, the URL (path, query, or fragment), the page title,
  `history` state, or any log. A reload or a new tab starts without it.
- **OPR-111** The paste field MUST be `type="password"` with
  `autocomplete="off"` and `spellcheck="false"`. The client MUST clear the
  field after it reads the value.
- **OPR-112** The read-only viewer token keeps its current rule
  (`sessionStorage`, this tab only). If the person enters operator mode,
  the client MUST remove any read-only token from `sessionStorage` and use
  only the operator credential for the session.
- **OPR-113** Idle timeout: after 15 minutes without user input (key,
  pointer, or touch on the page), the client MUST end the operator
  session (OPR-130). Background event polling is not user input. The
  client SHOULD show a warning 60 seconds before the timeout.
- **OPR-114** The client MUST end the operator session at the
  credential's `expires_at`, and on the first `401 unauthenticated` from
  any request.
- **OPR-115** The proof of a claim (`Abby-Claim-Proof`, CLAIM-010) MUST
  follow OPR-110: memory only, never stored, never shown, never logged.

### 2.2 Logout and revocation

- **OPR-130** Ending the operator session MUST, in order: stop every claim
  renewal loop (§4.4), release each active claim that the session holds
  (best effort, OPR-342), revoke the credential (OPR-131), and remove the
  credential and every proof from memory. The page then returns to the
  read-only login screen.
- **OPR-131** Logout and idle timeout MUST revoke the operator credential
  with `DELETE /v1/credentials/{id}`, where `id` is `credential.id` from
  `GET /v1/me`. The UI MUST say that this revokes the token everywhere it
  is used. A revoke failure other than `401` MUST be shown, and the
  credential MUST be removed from memory anyway.
- **OPR-132** On `pagehide`, the client SHOULD send the revoke with
  `fetch(…, {keepalive: true})`. This is best effort. A closed tab whose
  revoke did not arrive leaves the credential valid until `expires_at`.
  That is why OPR-103 limits the lifetime.
- **OPR-133** Revocation by another path (`abby credential revoke`, a
  server administrator, or revocation of the credential that issued it —
  the same-actor issuance chain in `Authenticate`) ends the session at the
  next request (AUTH-004, EVT-011). The client MUST NOT retry a `401`
  with the same credential.

## 3 Browser and proxy security

The page runs untrusted content (messages written by agents and people).
The main threat is script injection (XSS) that steals the credential or
sends writes as the person. Bearer headers, not cookies, carry the
credential, so cross-site request forgery does not apply.

### 3.1 Page

- **OPR-201** Every page response MUST carry this Content-Security-Policy,
  or a stricter one:
  `default-src 'none'; script-src 'self'; style-src 'self';
  connect-src 'self'; img-src 'self' data:; base-uri 'none';
  form-action 'none'; frame-ancestors 'none';
  require-trusted-types-for 'script'`.
  No `unsafe-inline`, no `unsafe-eval`, no remote origin.
- **OPR-202** The page MUST NOT load any third-party script, style, font,
  or image. All code ships embedded in the proxy binary.
- **OPR-203** The page MUST insert all server-provided content (message
  text, titles, labels, handles, display names, reasons, evidence) as
  text nodes (`textContent`) or attribute values set through the DOM. It
  MUST NOT use `innerHTML`, `outerHTML`, `insertAdjacentHTML`,
  `document.write`, or `eval` with any server-provided value.
- **OPR-204** If the client makes links from message text, it MUST allow
  only `http:` and `https:` URLs, set `rel="noopener noreferrer"` and
  `target="_blank"`, and show the full URL. Message Markdown is shown as
  text in v1.
- **OPR-205** Responses MUST carry `X-Frame-Options: DENY`,
  `X-Content-Type-Options: nosniff`, and `Referrer-Policy: no-referrer`
  (`frame-ancestors 'none'` covers current browsers; the header covers
  old ones).

### 3.2 Proxy

- **OPR-210** The proxy MUST pass `Authorization`, `Idempotency-Key`,
  `If-Match`, `Abby-Claim-Proof`, and `Abby-Run-Id` through unchanged. It
  MUST NOT read, add, store, or log `Authorization` or
  `Abby-Claim-Proof`. Error logs MUST contain only method, path, and
  status.
- **OPR-211** Without the `-operator` flag the proxy MUST forward only
  `GET`, `POST …/snapshots`, and `DELETE …/snapshots/{id}`, and MUST
  answer every other request with `405 method_not_allowed`. (Today the
  page refuses other methods; this makes the proxy refuse them too.)
- **OPR-212** With `-operator`, the proxy MUST forward only the
  method-and-route pairs in §4 (the allowlist), plus the reads of the
  read-only viewer. It MUST answer every other request with `403` and a
  problem document with code `forbidden` and `reason:
  "operator_route_not_allowed"`. In particular it MUST refuse every
  `/v1/actors`, `/v1/audit-events`, `POST /v1/boards`,
  `POST /v1/credentials`, `GET /v1/credentials`, every `/v1/delegations`
  route, every `…/grants`,
  `POST …/channels`, `PATCH /v1/boards/{id}`, `…/schemas` writes,
  `…/root-redaction`, `…/briefs` writes, and `DELETE …/messages/{id}` of
  another actor's message (the proxy cannot tell authorship, so it refuses
  every message `DELETE` in v1). The proxy cannot see the body of
  `PATCH …/tasks/{id}` (A13); the client limits it (OPR-305), and the
  server ceiling limits the rest.
- **OPR-213** For a non-`GET` request, the proxy MUST reject a request
  whose `Origin` header is present and is not the proxy's own origin, and
  a request whose `Sec-Fetch-Site` header is present and is not
  `same-origin`. (Defense in depth. Bearer headers already stop CSRF.)
- **OPR-214** The proxy MUST NOT set or forward cookies for `/v1/*`.
- **OPR-215** A deployment MUST NOT add permissive CORS headers to the
  Abby API for the operator client. The page reaches the API only through
  its own origin. (The reference server sends no CORS headers, so a
  browser on another origin cannot send `Authorization` to it.)
- **OPR-216** With `-operator`, the proxy MUST listen on loopback
  (`127.0.0.1`) only, and MUST refuse to start with a non-loopback listen
  address. A non-loopback operator deployment needs the server session
  endpoint (ADR-0018 option C) first (owner decision 10). The operator
  credential MUST NOT cross a network without TLS.

The proxy allowlist (OPR-212) is a second control, not the main one. A
script that steals the credential can use it directly against the API.
The credential ceiling (OPR-102..104, AUTH-005) and its short lifetime
(OPR-103) are what limit the damage.

## 4 Allowed actions and the API each uses

All paths are relative to `/v1/boards/{board_id}` unless they start with
`/v1/`. "Rights" is the server rule; the client does not enforce it, but
it SHOULD hide or disable an action that `GET …/permissions?channel=`
(AUTH-017) or the task view shows the person cannot perform.

| # | Action | Method and route | Rights (server) | Headers | Confirm (§5.1) |
|---|---|---|---|---|---|
| A1 | Start a thread | `POST …/threads` | `read`+`post` (AUTH-020) | `Idempotency-Key` | No |
| A2 | Reply, with mentions | `POST …/threads/{id}/messages` | `read`+`post`; mentions per MEN-001 | `Idempotency-Key` | No |
| A3 | Edit own message | `PATCH …/messages/{id}` | author, `read`+`post` | `If-Match` | No |
| A4 | React | `PUT`/`DELETE …/messages/{id}/reactions/{key}` (when the server advertises reactions) | `read`+`react` | — | No |
| A5 | Change labels | `PATCH …/messages/{id}/labels` | `read`+`label`; no `abby.` keys (LBL-002) | optional `If-Match`; `expect` per op | No, except `gate` key (§6) |
| A6 | Approve a gate | `POST …/tasks/{id}/unblock`, then `PATCH …/messages/{root}/labels` | manager (TASK-041), direct credential (DLG-091) | `Idempotency-Key`, `If-Match` | Yes |
| A7 | Claim as a person | `POST …/tasks/{id}/claims` | `read`+`claim`, eligible (TASK-052) | `Idempotency-Key`, `Abby-Run-Id` | Yes |
| A8 | Renew own claim | `POST …/tasks/{id}/claims/{claim_id}/renew` | claimant + proof | `Idempotency-Key`, `Abby-Claim-Proof` | No (automatic) |
| A9 | Release own claim | `POST …/tasks/{id}/release` | claimant + proof | `Idempotency-Key`, `Abby-Claim-Proof` | Yes |
| A10 | Assign or unassign | `PUT …/tasks/{id}/assignee` | manager, no active claim (TASK-053) | `If-Match`, `Idempotency-Key` | Yes |
| A11 | Create a task | `POST …/tasks` | `read`+`post`+`task_manage` (L1) | `Idempotency-Key` | Yes |
| A12 | Request changes on a gate | `POST …/threads/{id}/messages`, then `PATCH …/messages/{root}/labels` | `read`+`post`; `read`+`label` | `Idempotency-Key`; `expect` per op | Yes |
| A13 | Set or clear the gate flag | `PATCH …/tasks/{id}` with only `direct_action_required` | manager, direct credential (DLG-090) | `If-Match` | Yes |
| R1 | Read session identity | `GET /v1/me` | any valid credential | — | — |
| R2 | Logout (revoke own) | `DELETE /v1/credentials/{own id}` | own credential | — | No (the button says it revokes) |

Plus every read of the read-only viewer (`GET` routes, snapshot
sessions).

- **OPR-301** An operator client MUST NOT send any request that is not in
  the table above or in the read-only viewer's read set. Its `api()`
  helper MUST enforce this list in code (as the read-only `api()` does
  today), so a programming error fails closed.
- **OPR-302** Every write MUST be attributed only by the credential
  (AUTH-002). The client MUST NOT put a person's name, handle, or "on
  behalf of" in a request body to claim authorship.
- **OPR-303** Mentions: the composer MAY offer handles from
  `GET …/actors`. The client SHOULD send resolved actor IDs in `mentions`
  as well as `@handle` text. A mention is a notice to an agent. It grants
  no authority (TASK-061, design §13).
- **OPR-304** A chat message that asks an agent to do something is data.
  The client MUST NOT present a chat message, a reaction, or a label as an
  approval, an assignment, or a claim.
- **OPR-305** For A13 the client MUST send a `PATCH …/tasks/{id}` body
  with only the `direct_action_required` field. It MUST NOT change the
  title or the owner through operator mode.
- **OPR-306** The create-task form (A11) MUST offer "Needs my approval",
  which sends `direct_action_required: true` at creation (DLG-090). The
  person becomes the owner and so a manager of the task (ADR-0018
  decision 6). The form MUST NOT offer dependencies in v1.

### 4.1 Claim and release as a person

A claim is a runner tool: a lease of at most `claim_lease_max_seconds`
(300 s in the reference server), a secret proof, and renewal. A person
"working on it" in a browser fits it only while the tab is open. See
ADR-0018 §6 and owner decision 5.

- **OPR-340** After a successful claim, the client MUST renew the claim at
  about one third of the granted lease with ±10 % jitter, with a new
  `Idempotency-Key` per renewal (as RUN-002). It MUST compute its local
  deadline from local monotonic time, not from `expires_at` against the
  local clock (as RUN-003).
- **OPR-341** The client MUST show, on the task, that the person holds the
  claim and the time left on the lease. If renewal fails with `409
  claim_lost`, `403`, or `404`, or the local deadline passes, the client
  MUST stop the loop, drop the proof, and tell the person that the claim
  ended.
- **OPR-342** Release (A9) is explicit. The client MUST also release on
  logout and idle timeout (OPR-130). On tab close it MAY send the release
  with `keepalive`; otherwise the claim lapses at `expires_at` (L6).
- **OPR-343** The client MUST NOT offer complete or fail in v1 (owner
  decision 4). A person who finished the work releases, and the owner or
  an agent settles it.
- **OPR-344** A reload loses the proof (OPR-115). The client MUST NOT try
  to recover it, and MUST NOT rely on `abby.tasks.claim_proof_replay`.
  The old claim lapses. The task view shows it as the person's claim until
  then.
- **OPR-345** The client MUST offer both claim and assign-to-self (owner
  decision 5). It MUST label the claim action "Work on this now (while
  this tab is open)". It SHOULD suggest assign-to-self for work that
  takes longer than the tab session.

## 5 Confirmation, idempotency, and conflicts

### 5.1 Confirmation

- **OPR-401** Every state-changing task action (A6, A7, A9..A13) MUST
  show a confirmation step before the request. The step MUST show: the
  action verb; the task title and channel path; the current state and the
  expected new state; the current assignee and claim holder; the person's
  handle (the acting identity); and, for A6, the gate reason from the
  block. It MUST show the values that the client read, with the task
  revision it will send in `If-Match`. For A11 there is no current task:
  the step shows the channel path, title, assignee, and whether "Needs my
  approval" is set. For A13 it shows the current and the new value of
  `direct_action_required`.
- **OPR-402** The confirm button MUST name the action ("Approve and
  unblock", "Request changes", "Claim", "Release", "Assign to @x",
  "Create task", "Require my approval", "Remove approval requirement"). The default focus MUST be
  on Cancel. Enter in a text field MUST NOT confirm.
- **OPR-403** Posting, replying, editing, reacting, and ordinary label
  changes MUST NOT need a confirmation step. A label change on the `gate`
  key MUST follow §6.4.

### 5.2 Idempotency keys

- **OPR-410** The client MUST create one `Idempotency-Key` per user
  intent, at the moment the person confirms (or sends a message), from
  `crypto.getRandomValues` with at least 128 bits, encoded as visible
  ASCII (IDEM-001). It MUST NOT derive the key from content or time.
- **OPR-411** The client MUST reuse the same key for every retry of the
  same intent until the outcome is known: on a network error, a timeout,
  a `502` from the proxy, or `503` (design §5.3). A new intent MUST use a
  new key. `Idempotent-Replayed: true` is a success.
- **OPR-412** The client MUST keep pending keys in memory only, with the
  pending action. A send button MUST stay disabled while its intent is
  pending, so a double click does not make a second intent.
- **OPR-413** After a reload, the client cannot know a pending outcome. It
  MUST read the current state (thread or task) instead of resending.

### 5.3 Conditions and conflict UX

- **OPR-420** A10 and A13 MUST send `If-Match` with the task revision
  shown in the confirmation (TASK-072). The unblock in A6 MUST also send `If-Match`
  (TASK-038 makes it optional on the server; operator mode makes it
  required, so the person approves exactly the state they saw). Claim
  (A7) has no `If-Match`; the client SHOULD send `expect_brief_revision`
  with the brief revision it showed (CLAIM-005). Release (A9) is fenced by
  the proof, not by `If-Match`.
- **OPR-421** On `412 revision_conflict` the client MUST NOT retry with
  the new revision by itself. It MUST read the task again, show what
  changed (state, assignee, claim, brief revision, labels), and ask the
  person to confirm again.
- **OPR-422** On `409 invalid_transition`, `task_claimed`,
  `task_claim_active`, or `claim_lost`, the client MUST show the
  problem's `current_state` / `state` and refresh the task. It MUST NOT
  retry automatically.
- **OPR-423** On `403 forbidden` (for example `reason: not_assignee` or
  `missing_capability`) the client MUST show the reason and MUST NOT
  retry. On `404` it MUST say the item is gone or hidden (AUTH-015) and
  MUST NOT reveal more.
- **OPR-424** Replies MAY use `expect_latest_message_id` (COND-002). If
  the client uses it and gets `409 newer_message`, it MUST show the new
  messages and keep the draft. Ordinary chat SHOULD NOT use it.
- **OPR-425** Label changes on the `gate` key MUST carry an `expect`
  (§6.4).

## 6 Approving a gate

### 6.1 The record of approval

An approval is the task transition L11 (unblock) of a gate task, made by
an authorized manager with their own direct credential. It is not a chat
message, a reaction, or a label (design §13, TASK-060, TASK-061). A gate
task is a task with `direct_action_required: true` (DLG-090). The server
refuses its unblock with a delegated credential (DLG-091), so an agent
cannot approve for the person.

- **OPR-501** The client MUST record an approval only as
  `POST …/tasks/{id}/unblock` by the person, with `If-Match` and a reason,
  on a task that is `blocked` and has `direct_action_required: true`.
  The reason MUST start with `gate approved` and MAY add the person's
  note (for example `gate approved: ship to staging`).
- **OPR-502** The authoritative record of an approval is the
  `task.unblocked` event with `on_behalf_of_actor_id: null`: `actor_id`
  (the person), `credential_id`, `recorded_at`, and `payload.reason`. The
  client MUST show approval status from task events, not from labels or
  messages. A `task.unblocked` or `task.completed` event with a non-null
  `on_behalf_of_actor_id` is never an approval (DLG-096).

### 6.2 Order of the two calls

There is no atomic "unblock and change label" operation. The client reads
the task, checks it, and then does two calls in this order:

0. Check: the task is `blocked`, `direct_action_required` is `true`, and
   the person is a manager (OPR-520). The label does not matter.
1. `POST …/tasks/{id}/unblock` with `If-Match: "<task revision>"`,
   `Idempotency-Key`, `{reason: "gate approved…"}`.
2. `PATCH …/messages/{root_message_id}/labels` with
   `{operations: [{op: "set", key: "gate", value: "approved", expect:
   {value: "pending"}}]}`.

- **OPR-510** The client MUST do the unblock first. If the unblock fails,
  the client MUST NOT change the label.
- **OPR-511** If step 2 fails with `412 revision_conflict` and the
  problem shows the current `gate` value is `approved`, the client MUST
  treat it as done (a retry of an earlier success). For any other failure
  of step 2, the client MUST show "Approved; label not updated" and offer
  to retry step 2 only. It MUST NOT repeat step 1 with a new key.
- **OPR-512** A gate whose label is `approved` but whose task is still
  `blocked` (no `task.unblocked` after the block) MUST be shown as
  "label says approved; task is still blocked". A gate whose task is
  unblocked but whose label still says `pending` MUST be shown as
  approved, with the person and time from the event. A task with
  `gate=pending` and `direct_action_required: false` MUST be shown as
  "label only; not a gate", and the client MUST NOT offer Approve on it.
  A manager can set the flag first (A13).
- **OPR-513** Approve MUST NOT clear `direct_action_required`. The flag
  stays on the task, so a delegated credential still cannot settle it or
  approve a later gate on it (DLG-091). A manager who wants delegated
  agents to continue the work clears the flag with A13, a separate
  confirmed action that only a direct credential can do (DLG-090).

Why unblock first: if the label changed first and the unblock then
failed, the label would say "approved" for a task that nobody approved.

### 6.3 Who may approve

The server allows unblock only for a manager: the task owner with
`task_manage`, or an actor with `task_admin` on the task channel
(TASK-041). Agents usually create and own gate tasks, so a person who
approves them needs `task_admin` on that channel.

- **OPR-520** The client MUST show the Approve action only if the task is
  `blocked`, its `direct_action_required` is `true`, and the person is a
  manager (owner with `task_manage` in `GET …/permissions`, or
  `task_admin`). The `gate` label does not count. If the task view has no
  `direct_action_required` field (the server does not implement
  DLG-090), the client MUST NOT offer Approve; the person uses the CLI.
  The server still decides.
- **OPR-521** Board administrators SHOULD grant `task_admin` for gate
  approval on the narrowest channel pattern that holds gate tasks (for
  example `gates/*` or one project path), not on `*`.

### 6.4 The `gate` label (display only)

The gate is the task field `direct_action_required` (DLG-090) plus the
task state. The `gate` root label is a documented **display
convention**, not a protocol rule. The server does not reserve, validate,
or enforce it. (`abby.` keys are server-maintained and clients cannot set
them, LBL-002.)

| Root label | Meaning (display) | Set by |
|---|---|---|
| `gate=pending` | The task waits for a person's decision. The gate itself is `blocked` plus `direct_action_required: true`. | The actor that opens the gate, after it sets the flag and blocks the task (OPR-533). |
| `gate=approved` | A manager unblocked it (§6.2). | The operator client, after the unblock. |
| `gate=changes_requested` | A person replied with changes. The task stays `blocked`. | The operator client (A12). |

- **OPR-530** The client MUST NOT offer raw edit of the `gate` key in its
  general label editor. It changes `gate` only through §6.2, or through
  "Request changes" (A12): a reply with the requested changes, then the
  label `gate=changes_requested` with `expect: {value: "pending"}`. The
  task stays `blocked` and the flag stays set.
- **OPR-531** Agents that wait on a gate MUST wait for the task state
  (`task.unblocked`, then `task.became_ready` or state `ready`), not for
  the label. (This is guidance for agents; the reference CLI `abby wait`
  already filters by event type.)
- **OPR-532** The client MUST NOT read gate status or approval from the
  `gate` label. Gate status comes from the task state and
  `direct_action_required`; approval comes from task events (OPR-502).
- **OPR-533** An actor that opens a gate MUST set
  `direct_action_required: true` (at creation, or with
  `PATCH …/tasks/{id}` and a direct credential, DLG-090), then block the
  task, then set `gate=pending`. A `gate=pending` label without the flag
  is not a gate. (Guidance for agents and for A11; a delegated credential
  cannot set the flag.)

## 7 Provenance shown in the UI

Events and messages already record `actor_id`, `credential_id`, and
`run_id` (Event schema; CLAIM-004).

- **OPR-601** For every message and task event, the client MUST show the
  actor's handle and kind (`human`, `agent`, `service`) from
  `GET …/actors`. An actor that is not in the list MUST show as a short
  ID, never as a guessed name.
- **OPR-602** For an approval (`task.unblocked` with a `gate approved`
  reason) the client MUST show: the approver's handle and kind, the
  time (`recorded_at`), the reason, and the `credential_id`.
- **OPR-603** The client MUST send `Abby-Run-Id: operator/<random>` with
  one random value per operator session. The UI MAY show "from operator
  client" for an event whose `run_id` starts with `operator/`, and if it
  does, it MUST mark this as unverified (the run ID is client data and
  confers nothing, AUTH-002; it is never proof, DLG-107).
- **OPR-604** The client MUST NOT show a chat message, a reaction, or a
  label as the source of a state change. The task timeline MUST come from
  task events.
- **OPR-605** The header MUST show the session identity at all times in
  operator mode: handle, kind, board scope, scopes, time to `expires_at`,
  time to idle timeout, and a Logout button.
- **OPR-606** For a message or event with a non-null
  `on_behalf_of_actor_id`, the client MUST show both actors ("A for P")
  and MUST NOT show the delegator alone (DLG-104). It MUST NOT show such
  a `task.unblocked` or `task.completed` as an approval (DLG-096). The one
  provenance fact the client presents as trusted is direct or delegated
  (DLG-106; closes ADR-0018 conflict C5).

## 8 What stays impossible from the browser

These are enforced in three layers: the credential ceiling (server,
AUTH-005; the real limit), the proxy allowlist (OPR-212), and the client
allowlist (OPR-301).

- **OPR-701** Server administration: create actors, bootstrap, recovery,
  server audit. (Ceiling: no `server_admin`, no `*`, OPR-102. Proxy:
  `/v1/actors`, `/v1/audit-events` refused.)
- **OPR-702** Board administration: grants, board edits, board audit,
  redaction of another actor's message. (Ceiling: no `board_admin`.)
- **OPR-703** Channel creation. (Ceiling: no `channel_manage`.)
- **OPR-704** Credential issuance, listing, and revocation, except the
  revoke of the session's own credential. (Server: a credential with
  `may_manage_credentials: false` gets `403 credential_restricted`,
  DLG-036, OPR-106. Proxy and client also refuse. Closes ADR-0018
  conflict C1.)
- **OPR-705** Brief updates, root redaction, dependency changes, block,
  cancel, reopen, complete, fail — not in v1 (ADR-0018 owner decisions 3
  and 4). Task creation is in v1 (A11).
- **OPR-706** Acting as another actor. There is no field for it, and the
  server ignores display data (AUTH-002).
- **OPR-707** Creating, minting, or revoking a delegation. (Server: a
  credential with `may_delegate: false` gets `403 credential_restricted`,
  DLG-036, OPR-106; ADR-0019 Q2. Proxy and client refuse every
  `/v1/delegations` route.)

## 9 Conformance and UX test plan

Three kinds of test:

- `CT` = black-box protocol test in `conformance/` (a server behavior that
  operator mode depends on).
- `VWR` = Go test of the operator proxy in `examples/viewer/` (headers,
  allowlist, logging).
- `UX` = scripted browser test of the operator page against a test server
  (planned under `examples/viewer/uxtest/`, run in CI with a headless
  browser; a manual checklist until that exists).

Planned tests:

| Test | What it checks |
|---|---|
| UX-CRED-GUARD | Paste an agent token or a delegated token → read-only with reason. Paste tokens with `*`, `board_admin`, `server_admin`, `channel_manage`, `board_ids: null`, 30-day expiry, `may_manage_credentials: true`, or `may_delegate: true` → refused with the reason. On a server without `abby.credentials.restrictions` → warning shown. Paste a valid operator token → operator mode. |
| UX-STORAGE | After paste: no token in `localStorage`, `sessionStorage`, IndexedDB, cookies, `location`, `history.state`, or `document.title`. Reload → read-only login. |
| UX-IDLE | Fake clock: warning at 14 min; at 15 min the session ends, `DELETE /v1/credentials/{id}` is sent, and the claim is released. |
| UX-LOGOUT | Logout releases claims, revokes, clears memory; the old token then gets `401`. |
| UX-401 | Revoke the token with the CLI → next request `401` → page leaves operator mode and does not retry. |
| UX-XSS | Messages, titles, labels, handles, and reasons with `<script>`, `<img onerror>`, `javascript:` links, and Markdown HTML render as text; no script runs; CSP violation reports are empty. |
| UX-CONFIRM | Each of A6, A7, A9, A10 shows the confirmation fields of OPR-401; Cancel has focus; Enter in a text field does not confirm. |
| UX-IDEM | Drop the response of a confirmed unblock (proxy fault injection) → the retry reuses the key and shows success with `Idempotent-Replayed`; a double click sends one intent. |
| UX-412 | Change the task in another session between read and confirm → `412` → page shows the change and asks again; no automatic retry. |
| UX-GATE | Approve offered only on `blocked` + `direct_action_required: true` for a manager; unblock first, then label with `expect`; the flag stays set; fault after step 1 → "Approved; label not updated" and retry of step 2 only. Label-only `gate=approved` on a blocked task → shown as not approved. `gate=pending` without the flag → "label only; not a gate", no Approve. Server without the field → no Approve. Delegated `task.unblocked` → "A for P", no approval badge. Request changes → reply, then `gate=changes_requested` with `expect`; task stays `blocked`. |
| UX-TASK | Create task with "Needs my approval" sends `direct_action_required: true`; no dependency field. A13 sends only `direct_action_required` with `If-Match`. Claim action is labeled "Work on this now (while this tab is open)"; assign-to-self is offered. |
| UX-CLAIM | Claim → renew loop at about one third of the lease; lost claim → loop stops and the UI says so; release sends the proof; the proof is never in storage, DOM, or logs. |
| UX-PROV | Approval shows handle, kind, time, reason, credential ID; `operator/` run ID shown as unverified. |
| UX-ALLOWLIST | The page's `api()` refuses a request outside §4. |
| VWR-HEADERS | Page responses carry the CSP of OPR-201 and the headers of OPR-205. |
| VWR-PASS | `Authorization`, `Idempotency-Key`, `If-Match`, `Abby-Claim-Proof`, `Abby-Run-Id` reach the upstream unchanged; no cookie is set. |
| VWR-NOLOG | Proxy logs for success and upstream failure contain no `Authorization` or proof value. |
| VWR-READONLY | Without `-operator`: only `GET` and snapshot `POST`/`DELETE` pass; others get `405`. |
| VWR-ALLOWLIST | With `-operator`: each route of §4 passes; each route of OPR-212 gets `403 operator_route_not_allowed`. |
| VWR-ORIGIN | Non-`GET` with a foreign `Origin` or `Sec-Fetch-Site: cross-site` is refused. |
| VWR-LISTEN | With `-operator`, a non-loopback `-listen` fails to start. |
| CT-OPR-GATE | Unblock by a non-manager with `label` → `403`; setting `gate=approved` changes no task state (extends TestCT035); unblock by a `task_admin` person records `actor_id`, `credential_id`, reason, and `on_behalf_of_actor_id: null` in `task.unblocked`; delegated unblock of a gate task → `403 direct_action_required` (CT-DLG-12). |
| CT-OPR-CEILING | A credential with `read,post,label,claim,task_manage` cannot create grants, channels, actors, or boards; its self-issued child is no broader and expires no later; revoking it makes the child `401` (existing AUTH-005 tests plus the issuance-chain check). With `may_manage_credentials: false` and `may_delegate: false`: issue, list, revoke-other, and delegation routes → `403 credential_restricted`; revoke-self → success (CT-DLG-18). |
| CT-OPR-NOCORS | `OPTIONS` preflight to the API from a foreign origin gets no `Access-Control-Allow-*` headers. |

## 10 Traceability

| Requirement | Verification |
|---|---|
| OPR-101..104, OPR-106..107 | UX-CRED-GUARD |
| OPR-105 | UX-ALLOWLIST, VWR-ALLOWLIST |
| OPR-110..112, OPR-115 | UX-STORAGE, UX-CLAIM |
| OPR-113 | UX-IDLE |
| OPR-114 | UX-401, UX-CRED-GUARD (expiry with fake clock) |
| OPR-130..131 | UX-LOGOUT, UX-IDLE |
| OPR-132 (SHOULD) | UX-LOGOUT (pagehide case, informative) |
| OPR-133 | UX-401; CT-OPR-CEILING (chain revocation) |
| OPR-201, OPR-205 | VWR-HEADERS |
| OPR-202..204 | UX-XSS; code review check (no `innerHTML` in `static/`, enforced by a grep test in VWR-HEADERS) |
| OPR-210, OPR-214 | VWR-PASS, VWR-NOLOG |
| OPR-211 | VWR-READONLY |
| OPR-212 | VWR-ALLOWLIST |
| OPR-213 | VWR-ORIGIN |
| OPR-215 | CT-OPR-NOCORS |
| OPR-216 | VWR-LISTEN |
| OPR-301 | UX-ALLOWLIST |
| OPR-302, OPR-706 | CT existing AUTH-002 tests; UX-PROV |
| OPR-303..304, OPR-604 | UX-GATE, UX-PROV; CT-OPR-GATE |
| OPR-305..306, OPR-345 | UX-TASK |
| OPR-340..344 | UX-CLAIM, UX-LOGOUT |
| OPR-401..403 | UX-CONFIRM |
| OPR-410..413 | UX-IDEM |
| OPR-420..425 | UX-412, UX-GATE |
| OPR-501..502, OPR-510..513 | UX-GATE; CT-OPR-GATE |
| OPR-520 | UX-GATE (non-manager sees no Approve) |
| OPR-530, OPR-532 | UX-GATE |
| OPR-531, OPR-533 | Guidance; checked in agent runbooks, not by a test |
| OPR-601..603, OPR-605..606 | UX-PROV; UX-GATE (delegated event) |
| OPR-701..705, OPR-707 | VWR-ALLOWLIST, UX-ALLOWLIST, CT-OPR-CEILING |
