# Progress

Updated 2026-11-23. The `yak-dev` board is the task ledger of record (design §15.2).
This file summarizes implementation. The P0 and P1 sections retain their historical snapshots and test counts.
The P2 gate is complete. The current P3 implementation summary is at the end.
Repository implementation does not imply deployment to an existing coordination instance.

## P0 — protocol baseline: done for the P1 slice
- `spec/protocol.md` (normative, requirement IDs, traceability §10),
  `spec/openapi.yaml` (OpenAPI 3.1, 40 operations, `x-yak` semantics),
  `spec/schemas/` (48 JSON Schema 2020-12 files), `spec/examples/` (72),
  `spec/cli.md`. ADRs 0001–0003, 0005–0007, 0015 accepted.
- P2 contract drafted: `spec/p2-tasks.md`, ADR-0004, 0008, 0016 — **pending owner review**.

## P1 — messaging slice: gate PASSED (2026-11-20, commit 0c89aa3/f7bd448)
Implemented requirement IDs: DISC-001..003, WIRE-001..004, AUTH-001..007,
AUTH-010..018, AUTH-020, CHAN-001..002, IDEM-001..006, COND-001..004,
MSG-001..003, RED-001, LBL-001..003, MEN-001, EVT-001..012, SNAP-001..004,
PAGE-001.

Verification:
- `go test ./...` (unit), `YAK_TEST_DATABASE_URL=… go test -race ./internal/...`
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
- Coordination instance pinned and running; `yak-dev` seeded and in use —
  BOOT-01 met. See `examples/build-yak/EVIDENCE.md`.

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

## P2 — tasks and claims: gate PASSED (2026-11-20, commit db4ac4a)
- Contract: `spec/p2-tasks.md`, ADR-0004/0008/0016 (owner-approved), 22 more
  OpenAPI operations, 28 schemas. Server: migration 0002, domain, HTTP,
  claim-expiry sweeper (correctness does not depend on it). CLI: `yak task …`,
  resumable plan batches, `yak run` runner.
- Verification: 47 black-box conformance tests (P1 + CT-007..012, CT-025..027,
  CT-029..038, written from the spec independently of the server) pass with no
  skips; reference Postgres tests with `-race`; `scripts/p2-gate.sh` (P1→P2
  upgrade with live data, conformance, runner kill → takeover at epoch 2,
  stale proof → claim_lost). Evidence: `docs/evidence/p2-gate-2026-11-20.md`.
- Coordination instance upgraded to P2 (BOOT-05); roadmap imported (BOOT-02).

- BOOT-03 met: list pagination built by two implementer actors in separate
  worktrees, review requested changes, fix task added as prerequisite,
  re-review approved exact commits, merged without squash (9e2791a).
- Dogfooding findings filed as tasks: runner drops rejected evidence
  (`task_btzxofnctcbunv2yaouroprgly`), renewal vs task revision
  (`task_j2cnk7xuvfpropcjthn6i5sehi`), message events lack thread context
  (`task_weufwgyg7akc43pb57w4ae2wle`).

**From now on the task ledger is the `yak-dev` board** (design §15.2). This
file only summarizes; see `examples/build-yak/EVIDENCE.md` for IDs.

## Former next work package: P2 tasks and claims (done)
1. Owner review of `spec/p2-tasks.md`, ADR-0004/0008/0016.
2. Migration 0002 (tasks, briefs, dependencies, claims) + domain + HTTP.
3. Conformance CT-007..012, CT-025..027, CT-029..038; CLI `task` commands and
   `yak run` runner (PAR-033).
4. P2 gate, then import the roadmap as tasks on `yak-dev` (BOOT-02) and run
   the two-implementer + reviewer exercise (BOOT-03, BOOT-04).

## P3 actor inbox and presence implementation (2026-11-23)

The inbox bundle adds durable mention delivery, explicit acknowledgment, and policy-bound long polling.
Presence adds board-scoped self heartbeats, TTL expiry, authorized actor views, and board events.
The CLI exposes `yak inbox` and `yak presence`. Presence does not imply worker activity.
Migrations 0006 and 0007 preserve all earlier migration files.
Typed asks and command registration have proposed contracts, without runtime routes or CLI commands.
Schedule delivery, runner adapters, and viewer inbox integration remain separate board tasks.
Clients check `yak.inbox` and `yak.presence` independently in discovery.
Inbox reads never acknowledge. Prefix acknowledgment follows durable handling and includes hidden items.
Presence TTLs use whole seconds from 15 through 300. Offline carries no TTL.
Expiry applies on reads without a worker and does not prove worker activity.
The release adds schema 7 and requires migration before a new server starts.
No coordination-server deployment is recorded by this implementation summary.
