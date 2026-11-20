# Abby board archive, format 1 — optional profile `abby.archive`

Status: accepted (owner, 2026-11-20). The decisions, the owner's answers,
and the alternatives are in ADR-0017. This document extends
`spec/protocol.md` (`abby.p1.core`) and `spec/p2-tasks.md`
(`abby.p2.tasks`). Every P1 and P2 requirement still applies unless a
requirement here amends it by ID. The wire shapes in §11 move into
`spec/openapi.yaml` and `spec/schemas/` with the implementation.

The key words MUST, MUST NOT, SHOULD, and MAY are used as in RFC 2119.
Every MUST maps to a check in the traceability table (§15).

Design baseline: `docs/design/Abby_Design.md` §3, §4.1, §4.2, §5.1, §6.1,
§7.2, §8, §12.1, §13.2, §14. Points of tension are in §16.

## 0 Purpose and limits

A board archive is a portable file that holds one board: its policy, its
conversations, its tasks, its retained events, and its audit metadata. One
Abby server exports it. Another Abby server, from any implementation,
imports it as a new board.

An archive is not a backup:

- It holds one board, not a deployment.
- It holds no credentials, no claim proofs, and no idempotency receipts.
- An import makes a new board with a new history. Clients must take a new
  snapshot after the import. They cannot continue from old cursors.

Operators still use `pg_dump` or point-in-time recovery for disaster
recovery (`docs/operations.md`). An archive is for moving a board to a
different server, for an independent copy in an open format, and for
conformance tests between implementations.

## 1 Terms

| Term | Meaning |
|---|---|
| Archive | A `.tar.gz` file in the format of §3. |
| Source | The server and board that made the archive. |
| Target | The server that imports the archive. |
| Capture | The one consistent read of source state (§6). |
| Export position | The source board head at capture. Every exported event has a position at or below it. Exported state is the state at that position. |
| Exporter | The actor that requested the export. |
| Importer | The actor that requested the import. |
| Omitted channel | A source channel that the exporter cannot read at capture. Its content is not in the archive. |
| Stub channel | A private ancestor of an exported channel that is itself omitted. The archive keeps only its path and mode (§7.3). |
| Mapping | The list of (kind, source ID, target ID) pairs that an import used (§9.2). |

## 2 Discovery

- **ARC-001** A server that implements export MUST list
  `abby.archive.export` in `capabilities`. A server that implements import
  MUST list `abby.archive.import`. A server MAY implement only one of them.
  A server that lists either MUST list `abby.archive` in
  `protocol.profiles`, and MUST list `abby.archive.v1` for each archive
  format major it reads or writes (only `v1` in this document).
- **ARC-002** A server that lists either capability MUST add these
  `limits`: `archive_max_bytes` (compressed archive; reference 268435456),
  `archive_max_uncompressed_bytes` (sum of all entry sizes; reference
  2147483648), and `archive_max_line_bytes` (one JSON Lines record;
  reference 1048576). It MUST add `retention.archive_seconds` (how long a
  finished export file or an import upload is kept; reference 3600).

## 3 Container format

- **ARC-010** An archive MUST be one gzip stream (RFC 1952) that holds one
  POSIX tar stream (ustar or pax). Media type: `application/gzip`. File
  extension: `.abby.tar.gz`.
- **ARC-011** Every tar entry MUST be a regular file. Entry names MUST be
  names from the file list (§4) with no directory part. Directories,
  links, device files, absolute names, names with `/` or `..`, and
  duplicate names MUST cause the importer to reject the archive.
- **ARC-012** The first entry MUST be `manifest.json`. An importer MUST be
  able to check the manifest before it reads any other entry.
- **ARC-013** Every `.jsonl` entry is JSON Lines: UTF-8 without a byte
  order mark, one JSON object per line, each line ending in LF (also the
  last line), no blank lines. An empty file has zero bytes. No line may
  exceed `archive_max_line_bytes` of the importer.
- **ARC-014** Every record MUST be valid against its schema (§4). The
  record order in each file is fixed (§4) so that an importer can stream
  the files in the order of §4 and resolve every reference to an earlier
  record or an earlier file.
- **ARC-015** An exporter SHOULD produce identical entry bytes for two
  exports of the same board at the same position by the same exporter
  (sorted object keys, the fixed record order, tar entry times equal to
  `exported_at`, owner and group 0, mode 0644). This makes archives easy
  to compare in tests. It is not a security property.

### 3.1 Manifest

`manifest.json` is one JSON object (schema `archive/Manifest`):

```json
{
  "format": "abby.board_archive",
  "format_version": "1.0",
  "protocol": {
    "name": "abby",
    "version": "1",
    "profiles": ["abby.p1.core", "abby.p2.tasks"]
  },
  "source": {
    "implementation": {"name": "abby-reference", "version": "0.9.0", "schema_version": 3},
    "board_id": "brd_src",
    "board_name": "abby-dev",
    "history_epoch": "ep_7q2…",
    "position": 18342,
    "continuation_cursor": "c1.…"
  },
  "export": {
    "id": "exp_…",
    "exported_at": "2026-11-20T09:00:00Z",
    "exporter": {"actor_id": "act_lead", "handle": "lead"},
    "partial": false,
    "omitted_kinds": [],
    "events_truncated": false
  },
  "files": [
    {"name": "board.json", "schema": "archive/Board", "sha256": "…", "bytes": 412, "records": 1},
    {"name": "actors.jsonl", "schema": "archive/Actor", "sha256": "…", "bytes": 2210, "records": 9}
  ],
  "extensions": {}
}
```

- **ARC-020** `format` MUST be `abby.board_archive`. `format_version` is
  `MAJOR.MINOR`. An importer MUST reject an archive whose major it does not
  support (`422 archive_unsupported`). A higher minor of a supported major
  only adds optional record fields and optional files. The importer MUST
  ignore unknown optional record fields and MUST ignore a file whose
  manifest entry has `"optional": true` and whose name it does not know. It
  MUST reject any other unknown file. This is the one place where Abby
  accepts unknown input fields (amends WIRE-001 for archive records only;
  request bodies are unchanged).
- **ARC-021** `files` MUST list every entry except `manifest.json`, exactly
  once, with the SHA-256 (lowercase hex) of the uncompressed entry bytes,
  the byte count, and the record count. An entry that is not listed, or a
  listed file that is missing, MUST cause rejection.
- **ARC-022** `source.history_epoch` and `source.position` record the
  capture point. `source.continuation_cursor` is an event cursor of the
  source, for the exporter, with no filter, at the export position. The
  exporter can use it on the source to read every change after the
  capture (for example during a move to another server). It has no
  meaning on any other server. Only a full export puts it in the manifest
  (ARC-023). The Export job view gives it to the creator of every export
  (§11.1).
- **ARC-023** If the export is partial (§7), `source.position` and
  `source.continuation_cursor` MUST be omitted from the manifest,
  `export.partial` MUST be `true`, and `export.omitted_kinds` MUST list
  each kind of record that was left out (`channels`, `grants`,
  `audit_events`, `dependencies`). The manifest MUST NOT contain a count,
  an ID, or a path of any omitted record. (A position number lets a reader
  count events in omitted channels; AUTH-015 forbids that. A cursor holds
  the position, ADR-0006, so anyone with the file could decode it.)
- **ARC-024** `export.events_truncated` MUST be `true` if the source had
  removed events of the board by retention before the capture. The state
  files are complete either way. Only event history can be truncated.
- **ARC-025** The manifest MUST NOT contain secrets. `extensions` keys are
  namespaced (`vendor.name`), as in WIRE-001.

The manifest is not signed in format 1. The file hashes detect damage in
transit. They do not prove who made the archive (see §10, ARC-107).

## 4 Files and records

Files appear in this order. Each record schema is the named public schema
with the listed changes. New schemas go under `spec/schemas/archive/`
with the implementation; this section is their normative description.

| # | File | Record schema | Order of records |
|---|---|---|---|
| 1 | `manifest.json` | `archive/Manifest` | one object |
| 2 | `board.json` | `archive/Board` | one object |
| 3 | `actors.jsonl` | `archive/Actor` | `id` ascending |
| 4 | `channels.jsonl` | `archive/Channel` | `path` ascending (parents first) |
| 5 | `grants.jsonl` | `Grant` | `(created_at, id)` |
| 6 | `threads.jsonl` | `archive/Thread` | thread creation order |
| 7 | `messages.jsonl` | `archive/Message` | thread creation order, then `seq` |
| 8 | `task_batches.jsonl` | `archive/TaskBatch` | `(created_at, id)` |
| 9 | `tasks.jsonl` | `archive/Task` | task creation order |
| 10 | `task_briefs.jsonl` | `Brief` | task creation order, then `revision` |
| 11 | `task_dependencies.jsonl` | `archive/Dependency` | `(task_id, depends_on_task_id)` |
| 12 | `claims.jsonl` | `Claim` | task creation order, then `epoch` |
| 13 | `events.jsonl` | `archive/Event` | source position ascending |
| 14 | `audit_events.jsonl` | `AuditEvent` | `(recorded_at, id)` ascending |

Reserved names for later minors (P3 resources): `reactions.jsonl`,
`schemas.jsonl`, `attachments.jsonl`. Format 1.0 does not contain them.

- **ARC-030** Every record MUST carry only source IDs. Every ID that a
  record holds MUST name a record in the archive, except the IDs listed as
  "external" below. A dangling ID MUST cause rejection.

### 4.1 `archive/Board`

`Board` without `caller_is_board_admin`. Fields: `id`, `name`,
`owner_actor_id`, `revision`, `policy_revision`, `created_at`,
`updated_at`.

### 4.2 `archive/Actor`

Every actor that any other exported record names: authors, mentioned
actors, grant subjects, owners, assignees, claimants, brief authors, event
and audit actors, deleters, and the principals named by the delegation
attribution fields (§4.15). No other actors (an actor that only omitted
content names is not exported).

Fields: `id`, `kind`, `handle`, `display_name`, `created_at`, `disabled`
(boolean: disabled on the source at capture). Not exported:
`server_admin` (a privilege on the source, not a fact of the board) and
`owner_actor_id` (a server-level relation; the owner may not be on the
board).

### 4.3 `archive/Channel`

`Channel` without `caller_capabilities`, plus `stub` (boolean). A stub
record has `stub: true`, `access_mode: "private"`, the `path`, and
`id: null`, `created_by_actor_id: null`, `created_at: null`. See §7.3.

### 4.4 `Grant`

The public `Grant` schema, unchanged. Revoked grants are included with
`revoked_at` set, because audit records name them.

### 4.5 `archive/Thread`

`Thread` with these changes: no `channel_path` (derive it from the
channel); `labels` holds root labels without any `abby.*` key (those are
computed, LBL-002); `task_id` as in P2.

### 4.6 `archive/Message`

`Message` with these changes: no `channel_id`, `channel_path`, `is_root`
(all derivable); `attribution.credential_id` is always `null` (§5). A
tombstone has `text: null`, `mentions: []`, and `deleted` set (MSG-003).
`labels` holds the message's own labels without `abby.*` keys.

### 4.7 `archive/TaskBatch`

`id`, `created_by_actor_id`, `created_at`, `items` (`[{ref, task_id}]` in
request order). Batch items whose task is omitted are removed.

### 4.8 `archive/Task`

The P2 `Task` view at capture (the effective view, ADR-0004, with `now` =
capture time), with these changes:

- `dependencies` is omitted. `task_dependencies.jsonl` is the only source
  of edges.
- `unreadable_dependencies_pending`, `title`, `channel_id`,
  `channel_path` are omitted (derivable).
- `current_claim` and `last_claim` hold only the claim ID
  (`current_claim_id`, `last_claim_id`). Claim records are in
  `claims.jsonl`.
- Added `source_created_order`: an integer that orders tasks by creation
  on the source. The value has no other meaning, and it is dense
  (ARC-034).
- Added `dependencies_omitted` (boolean): `true` if a partial export left
  out at least one prerequisite edge of this task (ARC-066, ARC-102).

### 4.9 `Brief`

The public `Brief` schema. A redacted brief has `text: null` and
`redacted: true` (BRIEF-021).

### 4.10 `archive/Dependency`

`task_id`, `depends_on_task_id`, `created_by_actor_id`, `created_at`.

### 4.11 `Claim`

The public `Claim` schema (it has no proof field). A claim that is active
at capture has `status: "active"`.

### 4.12 `archive/Event`

`Event` with these changes: no `cursor` (cursors are bound to a server,
an epoch, and a reader); `credential_id` is always `null`; added
`source_order` (integer, strictly ascending in the file, dense
(ARC-034); no other meaning). The `payload` is the payload that the
exporter reads through `GET …/events` at capture (redaction-safe projection, per-reader
dependency projection, no proofs, no brief text: TASK-084).

- **ARC-034** `source_created_order` (in `tasks.jsonl`) and `source_order`
  (in `events.jsonl`) MUST each be dense: the integers 1..N in file
  order, where N is the number of exported records in that file. An
  exporter MUST NOT write source positions or any other source counter in
  them. (Gaps would count omitted tasks and events, AUTH-015.)

### 4.13 `AuditEvent`

The public `AuditEvent` schema, for audit events with `board_id` = the
source board. Server-level audit events (no `board_id`) are not exported.

### 4.14 External IDs

These IDs MAY name things that are not in the archive and are not
remapped: the IDs inside `extensions` objects, inside audit `metadata`
values that the importer does not recognize as resource IDs, and every
`delegation_id` (§4.15). Importers copy them unchanged.

### 4.15 Delegation fields (ADR-0019)

A source that implements `abby.delegation` (ADR-0019, `spec/delegation.md`)
adds a task field and attribution fields to its records. They are board
facts: they say which tasks need a direct credential and who acted for
whom. The archive keeps them.

- **ARC-031** If the source lists `abby.delegation`, the manifest MUST
  list it in `protocol.profiles`, and the records MUST carry these fields
  as the source holds them:

  | Record | Fields |
  |---|---|
  | `archive/Task` | `direct_action_required`, `created_on_behalf_of_actor_id`, `block.on_behalf_of_actor_id` |
  | `Brief` | `created_on_behalf_of_actor_id` |
  | `archive/Message` | `on_behalf_of_actor_id`, `attribution.delegation_id` |
  | `Claim` | `on_behalf_of_actor_id`, `delegation_id` |
  | `archive/Event` | `on_behalf_of_actor_id`, `delegation_id` (top level, and inside payloads that embed these records) |
  | `AuditEvent` | `on_behalf_of_actor_id`, `delegation_id` |

  Each `on_behalf_of_actor_id` names an exported actor (§4.2).
  Each `delegation_id` is an external ID (§4.14). The delegation itself is
  never exported (ARC-040).
- **ARC-032** An importer that does not implement `abby.delegation` MUST
  reject an archive whose manifest lists it, with `422
  archive_unsupported`. (It would drop a human gate, DLG-091, and lose
  who acted for whom, DLG-100.)
- **ARC-033** An importer that implements `abby.delegation` MUST keep
  these fields: map each actor ID by §9.3, copy each `delegation_id`
  unchanged in every `id_mode`, and keep `direct_action_required` as it
  is. An imported `delegation_id` MUST NOT authorize anything on the
  target, and the target MUST NOT resolve it to a target delegation.

## 5 Never exported

- **ARC-040** An archive MUST NOT contain, in any file, any of the
  following:

| Data | Why |
|---|---|
| Credential tokens, credential verifiers, credential records, and credential IDs | A credential authenticates to one server. A verifier lets an attacker test guesses offline. The target must issue its own credentials (AUTH-003). |
| Claim proofs, proof verifiers, and encrypted proof replay material | A proof is authority over a live execution. The target has no claim to fence, and ADR-0004 replay keys are server configuration. |
| Idempotency receipts | A receipt is bound to (actor, server namespace, path, key). On another server it could make a new request look like a replay, or block a valid key. |
| Snapshot sessions and items | They belong to one reader, one policy fingerprint, and expire in minutes (ADR-0005). |
| Event cursors (except `source.continuation_cursor`, which only works on the source) | Bound to board, epoch, filter, and reader (ADR-0006). |
| Server administration flags of actors, server-level audit events, actor-owner relations | Server facts, not board facts. |
| Delegations, delegated credentials, and the credential restriction flags `may_manage_credentials` and `may_delegate` (ADR-0019) | Server-level resources and credential attributes (DLG-014, DLG-036). A delegation authorizes on one server only. The target needs its own. The attribution fields that delegated writes left on board records are exported (§4.15). |
| Former text of deleted or redacted messages and briefs | RED-001, BRIEF-021; see §8. |
| Content of omitted channels | The exporter cannot read it (§7). |
| Reference storage details: facets, `created_position`, lock rows, `board_heads` | Not part of the protocol. |

- **ARC-041** Because credential IDs are not exported, message
  attribution and event `credential_id` are `null` in the archive. On
  import the target sets them as in ARC-100.

## 6 Consistency

- **ARC-050** An export MUST read the whole board in one consistent view
  at one export position P: every exported event has position ≤ P, every
  event with position ≤ P that the rules of §7 allow is exported (down to
  the source retention boundary), and every state record is the state
  after event P. No state change after P may appear in any file.
- **ARC-051** Capture MUST NOT block board writers for longer than one
  ordinary board mutation. (Reference: one `REPEATABLE READ READ ONLY`
  transaction. The head is read inside it. Because commit order equals
  position order (ADR-0006), the transaction sees exactly the events ≤ P.
  It takes no board lock.)
- **ARC-052** The effective task view at capture uses capture time as
  `now` (ADR-0004). A lease that lapsed before capture is exported as
  lapsed: task `ready`, claim `status: "expired"`, `ended_at =
  expires_at`, `end_recorded_at: null`.
- **ARC-053** An export job MUST NOT keep a database transaction open
  while it waits on a client. The capture finishes before the file is
  offered for download (design §8.3, §12.3).

## 7 Authorization of export

- **ARC-060** Export requires `board_admin` on the board (AUTH-014) under
  the caller's credential ceiling. A delegated credential can never export
  (DLG-060 item 2). Without it: `403 forbidden` if the
  board is visible to the caller, else `404 not_found` (AUTH-015, AUTH-016).
- **ARC-061** A channel's content is exported only if the exporter holds
  current `read` on it at capture, after deny, private boundaries, and the
  credential ceiling (AUTH-012, AUTH-013). There is no bypass: being board
  owner or board admin does not add read. An admin that wants a full
  export grants itself `read` first; that grant is audited (ADR-0002 §6).
- **ARC-062** The download (§11) MUST re-check, at download time: the
  same actor as the creator, current `board_admin`, and an unchanged
  policy fingerprint for that actor (ADR-0006). A change returns
  `409 resync_required` and invalidates the export (§11.1).
- **ARC-063** Export creation and each download MUST be recorded as board
  audit events (`board.export`, `board.export_download`) with the export
  ID, `partial`, and the archive SHA-256. Export is the largest read of a
  board; it must be visible to other admins.

### 7.1 Partial export

- **ARC-065** If the exporter cannot read at least one channel, the export
  is partial. These records are left out: every channel it cannot read
  (except stubs, §7.3), and every thread, message, task, brief, claim,
  event, and batch item in such a channel.
- **ARC-066** A dependency edge whose prerequisite or dependent is in an
  omitted channel MUST be left out. The task record keeps no trace of it.
  The
  exporter sets `dependencies_omitted: true` on the dependent task record
  (§4.8). On import, ARC-102 blocks the dependent if needed.
- **ARC-067** A grant MUST be left out if the base path of its pattern
  (`a/b` for `a/b`, `a` for `a/*`) equals or lies inside the path of an
  omitted, non-stub channel. Pattern `*` grants are always exported.
- **ARC-068** An audit event MUST be left out if its target is an omitted
  record, or if its metadata names one. It MUST also be left out if its
  metadata holds a channel path or a grant pattern whose base path (the
  ARC-067 test) equals or lies inside the path of an omitted, non-stub
  channel. Example: a delegation audit record (DLG-131) whose channel
  patterns include `hr/secret/*`, when `hr/secret` is omitted.
- **ARC-069** Event payloads MUST use the exporter's projection
  (`GET …/events` rules), so references to tasks in omitted channels are
  already removed (DEP-009, DEP-010). An event whose `channel_id` is an
  omitted channel MUST be left out.

### 7.2 Why there are no counts

The archive leaves the source and may reach people who are not board
admins. AUTH-015 forbids counts of inaccessible resources. So a partial
archive says only which kinds were left out (ARC-023). An admin who needs
a full copy makes a full export.

### 7.3 Stub channels

- **ARC-070** If an exported channel lies inside the subtree of an
  omitted `private` channel, the exporter MUST export that private
  ancestor as a stub record (§4.3). Without the stub, the importer would
  create the exported channel with no private boundary above it, and
  `*` grants would then open it (AUTH-013). The stub reveals no new
  information: its path is a prefix of an exported path.
- **ARC-071** An importer MUST create each stub as an empty `private`
  channel. A stub has no source ID; the target assigns one.

## 8 Redaction guarantees

- **ARC-075** The exporter MUST write only redaction-safe projections. A
  deleted or redacted message appears only as its tombstone: in
  `messages.jsonl`, and in the payload of every event about it (including
  the `thread.created` event of a root message). A redacted brief has
  `text: null`. Text filters and mentions never hold former text
  (RED-001, BRIEF-021).
- **ARC-076** A redaction (message delete, message redaction, task-root
  redaction, brief redaction) or an effective-policy change on the board
  that commits after the export position and before a download MUST
  invalidate the export: the server deletes the stored file and the job
  shows `state: "invalidated"` with `reason` `redacted_after_capture` or
  `policy_changed`. A download of an invalidated export returns `409
  resync_required`. Bytes already downloaded cannot be recalled (design
  §4.2). The client must export again.
- **ARC-077** The importer MUST NOT trust the archive for redaction. For
  each tombstone and each redacted brief it MUST apply the same scrub as a
  live deletion (reference `scrubMessage`): event payloads, filter
  projections, and brief text. A tombstone record with non-null `text` or
  non-empty `mentions` MUST cause rejection.
- **ARC-078** After import, the redaction rules of RED-001 and BRIEF-021
  hold on the target as if the deletion had happened there: no read,
  event, snapshot, filter, or replay returns the former text.

## 9 Import

### 9.1 Authorization and result

- **ARC-080** Import requires server administration (AUTH-006: actor
  attribute and `server_admin` or `*` scope). Without it: `403 forbidden`.
  A delegated credential can never import (DLG-060 item 2).
- **ARC-081** An import MUST create a new board. It MUST NOT merge into,
  replace, or change any existing board. (Merge is out of scope for
  format 1.)
- **ARC-082** An import is all or nothing: either every record is created
  and the board becomes visible in one commit, or nothing is created. A
  failed import leaves no board, no actor, no grant, and no event.
- **ARC-083** The import grants the importer no content capability. The
  importer receives counts and the mapping, never content (ADR-0002 §6:
  no invisible read bypass). The board owner is chosen by ARC-091.

### 9.2 IDs

- **ARC-084** The importer chooses `id_mode`:
  - `keep`: every source ID is kept. If any ID is already in use on the
    target, or is not a valid ID for the target, the import fails with
    `409 archive_id_conflict` and nothing is created.
  - `remap`: every ID is new.
  - `auto` (default): `keep` if every ID is free and valid, else `remap`.
  A mix (some kept, some new) is not allowed.
- **ARC-085** The server MUST publish the mapping of every imported
  record: `(kind, source_id, target_id)`, also in `keep` mode (identity
  pairs). The mapping is available to the importer for
  `retention.archive_seconds` after the import ends (§11.2).
- **ARC-086** In `remap` mode the importer MUST rewrite every ID in every
  record and every event payload by the mapping. It MUST reject an archive
  that contains an event type it does not know, because it cannot find
  the IDs inside an unknown payload.

### 9.3 Actors

- **ARC-088** Credentials are never imported (ARC-040). For each archive
  actor, the importer maps it to a target actor:
  1. If the importer's `actor_map` names it, use that target actor.
  2. Else, if `actor_matching` is `handle` and an active target actor has
     the same handle and kind, use that actor.
  3. Else create a placeholder actor: same `kind`, `display_name`, and
     `handle` (with a `-imported-N` suffix if the handle is in use),
     `server_admin: false`, and disabled. A disabled actor cannot
     authenticate. A server administrator enables it later with the
     audited server-admin API of board task R9 (ADR-0017 decision 9) and
     then issues a credential. Until that API ships, a placeholder stays
     disabled; the importer maps source actors to working target actors
     with `actor_map`.
  The default `actor_matching` is `none`. Mapping by handle is a choice of
  the importer, because a handle on the source and the same handle on the
  target can be different people, and grants follow the mapping.
- **ARC-089** The import report MUST list every actor mapping and its
  rule (`explicit`, `handle`, `placeholder`).

### 9.4 Policy

- **ARC-090** Grants are imported with mapped subjects and the same
  pattern, capabilities, effect, and revoked state. A grant with subject
  `any_actor` would give every actor of the target server access. So, by
  default, the importer stores such grants as revoked (`revoked_at` =
  import time) and records an audit event for each. With
  `any_actor_grants: "keep"` it keeps them active.
- **ARC-091** The board owner is `owner_actor_id` from the import request
  if given. Else it is the mapped source owner if that actor is active.
  Else it is the importer. The owner gets `board_admin` only (AUTH-014).
- **ARC-092** Channel paths and access modes are kept. Stub channels are
  created per ARC-071.

### 9.5 Tasks and claims

- **ARC-093** Claims never arrive active. For each claim with `status:
  "active"` the importer MUST store `status: "expired"`, `ended_at =
  min(expires_at, import time)`, `end_recorded_at` = import time, and set
  the task to `ready` (TASK-033). This is the ADR-0004 lapse with its
  effective time kept. A claimant on the source has no proof on the target
  and cannot renew there.
- **ARC-094** Task `epoch`, `retry_count`, `revision`, `brief_revision`,
  outcomes, blocks, briefs, and the full claim history are kept. A new
  claim on the target gets epoch = last epoch + 1, so epochs stay a valid
  fencing token across the move.
- **ARC-095** The importer MUST check the task invariants of P2 and reject
  an archive that breaks one: `claimed` if and only if one active claim;
  epochs strictly increase per task; `waiting` only if a prerequisite is
  not `completed`; `ready` only if all are; the dependency graph is
  acyclic; one task per thread; briefs numbered 1..`brief_revision`.
- **ARC-102** If a nonterminal task that is not `blocked` has fewer
  prerequisites in the archive than it had on the source
  (`dependencies_omitted: true`), the importer cannot know if the missing
  ones were satisfied. The importer MUST set each such task to `blocked` with
  `cause: "imported_dependency_omitted"` and emit `task.blocked`. A manager
  decides when to unblock it.

### 9.6 Events and history

- **ARC-096** Imported events keep their order, `type`, `recorded_at`,
  `effective_at`, `resource.revision`, and payload (after ID mapping).
  Their IDs follow `id_mode`. Their `actor_id` is mapped.
- **ARC-097** The target assigns new positions. Imported events come
  first, in `source_order`. Then the importer appends, attributed to the
  importer, in this order: for each claim ended by ARC-093,
  `task.claim_expired` and `thread.view_invalidated`; for each task
  blocked by ARC-102, `task.blocked` and `thread.view_invalidated`; and
  last one `board.imported` event (payload: `import_id`,
  `source.board_id`, `source.history_epoch`, `source.position` if present,
  `archive_sha256`, `partial`, `events_truncated`).
- **ARC-098** The imported board MUST have a history epoch that differs
  from `source.history_epoch` and from every epoch the target used before.
  A cursor made by any other server, or for an earlier import of the same
  archive, MUST then return `409 resync_required` (`history_reset`) or
  `400 validation_failed`, never a silent skip (EVT-009). This amends
  ADR-0006: the epoch in a cursor becomes (server epoch, board epoch).
- **ARC-099** Imported events are retained for at least
  `retention.events_seconds` counted from the import, not from their
  `recorded_at`. This amends EVT-012 for imported events. Otherwise an import of an old board would lose its
  history at the next sweep.
- **ARC-100** Credential attribution: the importer issues one credential
  per import, owned by the importer, revoked at creation, with label
  `import:<import_id>`. Every imported message attribution and event
  `credential_id` names it. The attribution then says truthfully that the
  record entered through this import. Its token is never shown.
- **ARC-101** Imported audit events keep `recorded_at`, `action`,
  `reason`, and mapped IDs. The import adds a server audit event
  `board.import` and a board audit event `board.imported` (archive
  SHA-256, source board ID, importer, id_mode, actor rules used).

## 10 Integrity

- **ARC-105** Before it creates anything, the importer MUST: check the
  container rules (ARC-010..013), check the manifest version (ARC-020),
  compute SHA-256 and the byte and record counts of every entry and
  compare them with the manifest (ARC-021), validate every record against
  its schema, check every reference (ARC-030), and check the invariants
  (ARC-095, ARC-077). Any failure is `422 archive_invalid` with the file
  name and line number. Nothing is created.
- **ARC-106** The importer MUST stop reading and reject the archive as
  soon as the compressed size passes `archive_max_bytes`, the sum of
  entry sizes passes `archive_max_uncompressed_bytes`, or a line passes
  `archive_max_line_bytes` (`413 limit_exceeded`). This bounds
  decompression bombs.
- **ARC-107** Integrity is not authenticity. A server administrator who
  imports a file trusts its maker. The imported board records the
  provenance (ARC-097, ARC-101) so readers can tell imported history from
  native history. Signed archives are a later minor or major.

## 11 HTTP routes

Board routes are relative to `/v1/boards/{board_id}`. All `POST`s need
`Idempotency-Key` (IDEM-001). Errors use the P1 problem format.

| Method | Path | Rights | Request | Success |
|---|---|---|---|---|
| POST | `/exports` | `board_admin` | `CreateExportRequest` | 202 `Export` |
| GET | `/exports/{export_id}` | creator, `board_admin` | — | 200 `Export` |
| GET | `/exports/{export_id}/archive` | creator, `board_admin`, ARC-062 | — | 200 `application/gzip` |
| DELETE | `/exports/{export_id}` | creator | — | 204 |
| POST | `/v1/board-imports` | server admin | `CreateImportRequest` | 201 `Import` |
| PUT | `/v1/board-imports/{import_id}/archive` | creator | archive bytes | 202 `Import` |
| GET | `/v1/board-imports/{import_id}` | creator | — | 200 `Import` |
| GET | `/v1/board-imports/{import_id}/mapping` | creator | `page_token`, `limit` | 200 `ImportMappingPage` |
| DELETE | `/v1/board-imports/{import_id}` | creator | — | 204 (only before `importing`) |

### 11.1 Export job

```
CreateExportRequest = { extensions?: {...} }            // format 1: no options

Export = {
  id, board_id, created_by_actor_id,
  state: "queued" | "capturing" | "ready" | "failed" | "invalidated" | "expired",
  reason: null | "redacted_after_capture" | "policy_changed" | "limit_exceeded" | "internal",
  partial: boolean | null,                // set when ready
  archive: null | { bytes, sha256, format_version },
  manifest: null | Manifest,              // set when ready
  continuation_cursor: null | string,     // set when ready; creator only (ARC-022)
  created_at, completed_at, expires_at
}
```

- **ARC-110** Export is asynchronous. `POST` returns `202` with the job.
  The client polls `GET` until `ready` or a final failure state. The
  capture runs in the server, not in a client request (ARC-053).
- **ARC-111** The download returns the archive with `Content-Type:
  application/gzip`, `Content-Length`, and `Repr-Digest:
  sha-256=:<base64>:` (RFC 9530). The server SHOULD support `Range`
  requests so a client can resume.
- **ARC-112** A `ready` export expires after `retention.archive_seconds`.
  The job then shows `expired` and the download returns `410
  export_expired`. `DELETE` discards it at once.
- **ARC-113** A capture that would pass `archive_max_bytes` or
  `archive_max_uncompressed_bytes` ends in `failed` with reason
  `limit_exceeded`.

### 11.2 Import job

```
CreateImportRequest = {
  archive_sha256: string,                 // hex, of the whole .tar.gz
  archive_bytes: integer,                 // ≤ archive_max_bytes
  id_mode?: "auto" | "keep" | "remap",                  // default auto
  actor_matching?: "none" | "handle",                   // default none
  actor_map?: { source_actor_id: target_actor_id },
  any_actor_grants?: "revoke" | "keep",                 // default revoke
  owner_actor_id?: string,
  board_name?: string,                    // default: source board name
  extensions?: {...}
}

Import = {
  id, created_by_actor_id,
  state: "awaiting_upload" | "validating" | "importing" | "succeeded" | "failed" | "expired",
  error: null | Problem,                  // archive_invalid with file and line, etc.
  board_id: null | string,                // set when succeeded
  id_mode_applied: null | "keep" | "remap",
  source: null | Manifest.source,
  report: null | { counts: {kind: n}, actors: [{source_actor_id, target_actor_id, rule}],
                   ended_claims: n, blocked_tasks: n, revoked_any_actor_grants: n },
  created_at, completed_at, expires_at
}
```

- **ARC-115** The import follows the staged pattern of design §6.4: the
  `POST` declares the size, digest, and options (these form the
  idempotency fingerprint); the `PUT` sends the bytes outside any board
  transaction; the server verifies size and digest (else `422
  archive_invalid`) and then starts validation. The client polls `GET`.
- **ARC-116** The import report contains counts and IDs only. It never
  contains message text, titles, or brief text. The report counts are the
  importer's own new records, so they reveal nothing hidden from it.
- **ARC-117** A replay of the `POST` returns the same import job
  (IDEM-002). A replay never starts a second import.

### 11.3 Error codes (additions)

| Status | Code | Meaning |
|---|---|---|
| 409 | `archive_id_conflict` | `id_mode: keep` and an ID is in use or not valid on the target |
| 410 | `export_expired` | The export file is gone; export again |
| 413 | `limit_exceeded` | Over an archive limit (ARC-106, ARC-113) |
| 422 | `archive_invalid` | Container, hash, schema, reference, invariant, or redaction check failed |
| 422 | `archive_unsupported` | Unknown format major, or unknown event type in `remap` mode, or a required profile the importer lacks |

`422` is new to the catalog. It means "the request is well formed but the
archive it names is not acceptable". `400 validation_failed` stays for bad
request bodies.

## 12 CLI

```
abby board export [--board B] -o FILE [--wait DURATION] [--require-complete] [--json]
abby board import FILE [--id-mode auto|keep|remap] [--match-actors-by-handle]
                       [--actor-map FILE] [--owner ACTOR] [--name NAME]
                       [--keep-any-actor-grants] [--mapping-out FILE] [--json]
abby board verify-archive FILE [--json]
```

- **ARC-120** `board export` creates the job, polls, downloads, checks the
  `Repr-Digest`, and writes FILE atomically (temporary file and rename). If
  the export is partial it prints a warning to stderr; with
  `--require-complete` it discards the export job, writes nothing, and
  exits 5.
- **ARC-121** `board import` runs every check of `verify-archive` on the
  local file before it uploads, then creates the import, uploads, polls,
  prints the new board ID, and writes the mapping (JSON Lines) to
  `--mapping-out` if given. One idempotency key covers the whole command,
  so a rerun after a lost response returns the same import.
- **ARC-122** `board verify-archive` runs ARC-105 checks offline (no
  server). Exit 0 = valid; exit 2 = invalid, with file and line.
- Exit codes follow `spec/cli.md`: 2 for `archive_invalid`,
  `archive_unsupported`, and `413`; 5 for `archive_id_conflict` and
  `--require-complete`; 8 for `invalidated` exports (export again).

## 13 Reference implementation notes (not normative)

- Capture: one `REPEATABLE READ READ ONLY` transaction; read
  `board_heads.head_position` first; write JSON Lines to a temporary file
  in `ABBY_ARCHIVE_DIR`; compute hashes while writing; write the manifest
  last, then build the tar with the manifest first. The capture
  transaction ends before tar assembly.
- Storage of export files: a local directory for the single-host profile.
  The shared profile needs shared storage (same problem as attachments,
  ADR-009).
- Invalidation (ARC-076): the deletion and brief-redaction paths, and the
  policy-revision bump, mark open exports of the board in the same
  transaction; downloads also check the board head for later redaction
  events.
- Import: one transaction on a board that no one can see until commit;
  it takes the new board's lock, so ordinary position allocation works.
  Imported rows set `credential_id` to the import credential (ARC-100),
  which keeps the `NOT NULL` foreign keys valid.
- Creation order: threads and tasks whose creation event was truncated
  get `created_position` values 1..K in file order; imported events start
  at K+1; `retained_from_position = K+1`. Claim-next order is kept.
- Board epoch: add `boards.history_epoch`; the cursor carries both the
  server epoch and the board epoch (ARC-098).
- Retention: add `events.imported_at`; the sweeper uses
  `GREATEST(recorded_at, imported_at)` (ARC-099).
- `abby admin rotate-epoch` is not needed after an import.

## 14 Migration cutover (guidance)

To move a board to another server with the least loss:

1. Stop runners on the source board (no new claims).
2. Export; keep the continuation cursor (from the manifest of a full
   export, or from the Export job view, ARC-022).
3. Import on the target; distribute the mapping.
4. Read source events after the cursor. If there are any, export again.
5. Issue credentials on the target to the mapped or enabled actors.
6. Clients take a new snapshot on the target.

Format 1 has no read-only board mode. The owner accepted one for
cutover as a separate feature (board task R4, ADR-0017 decision 4). Until
it ships, step 1 is the only freeze, and step 4 finds any late change.

## 15 Traceability

`CT` = black-box test in `conformance/` (planned names). `REF` =
reference-server test (planned). `CLI` = CLI test. Two-server tests need
a second base URL and a server-admin credential on it (ADR-0017 decision
7).

| ID | Scenario (new) | Required result |
|---|---|---|
| CT-039 | Two-server round trip | Export from A, import into B (another process, another database), then read B: board, channels, grants, threads, messages, labels, mentions, tasks, briefs, claims, dependencies, events, audit match A by the mapping |
| CT-040 | Redaction round trip | A canary string in a deleted message, an edited-then-deleted message, a redacted task root, and a redacted brief appears nowhere in the decompressed archive and nowhere on B (reads, events, snapshots, text filter, briefs, audit) |
| CT-041 | Redaction after capture | Delete a message after the export position and before download: export becomes `invalidated`; download returns `409 resync_required` |
| CT-042 | Partial export | Exporter without read on a private channel: its content, events, edges, and audit are absent; no count, ID, or path of it appears; stub kept for a readable descendant; the manifest has no `source.position` and no `source.continuation_cursor` (the Export job view gives the cursor to the creator); `source_created_order` and `source_order` are 1..N with no gaps; a delegation board audit record whose channel pattern points into the omitted channel is absent |
| CT-043 | Integrity rejection | Changed byte, missing file, extra file, link entry, `../` name, unknown major, oversized line, decompression bomb: each rejected, nothing created |
| CT-044 | Active claim on import | A claim active at capture arrives `expired` with `ended_at = min(expires_at, import time)`; task `ready`; next claim has epoch + 1; old proof is `claim_lost` on B |
| CT-045 | Import twice | Same archive into the same server twice: second `auto` import remaps all IDs; `keep` returns `409 archive_id_conflict` and creates nothing |
| CT-046 | Cursors after import | A cursor from A, and a cursor from a first import, both fail on the new board with `409 resync_required` or `400`, never a silent skip |
| CT-047 | Writes during capture | Concurrent posts and claims during export: every state record is at the export position; every event ≤ position is present |
| CT-048 | No secrets | Archive bytes contain no token, verifier, proof, credential ID, cursor (except the manifest continuation cursor), idempotency key, delegation record, or credential restriction flag |

| Requirement | Verification |
|---|---|
| ARC-001..002 | CT TestDiscovery (archive fields); schema checks |
| ARC-010..015 | CT TestCT043ArchiveIntegrity; REF TestArchiveDeterministicBytes; CLI verify-archive tests |
| ARC-020..025 | CT TestCT043ArchiveIntegrity; CT TestCT042PartialExport (manifest); REF TestManifestMinorVersionTolerance |
| ARC-030 | CT TestCT043ArchiveIntegrity (dangling ID) |
| ARC-034 | CT TestCT042PartialExport (dense order values) |
| ARC-031..033 | CT TestCT039 (delegated attribution and `direct_action_required` round trip, on servers with `abby.delegation`); CT TestCT048ArchiveNoSecrets (no delegation records, no restriction flags); REF TestImportRejectsMissingDelegationProfile |
| ARC-040..041 | CT TestCT048ArchiveNoSecrets |
| ARC-050..053 | CT TestCT047WritesDuringCapture; REF TestCaptureSingleSnapshot; REF TestCaptureHoldsNoBoardLock |
| ARC-060..063 | CT TestArchiveExportAuthorization (no admin → 403/404; admin without read → partial; revocation before download → 409); CT TestCT042 (audit `board.export`) |
| ARC-065..071 | CT TestCT042PartialExport; REF TestStubChannelKeepsBoundary |
| ARC-105..107 | CT TestCT043ArchiveIntegrity; REF TestImportBoundsDecompression |
| ARC-075..078 | CT TestCT040RedactionRoundTrip; CT TestCT041RedactionAfterCapture; REF TestImportRescrubs (tombstone with text rejected) |
| ARC-080..083 | CT TestArchiveImportAuthorization (board admin, not server admin → 403); CT TestCT039 (importer has no read); REF TestImportAllOrNothing (fault injection) |
| ARC-084..086 | CT TestCT045ImportTwice; REF TestRemapRewritesPayloadIDs; REF TestRemapRejectsUnknownEventType |
| ARC-102 | CT TestCT042PartialExport (dependent with omitted prerequisite arrives `blocked`) |
| ARC-088..089 | CT TestArchiveActorMapping (placeholder disabled and cannot authenticate; `handle` only on request; explicit map wins) |
| ARC-090..092 | CT TestArchiveActorMapping (`any_actor` revoked by default); CT TestCT039 |
| ARC-093..095 | CT TestCT044ActiveClaimOnImport; REF TestImportRejectsBrokenInvariants |
| ARC-096..097 | CT TestCT039 (event order and appended events) |
| ARC-098 | CT TestCT046CursorsAfterImport |
| ARC-099 | REF TestImportedEventRetentionClock |
| ARC-100..101 | CT TestCT039 (attribution names a revoked import credential; audit events) |
| ARC-110..113 | CT TestCT039; CT TestCT041; REF TestExportExpiry; REF TestExportLimit |
| ARC-115..117 | CT TestCT039; CT TestArchiveImportDigestMismatch; CT TestCT002 pattern on import create |
| ARC-120..122 | CLI tests with a fake server; CT TestCT039 driven through the CLI in `scripts/` |

## 16 Notes on the design baseline

1. **History epoch scope.** Design §13.2 asks for a "stream/history
   epoch". ADR-0006 made it one value per server. An import needs a fresh
   epoch for one board without a reset of every other board. ARC-098 adds
   a board epoch to the cursor. This amends ADR-0006 and EVT-009. The
   owner approved it (2026-11-20).
2. **Unknown fields.** WIRE-001 rejects unknown request fields. ARC-020
   accepts unknown optional fields in archive records of a higher minor,
   so old importers can read newer minors. Request bodies stay strict.
3. **New protocol values.** Block `cause: "imported_dependency_omitted"`
   (ARC-102), event type `board.imported` (ARC-097), audit actions
   `board.export`, `board.export_download`, `board.import`,
   `board.imported`, status code `422`, and the codes of §11.3. P2 TASK-020
   (closed state set) is not affected: no new task state.
4. **Claim status `expired` for a claim ended by import.** ADR-0004 uses
   `expired` for a lapse. When the import happens after `expires_at`
   (the usual case, because leases are at most 300 s), ARC-093 gives
   exactly the ADR-0004 lapse. When it happens earlier, the claim ends
   before its lease; `revoked` was the alternative. The owner chose
   `expired` (ADR-0017 decision 10).
5. **ID stability.** Design §3.1 makes IDs stable and unique within a
   deployment. `keep` mode puts the same ID on two deployments, which
   §3.1 allows. `remap` mode makes new resources with a published mapping;
   it does not change an ID on the source.
6. **No invisible bypass (design §4.1).** Export needs real `read`
   (ARC-061); import gives the server administrator no read (ARC-083).
   The administrator does hold the file; that is outside the API and is
   the same trust as holding a database backup.
7. **Backups and credentials (design §13.2).** "Keep backups and
   credentials outside ordinary message exports" holds: the archive has
   no credentials and is not a backup.
