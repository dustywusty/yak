# Build Yak using Yak

Design version 0.2. This is the implementation workflow from section 15.2 of Yak_Design.md. Read the canonical design for authorization, lifecycle, recovery, and protocol rules.

Yak's first ongoing project is Yak itself. This is a required adoption exercise and a source of product feedback. It adds no agent inference engine, Git host, or special private API to the backend. External runners launch agents and provide their tools; Git stores code; Yak records the shared conversation, ownership, dependencies, and results. Publish specification changes and ADRs in the repository, with links to the relevant board discussions.

Begin in two steps. After P1's authorization, durable messaging, event recovery, and CLI checks pass, create a `yak-dev` board and use it for design questions, progress, blockers, decisions, and evidence links. Until tasks are available, keep the implementation plan in the repository; a message saying someone has claimed work does not create a lease. After P2's task, claim, race, and recovery checks pass, import the remaining roadmap as tasks and use Yak as the authoritative coordinator for subsequent development. Repository checklists may summarize that state with task links; do not maintain a competing mutable task ledger.

Use the public HTTP API or CLI for setup and coordination. Initial adoption uses ordinary messages and links, so it does not depend on later typed schemas, uploaded files, schedules, webhooks, or MCP. Give each agent its own actor and scoped credential. Keep bootstrap and recovery credentials outside ordinary runner environments.

## Board and development workflow

Seed channels such as `roadmap`, `spec`, `server`, `cli`, `review`, and `ops`, with explicit grants. A planning or coordinating actor turns the next milestone into bounded tasks, records acceptance criteria, and links dependencies. Two implementer actors work in separate Git branches or worktrees and claim only eligible tasks. An independent reviewer records findings against exact commit IDs. Roles are workflow conventions enforced through ordinary credentials and grants; they are not privileged backend agent classes.

An implementation task may complete when its agreed patch and verification evidence are available. Create a separate review task that depends on the implementation work. Its acceptance criteria require an explicit approval of identified commits. A review requesting changes blocks or leaves the review task unfinished and creates follow-up fix tasks. Follow the normal claim revocation and dependency-edit rules when adding those prerequisites. Completed implementation tasks stay historical. If code changes after approval, create a new review task for the new commits. Completing an implementation or review task does not itself merge a branch or authorize a release; the repository's existing integration policy still applies.

Every development task's result includes its task/thread references, branch or PR when available, exact commit IDs, checks run and their outcomes, remaining limitations, and follow-up task links. A narrative claim that tests passed is insufficient evidence. Reviewer approval and later integration record the precise commits they cover.

## Stable coordination and isolated development

Run the `yak-dev` board on a stable coordination instance pinned to a tested server image or build digest and a compatible CLI version. Run candidate builds in a separate development instance. Separate their endpoint configuration, credentials, database identities, databases, and persistent storage. Development migration credentials must not have authority over coordination data. Both deployments can share one machine; shared infrastructure must still preserve these boundaries.

Test candidate upgrades and migrations on isolated data, then back up the coordination instance and promote deliberately. Record the old and new versions, migration outcome, and recovery procedure. Rolling back a binary is valid only if the resulting database schema remains compatible; otherwise follow the tested restore or forward-repair procedure. Keep deployment configuration, recovery instructions, and backup access available outside Yak so an outage does not prevent restoring it. Restore behavior must preserve the history-epoch rules in section 13.

During a coordination outage, clients must not invent ownership or silently start a second authoritative task ledger. Managed runners pause mutating work before their last confirmed lease could expire if renewal cannot be confirmed. Preserve notes and patches outside Yak, then reconnect, replay events, and reacquire ownership through the API before resuming. Retried mutations keep their original idempotency keys. External side effects still require their own deduplication or fencing.

## Adoption acceptance criteria

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

## Repository example to deliver

Create `examples/build-yak/` during implementation. It must contain a README, an idempotent board/channel/roadmap seed using the public API, configuration templates with no real secrets, external-runner recipes, and an evidence index for BOOT-01 through BOOT-06. Seed routines resolve existing resources and preserve task-ID mappings across reruns, including after idempotency receipts expire. They must never overwrite existing discussion or reset completed work.

Document server discovery and required capabilities, the stable coordination endpoint, the separate development endpoint, per-actor credentials, Git worktree setup, lease renewal and shutdown behavior, and how to collect results. Freeze executable examples only after the corresponding API and CLI syntax are specified and working. This design package supplies the assignment and criteria; it does not contain a working seed or runner integration yet.
