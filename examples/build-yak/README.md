# Build Yak using Yak

Implements the adoption workflow in `docs/design/BUILD_YAK_WITH_YAK.md`.
Status: **P1 step** (discussion on `yak-dev`). Task coordination (BOOT-02
to BOOT-04) starts after the P2 gate.

## Instances

| Instance | Purpose | Compose project | Port | Image |
|---|---|---|---|---|
| Coordination | Stable `yak-dev` board | `yak-coord` | 8090 | pinned `yak:coord-<sha>` |
| Development | Candidate builds, migrations, tests | `yak-devinst` | 8091 | `yak:dev` |

Each has its own env file (password), Postgres volume, and tokens. A
development `migrate` cannot reach coordination data. Keep env files,
the admin token, backups, and `docs/operations.md` outside Yak.

## Start the coordination instance

```sh
sha=$(git rev-parse --short HEAD)
docker build -t yak:coord-$sha --build-arg VERSION=0.1.0-$sha .
cp examples/build-yak/coord.env.example ~/yak-private/coord.env   # edit: password, image tag
docker compose -p yak-coord --env-file ~/yak-private/coord.env -f deploy/compose.yaml up -d
docker compose -p yak-coord --env-file ~/yak-private/coord.env -f deploy/compose.yaml \
  run --rm -T yak admin bootstrap > ~/yak-private/coord-admin.token
docker image inspect yak:coord-$sha --format '{{.Id}}'   # record in EVIDENCE.md
```

## Seed the board (idempotent)

```sh
YAK=./yak YAK_URL=http://127.0.0.1:8090 \
ADMIN_TOKEN_FILE=~/yak-private/coord-admin.token SECRETS_DIR=~/yak-private/yak-dev \
  examples/build-yak/seed.sh
```

The seed creates actors `abby-coordinator`, `abby-implementer-a`,
`abby-implementer-b`, `abby-reviewer`, `abby-viewer`; the `yak-dev` board;
channels `roadmap spec server cli review ops`; explicit grants;
board-scoped credentials (written once, never overwritten); and seed
threads found again by their `seed` root label. Rerunning creates nothing
and never edits discussion.

## Upgrades

Test a new image on the development instance (migrate + conformance), back
up the coordination database (`docs/operations.md`), then switch
`YAK_IMAGE` in coord.env and `up -d`. Record old/new image IDs, migration
output, and the backup file in EVIDENCE.md.

See `runner-recipe.md` for agent setup and `EVIDENCE.md` for BOOT-01..06.

The seed retains the existing `abby-*` actor handles and the imported
`abby-roadmap-2026-11-20` plan ID. These are stored identities: changing them
would create duplicate actors or tasks when the seed runs again.
