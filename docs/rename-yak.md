# Upgrade from Abby to Yak

The rename changes the module to `github.com/dustywusty/yak`, the command to
`yak`, and the Go SDK package to `sdk/go/yak`. Rebuild clients before upgrading
the server. Headers use `Yak-Run-Id` and `Yak-Claim-Proof`. Discovery names and
problem URNs use `yak`. Old headers are outside the transition guarantee.

The server still accepts `abby.` bearer tokens. CLI and server settings accept
`ABBY_*` aliases; nonempty `YAK_*` values win. See [CLI settings](../spec/cli.md)
for credential precedence. Compose accepts the old deployment settings too.
Reissue credentials through the API to obtain new `yak.` tokens, then revoke
the old credentials after clients switch. Changing a token prefix alone does
not rotate its secret or revoke the old spelling.

No data migration is needed. Keep the existing Compose project name and
`pgdata` volume. The physical Postgres role, database, SQL function and
migration lock keep their existing identities for upgrade compatibility.
Historical evidence files, event labels, actor handles, event positions,
history epochs and cursors retain their original values. New task events and
current task-state views use `yak.*` labels. Old filtered cursors must keep
their original filters; changing a filter requires a new cursor.

The coordinator performs these operations after independent review approves
an exact PR head. The implementation PR does not perform them.

1. Merge the approved head with a merge commit, then fetch `origin/main`.
2. Stop or finish old runners before the server switch. Rebuild the CLI as
   `/tmp/yak` from the merged commit and update runner commands and headers.
3. Capture the board task count, an event cursor and its filter. Back up the
   coordination database to its existing backups directory.
4. Build a pinned image from a clean detached checkout of merged `origin/main`.
   Keep the Compose project `abby-coord`, its env file and its volume.
5. Set `YAK_IMAGE` in the env file to the new image. Run the new image's
   `migrate`, stop the old `abby` service, then start `yak` with
   `up -d --remove-orphans`. Do not use `down -v`.
6. Check `/readyz`, discovery, the task count, old-token authentication and
   continuation from the saved cursor with its original filter.
7. Rename the board through `PATCH /v1/boards/{id}` with its current ETag and
   `{"name":"yak-dev"}`. Preserve its ID and all actors.
8. Rename the repository with `gh repo rename yak --repo dustywusty/abby --yes`.
   Update remotes to `https://github.com/dustywusty/yak.git` as appropriate.
9. Post exact commits, image digest, backup path and verification outcomes on
   the board. Complete the rename task only after these operations pass.

If validation fails, stop the candidate and restart the pinned previous image
using the previous Compose file and env settings. This rename adds no schema
migration. A backup restore is a separate recovery action: restore into a fresh
volume and rotate the history epoch as described in [operations](operations.md).
