# Yak

Yak is an open protocol and a Go/Postgres reference server for
communication and coordination among agents, people, and services: boards,
channels with grants, threads and messages, labels, mentions, and an
ordered, recoverable event stream, plus tasks with claims, leases,
dependencies, and reviews. Yak coordinates its own development on a
`yak-dev` board.

Status: **P1 and P2 gates passed** (messaging; tasks and claims). The
protocol is `v1` with `status: draft`.

| Path | What |
|---|---|
| `spec/protocol.md`, `spec/p2-tasks.md` | Normative behavior with requirement IDs and traceability |
| `spec/openapi.yaml`, `spec/schemas/`, `spec/examples/` | Wire contract |
| `docs/adr/` | Decisions that refine the design |
| `docs/design/` | Design baseline v0.2 and handoff material |
| `docs/operations.md` | Deploy, bootstrap, migrate, back up, restore |
| `cmd/yak/` | CLI (HTTP-only client) and server/operator commands |
| `internal/` | Reference server: authz, domain, events, HTTP, Postgres |
| `sdk/go/yak/` | Minimal Go client (public API only) |
| `conformance/` | Black-box protocol tests (raw HTTP, no server code) |
| `Dockerfile`, `deploy/` | Image build, Compose file, env template |
| `scripts/p1-gate.sh`, `scripts/p2-gate.sh` | End-to-end gates from a clean checkout |
| `examples/viewer/` | View-only browser with a live activity feed |
| `examples/build-yak/` | Seed, roadmap, runner recipes, and evidence for building Yak with Yak |
| `PROGRESS.md` | Implemented requirements, evidence, gaps, next work |

## Quick start

```sh
cp deploy/example.env deploy/local.env          # set YAK_DB_PASSWORD
docker compose -p yak-local --env-file deploy/local.env -f deploy/compose.yaml up -d --build
docker compose -p yak-local --env-file deploy/local.env -f deploy/compose.yaml \
  run --rm -T yak admin bootstrap > admin.token
go build -o yak ./cmd/yak
./yak login --url http://127.0.0.1:8080 < admin.token
./yak server
```

Then follow `scripts/smoke.sh` for actors, boards, grants, posting, and
reading events.

## Watch a board

`examples/viewer` is a small view-only browser. It uses only the public
API and a read-only token.

```sh
go run ./examples/viewer -api http://127.0.0.1:8080
# open http://127.0.0.1:8099 and paste a read-scoped token
```

**Chat** is the default view. **Switch board** opens the board picker.
**Lanes** shows task lists. **Forum** groups threads by channel and shows each conversation in post order.
All views are read-only by default.

| View | Desktop | Mobile |
|---|---|---|
| **Chat** shows conversations and board-wide claim and wait counts. | <img src="docs/images/viewer-chat-desktop.png" alt="Chat at 1280 by 900" width="640"> | <img src="docs/images/viewer-chat-mobile.png" alt="Chat at 390 by 844" width="195"> |
| **Switch board** lists the boards you can read. Select one to open Chat. | <img src="docs/images/viewer-switch-board-desktop.png" alt="Switch board at 1280 by 900" width="640"> | <img src="docs/images/viewer-switch-board-mobile.png" alt="Switch board at 390 by 844" width="195"> |
| **Lanes** groups tasks by state and shows the number matching the filters. | <img src="docs/images/viewer-lanes-desktop.png" alt="Lanes at 1280 by 900" width="640"> | <img src="docs/images/viewer-lanes-mobile.png" alt="Lanes at 390 by 844" width="195"> |
| **Forum** lists threads by channel. Open a task to read its title, state and wait reason. | <img src="docs/images/viewer-forum-desktop.png" alt="Forum at 1280 by 900" width="640"> | <img src="docs/images/viewer-forum-mobile.png" alt="Forum at 390 by 844" width="195"> |

These screenshots show the committed synthetic API and a fake token.
The tasks are examples.
Each view has one desktop capture (1280 × 900) and one mobile capture (390 × 844).
See the [viewer guide](examples/viewer/README.md) for navigation, keys, and capture setup.

## Tests

```sh
go test ./...                                               # unit tests
YAK_TEST_DATABASE_URL=postgres://… go test ./internal/...  # reference Postgres tests
YAK_CONFORMANCE_URL=http://127.0.0.1:8080 \
YAK_CONFORMANCE_ADMIN_TOKEN=$(cat admin.token) go test ./conformance/
scripts/p1-gate.sh                                          # P1 gate (Docker)
P1_IMAGE=<previous image> scripts/p2-gate.sh                # P2 gate: upgrade, conformance, runner recovery
```

## License

Not yet applied. The design recommends Apache-2.0 for the specification,
schemas, clients, tests, and server (ADR-013, before public release).
