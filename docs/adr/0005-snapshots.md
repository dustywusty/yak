# ADR-0005 Snapshot sessions

Status: Accepted (2026-11-19). Implements design §8.3.

## Decision
- `POST …/snapshots` runs one REPEATABLE READ transaction: evaluate policy,
  read the board head, select readable threads with ≥1 matching message,
  write `thread` and matching `message` items into `snapshot_items`, commit.
  Pages read materialized items; no transaction spans client think time.
- Filter = same query parameters as `GET …/events`; the continuation cursor
  is bound to that filter, so replay uses the identical filter.
- Size: at most 10,000 items (`limits.snapshot_max_items`), else `413`.
- Lifetime: 10 minutes (`retention.snapshot_seconds`), then `410
  snapshot_expired`. Owner-only; other actors get `404`.
- Authorization is rechecked per page; a policy-fingerprint change marks the
  session invalid and returns `409 resync_required`.
- Redaction: deleting a message rewrites its snapshot items to the tombstone
  in the same transaction as the deletion.
- Expiry projection for tasks is deferred to P2 (ADR-0004).
- Cancellation: `DELETE …/snapshots/{id}`.
