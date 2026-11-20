# ADR-0017 Board archive: export and import contract

Status: Proposed (draft for owner review, 2026-11-20). Not approved for
implementation. Normative text: `spec/archive.md` (ARC-001..122,
CT-039..048). Builds on ADR-0002, ADR-0003, ADR-0004, ADR-0005,
ADR-0006, ADR-0016. Abby task: `task_om5lfbm7iokinjzcnvpd72jzle`.

## Context

The owner asked for a portable export and import of one board that does
not depend on Postgres. `pg_dump` works only for the reference server, holds
the whole deployment (credentials included), and cannot move one board to
another server or another implementation.

An archive crosses the trust boundary of the server. So it must keep every
rule that the live API keeps: no secrets, no content the exporter cannot
read, no erased text, and no counts of hidden things. It must also keep the
task rules: no claim may become live on another server, and epochs must
stay valid fencing tokens.

## Decision

1. **Format.** One `.tar.gz` (gzip of POSIX tar). First entry
   `manifest.json`; then one JSON file for the board and one JSON Lines file
   per record kind, in a fixed order that lets an importer stream and
   resolve every reference to an earlier record. The manifest holds the
   format version (`MAJOR.MINOR`), protocol version and profiles, source
   implementation, source board ID and name, source history epoch, export
   position (full exports only), a source continuation cursor, the
   exporter, the partial-export declaration, and per-file SHA-256, byte
   count, and record count.
2. **Records reuse the public schemas.** `Grant`, `Brief`, `Claim`, and
   `AuditEvent` are used as they are. `Board`, `Actor`, `Channel`,
   `Thread`, `Message`, `Task`, and `Event` get archive variants that drop
   per-caller fields (`caller_*`), derivable fields, cursors, and
   credential IDs, and add only what an importer needs (`stub`,
   `source_order`, `source_created_order`, `dependencies_omitted`). The
   archive schemas go under `spec/schemas/archive/` with the code.
3. **Never exported:** credentials (tokens, verifiers, records, IDs),
   claim proofs and their verifiers and replay ciphertext, idempotency
   receipts, snapshot sessions, cursors (except one source cursor that
   only works on the source), server-admin flags, server-level audit, and
   reference storage details. Each is bound to one server or is a secret.
4. **One consistent capture.** All state is as of one export position P;
   all exported events are ≤ P. Reference: one `REPEATABLE READ READ ONLY`
   transaction, no board lock. Commit order = position order (ADR-0006)
   makes the head read inside that transaction exact.
5. **Asynchronous export job, not a streaming download.** `POST …/exports`
   → poll → `GET …/exports/{id}/archive`. The capture ends before any byte
   goes to the client.
6. **Export authorization.** `board_admin` and current `read` on each
   exported channel. No bypass. Channels the exporter cannot read are left
   out with their content, events, edges, grants based inside them, and
   audit about them. The manifest says only `partial: true` and which kinds
   were left out — no counts, IDs, paths, or source position. Private
   ancestors of exported channels are kept as empty stubs so the private
   boundary survives the move.
7. **Redaction.** The exporter writes only tombstones and scrubbed
   projections. A redaction or policy change after P invalidates the export
   before download. The importer re-applies the live scrub to every
   tombstone and rejects a tombstone that carries text.
8. **Import authorization and shape.** Server administration. Always a new
   board, all or nothing, in one commit. Never a merge in format 1. The
   importer gets counts and the ID mapping, never content.
9. **IDs: keep, remap, or auto — never mixed.** `auto` (default) keeps all
   IDs if all are free and valid, else remaps all. Every import publishes
   its mapping. `remap` rewrites IDs inside event payloads, so an unknown
   event type is rejected.
10. **Actors.** Never with credentials. Mapped by an explicit `actor_map`,
    or by handle only if the importer asks (`actor_matching: "handle"`),
    else created as disabled placeholders. `any_actor` grants are imported
    as revoked unless the importer asks to keep them.
11. **Claims arrive ended.** An active claim becomes `expired` with
    `ended_at = min(expires_at, import time)`; the task becomes `ready`.
    Epoch, retry count, outcomes, briefs, and claim history are kept, so the
    next claim on the target has epoch + 1. A dependent whose prerequisite
    was left out by a partial export arrives `blocked` (cause
    `imported_dependency_omitted`).
12. **Events.** Kept in order with original `type`, `recorded_at`,
    `effective_at`, revisions, and payloads (IDs mapped; event IDs follow
    `id_mode`). New positions on the target. The import appends its own
    events last (claim expiries, blocks, then `board.imported`). The new
    board gets its own history epoch, so no old cursor can silently skip.
    Retention of imported events counts from the import.
13. **Attribution.** One revoked credential per import, owned by the
    importer and labeled `import:<id>`, is the `credential_id` of every
    imported message and event. It keeps attribution truthful and keeps the
    reference `NOT NULL` foreign keys.
14. **Integrity.** The importer checks container rules, version, every file
    hash and count, every schema, every reference, the P2 invariants, and
    the redaction rules before it creates anything. Unknown major →
    `422 archive_unsupported`. Size limits stop decompression bombs.
    Integrity is not authenticity; signing is deferred.
15. **Discovery.** Capabilities `abby.archive.export`,
    `abby.archive.import`, `abby.archive.v1`; profile `abby.archive`;
    limits `archive_max_bytes`, `archive_max_uncompressed_bytes`,
    `archive_max_line_bytes`; `retention.archive_seconds`.
16. **CLI.** `abby board export -o FILE`, `abby board import FILE`, and an
    offline `abby board verify-archive FILE`.

## Alternatives considered

| Topic | Alternative | Why not |
|---|---|---|
| Format | One large JSON document | Must be held in memory to parse; no streaming; one bad byte spoils all. |
| Format | ZIP | Random access is not needed; ZIP has more ways to hide entries (central directory vs local headers). Tar + gzip is in every toolchain. |
| Format | SQLite file | Ties readers to one library and one storage model; not an open wire format; harder to diff and to hash per record kind. |
| Format | The event log only, replayed on import | Retention removes old events, so state cannot be rebuilt from them. Replay would also run lifecycle rules again and could produce a different state. |
| Format | `pg_dump` per board | Reference-specific; carries credentials and receipts; no cross-implementation import. |
| Records | Brand-new archive schemas for every kind | Two schemas to keep in step for each resource. Reuse keeps the archive close to what clients already validate. |
| Consistency | Page the live tables with the public API | Not one view; a write between pages gives a state that never existed (design §8.3). |
| Consistency | Take the board lock for the whole capture | Stops all writes on a busy board for the whole export. |
| Delivery | Streaming download in the request | Holds the database snapshot open for as long as the client reads (client think time, forbidden by design §8.3). The manifest hashes would have to come last, so an importer could not check the version first. |
| Authorization | Board admin alone is enough | A hidden read bypass for admins (design §4.1, ADR-0002 §6). |
| Authorization | Fail the export if any channel is unreadable | Forces admins to grant themselves read on everything to get any copy. Partial export with a clear flag is safer and still lets them get a full copy if they choose. |
| Partial | List omitted channel paths or counts | AUTH-015 forbids counts and names of hidden resources; the archive leaves the server. |
| Partial | No stub channels | An exported child of an omitted private channel would lose its boundary; a `*` grant would then open it on the target. |
| Import | Merge into an existing board | Conflicts in IDs, positions, policy, and task graphs; needs its own design. Out of scope for format 1. |
| Import | Board admin can import | Import creates actors and boards, which need server administration (AUTH-006). |
| IDs | Keep each ID if free, else remap only that one (the brief's proposal) | A mixed set is hard to reason about and to test; a partial collision usually means "this archive was already imported", where a full remap is what the operator wants. |
| IDs | Always remap | Breaks every stored link (task IDs in commits, docs, runner configs) on every move, even to an empty server. |
| Actors | Always match by handle | A handle on two servers can be two different people; grants would follow the wrong person. Kept as an opt-in. |
| Actors | Import credentials | Moves authentication secrets between servers; a leaked archive would be a leaked login. |
| Policy | Keep `any_actor` grants active | `any_actor` means every actor of the target server, which can be a much larger set. |
| Claims | Keep active claims active | The proof is not exported, so no one could renew or settle; the source claimant could still be working there. |
| Claims | End them as `revoked` | `revoked` means a manager action. When the import is after `expires_at` (the usual case, leases ≤ 300 s), the ADR-0004 lapse is the exact truth. |
| Claims | Reset epochs to 0 | External systems that fence on epoch would accept a stale writer. |
| Events | Keep source positions | Partial exports would show gaps, which count hidden events; positions of two servers would mix. |
| Events | New event IDs always | Loses the link between source and target history for audit and evidence; `keep` mode is safe because IDs are unique per deployment. |
| Epoch | Rotate the server-wide epoch after each import | Forces every client of every other board to resync. |
| Retention | Count from `recorded_at` | An old board would lose its history at the next sweep after import. |
| Attribution | `credential_id: null` on imported records | Breaks the public `Message` schema (required string) and the reference foreign keys. |
| Integrity | Trust the archive's redaction state | A crafted or buggy archive could reintroduce erased text. |
| Integrity | Signed manifests in format 1 | Needs a key model (whose key, how to trust it). Deferred; hashes still catch damage. |

## Consequences

- Protocol additions (need owner approval): profile and capabilities, the
  routes and shapes of `spec/archive.md` §11, status `422` with
  `archive_invalid` and `archive_unsupported`, `409 archive_id_conflict`,
  `410 export_expired`, block cause `imported_dependency_omitted`, event
  type `board.imported`, and four audit actions.
- **Amends ADR-0006 / EVT-009:** the history epoch in a cursor becomes
  (server epoch, board epoch). Native boards have an empty board epoch, so
  existing cursors stay valid.
- **Amends WIRE-001 for archive records only:** unknown optional fields
  of a higher minor are ignored. Request bodies stay strict.
- Reference schema changes: `boards.history_epoch`, `events.imported_at`,
  export and import job tables, and an archive file directory.
- The conformance suite needs a second server (base URL and server-admin
  credential) for CT-039.
- P3 resources (reactions, schemas, attachments) need a minor version.
  Attachment bytes may need a separate file set or a size class of their own.

## Open questions for the owner

1. Is `id_mode: auto` (keep all if possible) the right default, or should
   the board ID always be new?
2. Is handle matching acceptable as an opt-in, or should only an explicit
   `actor_map` be allowed?
3. Default for `any_actor` grants: revoke (proposed) or keep?
4. Do we need a read-only board mode for cutover, so the source cannot
   change after export (spec §14)? Format 1 has none.
5. Should a full-export manifest for admins carry counts of omitted
   records? Proposed: no counts at all.
6. Retention clock from import time (ARC-099): agree?
7. Conformance: may the suite take a second server and a server-admin
   credential? Design §14 allows test credentials; a server-admin one is
   new.
8. Export file storage in the reference server: local directory now;
   shared storage with the attachment store (ADR-009) later?
9. Placeholder actors: there is no API yet to enable a disabled actor.
   Add `POST /v1/actors/{id}/enable` with the archive work, or keep it an
   operator command?
10. Claim end status on import: `expired` (proposed) or a new
    `ended_by_import` value?
