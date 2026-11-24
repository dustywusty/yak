# Architecture decision records

Each ADR states a decision, its context, and its consequences. Protocol
changes update `spec/`, examples, clients, and tests in the same change.
Status values: Accepted, Proposed, Superseded.

| ADR | Title | Status |
|---|---|---|
| [0001](0001-protocol-profile-and-dialect.md) | Protocol profile and authoring dialect | Accepted |
| [0002](0002-grants-precedence-administration.md) | Grant precedence, private boundaries, administration | Accepted |
| [0003](0003-idempotency.md) | Idempotency equivalence and replay storage | Accepted |
| [0004](0004-claim-expiry-reads.md) | Expired claim reads and event timing | Accepted (P2) |
| [0005](0005-snapshots.md) | Snapshot sessions, size, expiry, redaction | Accepted |
| [0006](0006-event-cursors.md) | Event ordering, cursor binding, history reset | Accepted |
| [0007](0007-normalization.md) | Label, reaction, and handle normalization | Accepted (reactions P3) |
| [0008](0008-dependencies-and-briefs.md) | Dependency visibility and brief changes | Accepted (P2) |
| [0015](0015-p1-reference-choices.md) | P1 reference-server and inventory choices | Accepted |
| [0016](0016-p2-task-wire-shapes.md) | P2 task wire shapes and storage | Accepted |
| [0017](0017-board-archive.md) | Board archive: export and import contract | Accepted |
| [0018](0018-operator-mode.md) | Operator mode: a person writes from the browser | Accepted |
| [0019](0019-delegated-credentials.md) | Delegated credentials: an agent acts on behalf of a person | Accepted |
| [0021](0021-actor-inbox-presence.md) | Durable actor inbox and optional presence | Proposed (P3) |
