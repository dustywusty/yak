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
