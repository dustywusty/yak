# Progress

Updated 2026-11-20. Branch `Dusty/queenfish`. This file summarizes state;
from P2 onward the task ledger of record will be the `abby-dev` board, not
this file (design §15.2).

## P0 — protocol baseline: done for the P1 slice
- `spec/protocol.md` (normative, requirement IDs, traceability §10),
  `spec/openapi.yaml` (OpenAPI 3.1, 40 operations, `x-abby` semantics),
  `spec/schemas/` (48 JSON Schema 2020-12 files), `spec/examples/` (72),
  `spec/cli.md`. ADRs 0001–0003, 0005–0007, 0015 accepted.
- P2 contract drafted: `spec/p2-tasks.md`, ADR-0004, 0008, 0016 — **pending owner review**.

## P1 — messaging slice: gate PASSED (2026-11-20, commit 0c89aa3/f7bd448)
Implemented requirement IDs: DISC-001..003, WIRE-001..004, AUTH-001..007,
AUTH-010..018, AUTH-020, CHAN-001..002, IDEM-001..006, COND-001..004,
MSG-001..003, RED-001, LBL-001..003, MEN-001, EVT-001..012, SNAP-001..004,
PAGE-001.

Verification:
- `go test ./...` (unit), `ABBY_TEST_DATABASE_URL=… go test -race ./internal/...`
  (reference Postgres tests: gapless positions under 40 concurrent writers,
  retention → 410, epoch rotation → resync, snapshot limit, tombstone overlay,
  epoch rotation during wait).
- `go test ./conformance/` against a live server: 26 tests incl. CT-002..006,
  CT-013..018, PAR-012, isolation, mentions, schema/example/live-response validation.
- `scripts/p1-gate.sh`: clean Compose, bootstrap, CLI + conformance clients,
  CT-001 restart continuity, stack restart, backup/restore + epoch rotation.
  Evidence: `docs/evidence/p1-gate-2026-11-20.md`.
- Independent review: 11 findings, all fixed (commit 7506fbe); live schema
  validation found 3 more, fixed (3595e24).

## Adoption (design §15.2)
- Coordination instance pinned and running; `abby-dev` seeded and in use —
  BOOT-01 met. See `examples/build-abby/EVIDENCE.md`.

## View-only browser (examples/viewer)
Built after the P1 gate (owner decision 2026-11-19): stdlib Go proxy + vanilla
JS, read-only guard, snapshot → long-poll replay with resync handling.
Tested via curl and headless Chrome against the coordination instance. It is
an independent client of the published contract (CT-024 evidence).

Contract feedback from building it (open spec issues):
1. Fixed: timestamps now microsecond precision everywhere (WIRE-003).
2. Non-admins cannot resolve an author ID that is no longer a visible
   participant (e.g. a board owner without grants) — need an author lookup.
3. Whether a reply emits `thread.updated`, and whether `message_count`
   counts tombstones, is unspecified.
4. `channel.created` carries empty `caller_capabilities`; capability changes
   are only signalled by a later 409.
5. `label` on `/threads` matches root labels only; on events/snapshots it
   matches message or root labels. `label=key:` (empty value) undefined.
6. Over-limit `limit`/`wait_ms` are rejected (400), not clamped — state it.
7. Snapshots accept but ignore `type` while binding the cursor to it.
8. Recovery steps for `cursor_expired` and cursor-binding 400s need prose.

## Known gaps
- H1 fix (single-snapshot event read) has no deterministic race test.
- Lists of boards, channels, grants, actors, credentials are not paginated;
  audit returns the newest 200.
- `effective_at` equals `recorded_at` (no delayed effects until P2 expiry).
- No rate limiting (429), SSE, metrics endpoint, or worker health reporting.
- Server-admin recovery grants (ADR-0002 open issue).
- No capacity measurements; per-board lock throughput unmeasured (ADR-014).
- License not yet applied (ADR-013).
- Design §11 routes not implemented: reactions, schemas, attachments, tasks
  (P2), SSE, subscriptions, deliveries, schedules, external links.

## Next bounded work package: P2 tasks and claims
1. Owner review of `spec/p2-tasks.md`, ADR-0004/0008/0016.
2. Migration 0002 (tasks, briefs, dependencies, claims) + domain + HTTP.
3. Conformance CT-007..012, CT-025..027, CT-029..038; CLI `task` commands and
   `abby run` runner (PAR-033).
4. P2 gate, then import the roadmap as tasks on `abby-dev` (BOOT-02) and run
   the two-implementer + reviewer exercise (BOOT-03, BOOT-04).
