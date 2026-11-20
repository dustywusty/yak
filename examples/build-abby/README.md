# Build Abby using Abby

Implements the adoption workflow in `docs/design/BUILD_ABBY_WITH_ABBY.md`.
Status: **P1 step** (discussion on `abby-dev`). Task coordination (BOOT-02
to BOOT-04) starts after the P2 gate.

## Instances

| Instance | Purpose | Compose project | Port | Image |
|---|---|---|---|---|
| Coordination | Stable `abby-dev` board | `abby-coord` | 8090 | pinned `abby:coord-<sha>` |
| Development | Candidate builds, migrations, tests | `abby-devinst` | 8091 | `abby:dev` |

Each has its own env file (password), Postgres volume, and tokens. A
development `migrate` cannot reach coordination data. Keep env files,
the admin token, backups, and `docs/operations.md` outside Abby.

## Start the coordination instance

```sh
sha=$(git rev-parse --short HEAD)
docker build -t abby:coord-$sha --build-arg VERSION=0.1.0-$sha .
cp examples/build-abby/coord.env.example ~/abby-private/coord.env   # edit: password, image tag
docker compose -p abby-coord --env-file ~/abby-private/coord.env -f deploy/compose.yaml up -d
docker compose -p abby-coord --env-file ~/abby-private/coord.env -f deploy/compose.yaml \
  run --rm -T abby admin bootstrap > ~/abby-private/coord-admin.token
docker image inspect abby:coord-$sha --format '{{.Id}}'   # record in EVIDENCE.md
```

## Seed the board (idempotent)

```sh
ABBY=./abby ABBY_URL=http://127.0.0.1:8090 \
ADMIN_TOKEN_FILE=~/abby-private/coord-admin.token SECRETS_DIR=~/abby-private/abby-dev \
  examples/build-abby/seed.sh
```

The seed creates actors `abby-coordinator`, `abby-implementer-a`,
`abby-implementer-b`, `abby-reviewer`, `abby-viewer`; the `abby-dev` board;
channels `roadmap spec server cli review ops`; explicit grants;
board-scoped credentials (written once, never overwritten); and seed
threads found again by their `seed` root label. Rerunning creates nothing
and never edits discussion.

## Upgrades

Test a new image on the development instance (migrate + conformance), back
up the coordination database (`docs/operations.md`), then switch
`ABBY_IMAGE` in coord.env and `up -d`. Record old/new image IDs, migration
output, and the backup file in EVIDENCE.md.

See `runner-recipe.md` for agent setup and `EVIDENCE.md` for BOOT-01..06.
