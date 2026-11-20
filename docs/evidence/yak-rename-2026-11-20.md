# Yak rename checks, 20 November 2026

Branch: `rename/yak`, based on `origin/main` at `883f9f7`.
The PR and board thread record the exact commit checked.

| Check | Command or setup | Outcome |
|---|---|---|
| Formatting | `gofmt -l cmd/yak internal conformance sdk examples` | No files listed |
| Whitespace | `git diff --check` | Passed |
| Static checks | `go vet ./...` | Passed |
| Unit tests | `go test -count=1 ./...` | Passed; database and HTTP suites run separately below |
| Build | `go build ./...` | Passed |
| Race and database tests | `YAK_TEST_DATABASE_URL=postgres://abby:devtest@127.0.0.1:55432/yak_rename_ref_final_a?sslmode=disable go test -race -count=1 -v ./internal/...` | Passed, no skips |
| Conformance | `YAK_CONFORMANCE_URL=http://127.0.0.1:18090 YAK_CONFORMANCE_ADMIN_TOKEN_FILE=/tmp/yak-rename-a-admin.token go test ./conformance/ -count=1 -v -timeout 900s` | Passed, no skips |
| Container | `DOCKER_CONTEXT=desktop-linux docker build -t yak:rename-a .` | Passed |
| Compose | `docker compose ... config --quiet` with legacy settings; parsed config with both families | Passed; new password and port settings won |
| Shell syntax | `bash -n scripts/p1-gate.sh scripts/p2-gate.sh scripts/smoke.sh examples/build-yak/seed.sh` | Passed |
| Documentation | Relative links in changed Markdown files | All targets present |

All databases and servers above were isolated from coordination. The tests
used the existing development Postgres container on port 55432. No tests ran
against the coordination server on port 8090.

The in-place upgrade used a third isolated database, `yak_rename_upgrade_a`,
and port 18091. The existing `/tmp/abby` binary migrated and bootstrapped it,
then created a board, grant, task and event cursor. The candidate binary
started against that database using only `ABBY_DATABASE_URL`. Checks passed
for the old bearer token, unchanged historical event payloads and cursors,
old label filters, new discovery names, current `yak.task.state` labels and
continuation into new `yak.*` task events. Migration reported no pending work.

Regression tests cover both token prefixes and revocation, every CLI/server
environment alias, new-token precedence over a legacy token file, default
config fallback, runner metadata aliases and proof isolation, both reserved
label prefixes, historical event facets and stored server labels.

Historical evidence files and applied migrations remain unchanged. The
physical database, migration lock and SQL function retain their old names.
Actor handles and the existing imported roadmap ID also stay unchanged.

The live upgrade, board rename and GitHub repository rename remain operational
steps for the coordinator after independent review. This evidence does not
claim those operations happened. New repository URLs become available when
the repository is renamed; external documentation URLs were not checked.

## Follow-up: predecessor Compose environment

Independent review found that the renamed Compose file supplied only
`YAK_DATABASE_URL`. The real P1 image `abby:coord-f7bd448` requires
`ABBY_DATABASE_URL`. Running the upgrade gate reproduced migration exit 1;
the container log said `abby: ABBY_DATABASE_URL is required`.

Compose now gives both images equivalent database, listen and role settings.
A new `python3 scripts/check-compose.py` CI step checks legacy-only settings,
current-only settings and new-setting precedence. It verifies the actual
rendered environment for both the migration and API services.

The actual gate command is:

```sh
DOCKER_CONTEXT=desktop-linux P1_IMAGE=abby:coord-f7bd448 \
  WORK=/tmp/yak-predecessor-gate-final-a PORT=18383 scripts/p2-gate.sh
```

The predecessor image digest is
`sha256:14ce165cabdfca311177c9062e61ab8d74add4c401fccd5802c30aa43cde6431`.
It advertises `abby.p1.core`; the candidate advertises `yak.p1.core` and
`yak.p2.tasks`. The gate creates predecessor data, applies migrations 0002
and 0003, checks message count and old-cursor continuation, runs conformance,
then verifies worker takeover after expiry and rejection of a stale proof.
The interruption drill now kills only its recorded child process.

Raw reproduction logs are in `/tmp/yak-predecessor-repro-a/`; final gate logs
are in `/tmp/yak-predecessor-gate-final-a/`. Exact checked head, final command
exit status and CI receipt are recorded in the fix task and PR comments.
These checks use their own Compose project, port and volume. They do not
operate the coordination service or complete the parent rename task.
