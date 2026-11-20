# Abby conformance checklist

Version 0.2. This is a test plan, not an implemented or passed test suite. See the canonical design for the contract and sources.

### 14.1 Mandatory scenarios

| ID | Scenario | Required result |
|---|---|---|
| CT-001 | Two actors post and read across restart | Attributed content and cursors remain valid |
| CT-002 | Same successful mutation retried after a lost response | One state change and one event set |
| CT-003 | Same idempotency key with different semantic input | Explicit conflict and no new side effect |
| CT-004 | Two clients edit one revision | One accepted update; other detects conflict |
| CT-005 | Guarded reply races a new reply | Stale guard fails without appending |
| CT-006 | Concurrent label compare-and-set | One winner and consistent filter transitions |
| CT-007 | Two clients race one task claim | At most one current claim |
| CT-008 | Claim expires and another client acquires it | Old proof cannot settle or renew new work |
| CT-009 | Claim response or completion response is lost | Replay preserves original outcome without extending authority |
| CT-010 | Renewal, cancellation, and completion race | Exactly one valid serialized outcome |
| CT-011 | Prerequisite fails, reopens, then completes | Dependent readiness follows the documented rules |
| CT-012 | Batch contains a cycle or unauthorized reference | Entire atomic batch rejected |
| CT-013 | Event writer commits while reader advances | No permanent event skip |
| CT-014 | Resource enters and leaves a filtered view | Subscriber observes both transitions |
| CT-015 | Snapshot capture and replay overlap writes | Local view reaches consistent state without gaps |
| CT-016 | Permission revoked during wait or snapshot | No further protected content; explicit recovery result |
| CT-017 | Message redacted after history, receipts, and deliveries exist | Events, snapshots, idempotency replay, and unsent delivery projections do not reveal old body |
| CT-018 | Expired cursor or restored history epoch | Explicit resynchronization required |
| CT-019 | Schema is invalid or a version is redefined | Write rejected before message commit |
| CT-020 | Attachment ID copied across inaccessible channels | Unauthorized association/download rejected |
| CT-021 | Delivery accepted before sender crashes | Stable redelivery identity permits deduplication |
| CT-022 | Schedule restart or concurrent worker execution | One recorded occurrence per uniqueness key |
| CT-023 | Integration webhook repeats or echoes | No loop and no duplicate imported message |
| CT-024 | Different client implementation uses the same API | Compatible behavior without private server knowledge |
| CT-025 | A participant converts another author's thread | Rejected without explicit `task_admin`; author controls remain intact |
| CT-026 | Brief update races completion or cancellation | Terminal-state guard wins consistently; an accepted update blocks work and revokes its claim |
| CT-027 | Assignment change races an active claim | Reassignment is rejected or precedes claim acquisition; no stranded valid claimant |
| CT-028 | Authorized operator skips a failed delivery | Audited skip advances position, preserves history, and does not implicitly resume a paused subscription |

Use real Postgres in implementation integration tests for locking, transaction visibility, and recovery. An in-memory substitute cannot validate those behaviors. Keep black-box tests independent of that reference-specific choice.

Lease tests must use advertised bounds and observed server timestamps. A controlled clock can be an internal testing aid, but it cannot be a mandatory compatibility dependency or an exposed production authority.

