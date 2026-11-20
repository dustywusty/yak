# Yak implementation agent starting brief

Design version 0.2. Read Yak_Design.md first. It is the canonical design baseline. Use PARITY_MATRIX.md, CONFORMANCE_CHECKLIST.md, and API_SURFACE.json as working indexes. BUILD_YAK_WITH_YAK.md contains the required workflow for adopting Yak during its own development.

## Assignment

Expand the design into an open, independently implementable protocol and begin the Go/Postgres reference implementation. There is no implemented service in this handoff. The API catalog is an operation inventory, not a complete OpenAPI document.

The owner chose Yak as the working name, a backend/API/CLI product, Postgres for local and shared deployments, an open specification that others can reproduce, and a capability roadmap based on Vercel Board. The owner also wants Yak to coordinate its own development as soon as the necessary capabilities pass their gates. Preserve these requirements.

The interface should stand on its own. A compatible implementation must not need Go packages, Yak hosting, Vercel credentials, direct SQL access, or private test hooks.

## First working increment

1. Inspect the existing checkout and its instructions. Preserve unrelated work.
2. Create or update the repository structure described in the design.
3. Write normative protocol requirements, OpenAPI and object/event/error schemas for the first vertical slice. Convert the blocking ADRs into concrete decisions with examples.
4. Map mandatory requirements to black-box acceptance cases. Keep reference-specific Postgres tests separate.
5. Implement discovery, authenticated actors, boards, private channel policy, grants, threads/messages, guarded writes, labels/mentions, and committed event catch-up.
6. Provide Docker Compose with Yak and Postgres, persistent storage, explicit bootstrap, and scoped credentials.
7. Provide a CLI that uses only HTTP. Exercise two independent clients, server restart, event reconnection, a guarded-write race, and channel isolation.
8. After the P1 gate passes, establish a pinned coordination instance and create an yak-dev board through public interfaces. Move ongoing design discussion, progress, blockers, and evidence links onto that board.
9. Extend to tasks and claims after authorization and state/event atomicity pass. After the P2 gate passes, import the remaining roadmap and use Yak as the authoritative coordinator for subsequent development.
10. Complete the adoption exercise in BUILD_YAK_WITH_YAK.md, then continue the phase gates for broader content, automation, and integration parity.

Record progress with specific commits, requirements, tests, and remaining gaps. Continue ordinary implementation work without repeatedly asking about reversible engineering choices. Resolve consequential protocol changes in ADRs and update the specification, examples, clients, and tests together.

## Build Yak using Yak

Use a stable, pinned coordination instance for actual development work and a separate instance for candidate builds, migrations, and tests. Isolate credentials, databases, and persistent storage; keep recovery instructions and backup access outside Yak. Test upgrades on isolated data before promoting them.

The adoption exercise requires two distinct implementer actors and an independent reviewer to finish a feature through the API or CLI. Include a dependency, discussion, exact commit-linked evidence, a reviewer decision, and recovery after a claimed worker is interrupted. Use eligible replacement workers and reject stale claim proofs. Runners pause mutating work before an unconfirmed lease could expire and reconcile ownership on reconnect.

Completed implementation tasks remain historical. Requested changes create follow-up fix tasks; the review task stays unfinished until its acceptance criteria are met. Approval covers exact commits, and changed code requires new review. Git holds code and external runners launch agents; Yak supplies shared discussion and coordination state.

Deliver examples/build-yak/ with an idempotent public-API seed, configuration templates, runner recipes, and evidence for BOOT-01 through BOOT-06. Begin with messaging after P1 and task ownership after P2; later SDKs, MCP, subscriptions, and integrations are not prerequisites. These files are an implementation assignment, not a claim that seed code or a service already exists.

## Constraints that must survive expansion

- Postgres is the reference server's database; the public protocol is storage neutral.
- A UI and agent inference runtime are outside the backend assignment.
- API, CLI, SDK, and MCP calls share authorization and semantics.
- Channels, grants, labels, message schemas, and complete task operations are part of the parity target.
- Public task state is typed; natural-language messages do not silently complete tasks or grant authority.
- Claims bind an execution attempt and reject stale operations.
- Event order is committed order within one board. Naive sequence allocation is insufficient.
- Snapshot pagination and replay must form a gap-free recovery contract.
- Redaction covers event filtering, snapshots, idempotency receipts, staged delivery bodies, and retained brief content.
- Thread conversion requires root-author task authority or explicit task administration. Live claims and terminal outcomes constrain reassignment and brief changes.
- Idempotency replay does not extend an expired claim.
- External side effects and webhook deliveries must not be advertised as exactly once.
- Later milestones retain the external integration requirements.
- Do not claim parity from endpoint counts or proposed APIs.

## Proposed defaults

The design supplies concrete defaults to review: 120-second claims, 24-hour minimum idempotency retention, 30-day event retention, 10-minute snapshot sessions, 30-second long polls, and 100-task atomic batches. Changes require a recorded rationale and updated discovery/tests; these are not established throughput or reliability measurements.

## Completion evidence for the first increment

A clean checkout starts the authenticated service through Compose; the CLI works through the public API; two independent clients exchange messages; concurrent conditional writes produce one winner; API restart preserves state and safe event continuation; private content does not leak through direct reads, listings, or event filters; migrations and basic restore steps are documented.

Report the exact commands run, outcomes, known failures, implemented requirement IDs, and the next bounded work package.
