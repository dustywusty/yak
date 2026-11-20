# ADR-0006 Event ordering, cursors, and history reset

Status: Accepted (2026-11-19). Implements design §8 and §12.1.

## Decision
- Every board mutation locks `board_heads` (fixed order: idempotency lock →
  board row → resource rows), allocates positions `head+1…` inside the
  transaction, writes events, and commits. PostgreSQL makes a commit visible
  before releasing its locks, so commit order equals position order and a
  reader using `position > cursor` cannot skip a later-visible event.
  No sequences are used for positions.
- Cursor = `c1.` + base64url(position, board ID, history epoch, filter hash,
  policy fingerprint). Opaque to clients; forging one cannot widen access
  because authorization is applied to every returned event.
- Policy fingerprint = hash of the actor's applicable grants, the board's
  private paths, the credential scopes, and ownership. Creating an inherited
  channel does not change it; grants and private channels do.
- History epoch lives in `server_state` and is rotated by the operator
  after a restore (`abby admin rotate-epoch`). Mismatch → `409
  resync_required` (`history_reset`). A cursor ahead of the head is treated
  the same way.
- Retention: a sweeper deletes events older than 30 days under the board
  lock and raises `retained_from_position`; older cursors get `410
  cursor_expired`.
- Long polls hold no DB connection while sleeping: one LISTEN connection per
  process wakes waiters; waiters also re-query every 2 s.

## Consequences
Per-board serialization is the throughput ceiling; to be measured (ADR-014).
