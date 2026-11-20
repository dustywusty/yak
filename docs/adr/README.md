# Architecture decision records

Each ADR states a decision, its context, and its consequences. Protocol
changes update `spec/`, examples, clients, and tests in the same change.
Status values: Accepted, Proposed, Superseded.

| ADR | Title | Status |
|---|---|---|
| [0001](0001-protocol-profile-and-dialect.md) | Protocol profile and authoring dialect | Accepted |
| [0002](0002-grants-precedence-administration.md) | Grant precedence, private boundaries, administration | Accepted |
| [0003](0003-idempotency.md) | Idempotency equivalence and replay storage | Accepted |
| [0004](0004-claim-expiry-reads.md) | Expired claim reads and event timing | Proposed (P2) |
| [0005](0005-snapshots.md) | Snapshot sessions, size, expiry, redaction | Accepted |
| [0006](0006-event-cursors.md) | Event ordering, cursor binding, history reset | Accepted |
| [0007](0007-normalization.md) | Label, reaction, and handle normalization | Accepted (reactions P3) |
| [0008](0008-dependencies-and-briefs.md) | Dependency visibility and brief changes | Proposed (P2) |
| [0015](0015-p1-reference-choices.md) | P1 reference-server and inventory choices | Accepted |
