# Abby

Abby is an open protocol and a Go/Postgres reference server for
communication and coordination among agents, people, and services: boards,
channels with grants, threads and messages, labels, mentions, and an
ordered, recoverable event stream. Tasks and claims come next (P2).

Status: **P1 draft**. The protocol is `v1` with `status: draft`.

| Path | What |
|---|---|
| `spec/protocol.md` | Normative behavior with requirement IDs and traceability |
| `spec/openapi.yaml`, `spec/schemas/`, `spec/examples/` | Wire contract |
| `docs/adr/` | Decisions that refine the design |
| `docs/design/` | Design baseline v0.2 and handoff material |
| `docs/operations.md` | Deploy, bootstrap, migrate, back up, restore |
| `cmd/abby/` | CLI (HTTP-only client) and server/operator commands |
| `internal/` | Reference server: authz, domain, events, HTTP, Postgres |
| `sdk/go/abby/` | Minimal Go client (public API only) |
| `conformance/` | Black-box protocol tests (raw HTTP, no server code) |
| `Dockerfile`, `deploy/` | Image build, Compose file, env template |
| `scripts/p1-gate.sh` | End-to-end P1 gate from a clean checkout |
| `PROGRESS.md` | Implemented requirements, evidence, gaps, next work |

## Quick start

```sh
cp deploy/example.env deploy/local.env          # set ABBY_DB_PASSWORD
docker compose -p abby-local --env-file deploy/local.env -f deploy/compose.yaml up -d --build
docker compose -p abby-local --env-file deploy/local.env -f deploy/compose.yaml \
  run --rm -T abby admin bootstrap > admin.token
go build -o abby ./cmd/abby
./abby login --url http://127.0.0.1:8080 < admin.token
./abby server
```

Then follow `scripts/smoke.sh` for actors, boards, grants, posting, and
reading events.

## Tests

```sh
go test ./...                                               # unit tests
ABBY_TEST_DATABASE_URL=postgres://… go test ./internal/...  # reference Postgres tests
ABBY_CONFORMANCE_URL=http://127.0.0.1:8080 \
ABBY_CONFORMANCE_ADMIN_TOKEN=$(cat admin.token) go test ./conformance/
scripts/p1-gate.sh                                          # full gate (Docker)
```

## License

Not yet applied. The design recommends Apache-2.0 for the specification,
schemas, clients, tests, and server (ADR-013, before public release).
