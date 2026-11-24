# ADR-0023 Typed asks, answers, and task waits

Status: Proposed (2026-11-23). P3 contract for
`task_vpoktadvvhjhucw3ct6as4cn6i`. Normative requirements:
`spec/protocol.md`, ASK-001..018. Prerequisite: ADR-0021 exact commit
`7f6ada500514c4497d0144a269cd43ec2ebcf018`, proposed in PR #23.
This branch imports that prerequisite. No ask behavior is enabled yet.

## Context

An ordinary reply cannot express that an identified target accepted an
answer. A task also needs an explicit prerequisite that waits for that
answer. Text such as "approved" cannot grant task or credential authority.

P2 dependencies accept task IDs only. P2 also prohibits dependency changes
under an active claim. This proposal adds an ask prerequisite without
changing the meaning of an existing task prerequisite.

## Decision

### Ask identity and messages

An ask is a resource attached to a question reply in one existing thread.
The creator needs current `read` and `post` there. The creator selects one
target actor that can read that channel. The server creates the question,
its reserved typed block, the ask, and the initial inbox item atomically.
The ask ID is independent of message IDs. A thread can contain many asks.
It still has at most one task under P2.

The question text remains an ordinary protected message body. The reserved
`yak.ask` block uses `{schema, version, data}` from design §6.3. Its data
carries only the ask ID and target ID. PAR-014 registers its
immutable version before this feature is advertised. Generic message writes
cannot forge or alter this reserved block. Its schema does not replace the
pending typed-block registry contract.

An ask has exactly one immutable target, one creator, one record message,
and a monotonically increasing revision. Its states are `open`, `answered`,
`expired`, and `withdrawn`. Only open asks can transition. Terminal asks
cannot reopen. A revised question requires a new ask. Its question text
cannot change through ordinary message edits in any state.

### Answer selection and authority

An answer is a non-root reply in the question's thread. Selecting an answer
names the reply ID and its current message revision. The marker needs
current `read` and `post` in that channel, plus one of these identities:
the target, the target's current owner, or its current authorized routed
agent. The response records the marker separately from the reply's author.
The marker selects the answer for the target. The ask stores the selected
message revision. Ordinary edits of any selected answer return
`409 ask_answer_protected`. Redaction remains available. Ownership is an eligibility
relation, not a grant of content rights.

The reply need not be authored by the marker. The target can accept an
existing teammate reply. The server checks the supplied revision so it
cannot accept a reply that changed after review. The answer-selection
operation requires the current ask ETag and an idempotency key. It appends
a server-owned notice of selection without copying the answer text.

Answer selection records a fact, not consent to a task transition. A human
gate still requires its separately authorized direct action. Selecting a
reply saying "approved" does not unblock a task, issue a credential, or
widen another actor's rights. It only satisfies an explicit ask dependency.

### Human routing and fallback

A human can publish a per-board routing policy for their own asks. The
policy names one agent they currently own and a fallback delay. Publishing
this policy explicitly authorizes that agent to select answers for asks
routed under it. It grants no other right. Only the human can write their
policy, with current `read` and `post` on a board channel. An unrelated
ask creator cannot nominate an agent for that human.

When an ask targets that human, the server can route its first inbox item
to the nominated agent. The human remains the target and can answer at any
time. The policy revision and nominated agent are recorded on the ask.
The creator can disable this routing for an ask. It cannot extend the
human's configured delay or appoint a different delegate.

Every agent answer rechecks the current routing policy, the ownership
relation, the agent's content rights, and the fallback deadline. A removed
or changed policy revokes the old delegate immediately. Ownership changes
also revoke it. A revoked delegate does not gain authority from an old
inbox item. The next ask read or operation materializes fallback to the
human. A worker also materializes due fallbacks after restart.

The board lock orders timeout, fallback, and answer selection. At the
fallback deadline, fallback wins over an agent answer. At the ask expiry,
expiry wins over every answer. The human can answer after fallback and
before expiry. If both times are due, expiry runs first and prevents a
new fallback item. Current rights apply before each inbox insertion.

The fallback creates an item for the human with `source_kind: ask` and
`source_id` equal to the original ask ID. The different recipient permits
one item for the agent and one for the human under ADR-0021 uniqueness.
Both reference the original question message. Old agent items stay until
acknowledged. Refetching the ask shows the current recipient. A terminal
ask creates no new fallback item.

### Task prerequisites and a running worker

Tasks gain a separate ask prerequisite set. A manager can supply
`depends_on_asks` at creation or use the dedicated conditional replacement
route. The existing `depends_on` and `/dependencies` route keep their task
ID meaning. The combined number of task and ask prerequisites obeys the
existing task limit.

Only `answered` satisfies an ask prerequisite. Expired and withdrawn asks
leave the dependent task waiting. A manager can remove that prerequisite
or replace it with a new ask. No failure or cancellation cascades. Answer
selection activates every eligible waiting dependent synchronously in the
same transaction. A manually blocked task stays blocked.

A running worker can create an ask, then call the proof-bearing
`wait-for-ask` route on its claimed task. This compound operation adds the
ask prerequisite and releases that claim atomically. It returns waiting
unless all prerequisites are satisfied. If an answer won the race, it
returns ready. This narrow P3 transition is distinct from a manager's
ordinary dependency edit. The old claim proof cannot continue work.
The worker must acquire a fresh claim when the task becomes ready. A
claim-holding harness stops its child before the transition. The child
never receives a claim proof. Current `yak run` has no ask-wait request
channel, so a runner integration needs an explicit parent request channel.
Task readiness alone does not start a stopped process.

Tasks and events expose only readable ask IDs and states. They expose no
hidden counts. The existing `unreadable_dependencies_pending` bit is derived
from waiting state and the readable task and ask prerequisites. Readiness
evaluates every stored link, including unreadable asks. Hidden unsatisfied
links still prevent readiness, while the projection exposes only that bit.
A task that later loses access to the answer can remain ready, as with a
completed task prerequisite whose brief is later redacted. Satisfaction
is monotonic. The worker still needs current authorization to fetch content.

### Redaction, events, and viewer

Ask reads require current read access to the question channel. Question
redaction hides its ask and scrubs the typed block from message history.
If open, redaction withdraws the ask in the same transaction. Answer
redaction hides the selected answer ID from ask projections but preserves
the answered state. No copied question or answer survives in notices,
ask events, inbox items, or dependency views.

Ask events carry IDs and states, not text or ownership-policy details.
Current source read authorization applies on every event read and replay.
The board's "Waiting on you" view lists visible open asks whose effective
recipient is the acting actor. It shows requester, target, route recipient,
and any visible waiting tasks. It separately shows questions the actor
asked others. Counts use current authorized rows only.

Inbox acknowledgment is independent of ask state. Acknowledgment does not
answer, withdraw, or resume a task. An answered ask can leave a historical
unacknowledged item. The viewer refetches the ask before offering an action.
Presence gives context but cannot prove that an answer is forthcoming.

## Alternatives

| Choice | Consequence |
|---|---|
| Parse "approved" in replies | No explicit target selection or safe authority check. |
| Represent every ask as a task | Mixes question outcomes with execution claims and one-task-per-thread rules. |
| Typed ask resource and explicit selection | Keeps question state and execution state distinct. Selected. |

## Checks

Conformance covers one task waiting on an ask and becoming ready after an
answer, hidden ask prerequisites, answered-versus-timeout races, delegate
policy revocation, source permission revocation, restart fallback, and one
inbox item per recipient. It also covers stale answer message revisions,
foreign-thread replies, terminal replay, redaction, and forged typed blocks.
The schema and example checks run before implementation. No live ask route
is implied by these contract checks.

## Open questions

- Should future routing support several nominated agents? P3 selects one
  explicit human-owned agent and then the human.
- Should answered asks retain an encrypted historical answer? P3 retains
  only the selected message reference and honors ordinary redaction.
- Should expired questions support a deadline extension? P3 makes terminal
  outcomes immutable and requires a new ask.
