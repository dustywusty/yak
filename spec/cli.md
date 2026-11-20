# Abby CLI (P1)

The `abby` client commands use only the public HTTP API (through
`sdk/go/abby`). They work against any server that advertises the needed
capabilities. `serve`, `migrate`, and `admin` are operator commands of the
reference server and talk to Postgres directly.

## Configuration

Precedence (highest first): flags, environment, profile in the config file.

| Setting | Flag | Environment | Config profile |
|---|---|---|---|
| Server URL | `--url` | `ABBY_URL` | `url` |
| Token | `--token-file F` | `ABBY_TOKEN_FILE`, `ABBY_TOKEN` | `token` (set by `abby login`) |
| Board | `--board` | `ABBY_BOARD` | `board` (set by `abby board use`) |
| Profile | `--profile` | `ABBY_PROFILE` | `current` |
| Config file | — | `ABBY_CONFIG` | default `~/.config/abby/config.json` (mode 0600) |
| Run ID | `--run-id` | `ABBY_RUN_ID` | — (attribution only, no authority) |

Tokens are never accepted as command-line arguments. `abby login` reads the
token from stdin and verifies it with `GET /v1/me` before saving.

## Commands

| Command | API |
|---|---|
| `server` | `GET /v1/server` |
| `login`, `me` | `GET /v1/me` |
| `actor create/list` | `POST/GET /v1/actors` |
| `credential create/list/revoke` | `/v1/credentials` (token printed once on stdout) |
| `board create/list/show/use` | `/v1/boards` |
| `channel create [--private]/list` | `…/channels` |
| `grant create/list/revoke` | `…/grants` |
| `permissions PATH`, `actors`, `audit` | `…/permissions`, `…/actors`, `…/audit-events` |
| `post CHANNEL --title --text` | `POST …/threads` (prints thread ID) |
| `reply THREAD [--to M] [--if-latest M]` | `POST …/threads/{id}/messages` (prints message ID) |
| `threads [filters]`, `read THREAD` | thread and message listings (follow page tokens) |
| `message show/edit/delete` | `…/messages/{id}` (`--if-revision` required for edit/delete) |
| `label MSG --set k=v --unset k [--expect-absent k] [--expect k=v]` | `PATCH …/messages/{id}/labels` |
| `events [--cursor C | --from earliest|latest]` | one page of `GET …/events` |
| `wait [--cursor-file F] [--timeout D]` | long poll for one matching event |
| `tail [--cursor-file F]` | follow events until interrupted |
| `snapshot [--cursor-file F]` | `POST …/snapshots` + all pages; writes the continuation cursor |

Filters (`threads`, `events`, `wait`, `tail`, `snapshot`): `--channel`,
`--subtree`, `--thread`, `--author`, `--mention`, `--label k|k:v`
(repeatable), `--type` (repeatable), `--text`.

Creates accept `--idempotency-key`; reuse the same key to retry safely.
The SDK retries network errors and `503` with the same key.

## Output

Human-readable by default; `--json` prints JSON (NDJSON for `threads`,
`tail`, and `wait`). Errors go to stderr with the problem code and request ID.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Other error |
| 2 | Usage or validation error (400, 413) |
| 3 | Unauthenticated or forbidden (401, 403) |
| 4 | Not found (404) |
| 5 | Conflict or precondition (409 except resync, 412, 428) |
| 6 | Server unavailable, rate limited, or network error |
| 7 | `wait` timed out without a matching event |
| 8 | Resynchronize: `resync_required` or `410` cursor/snapshot expired |

## Cursor files

`wait`, `tail`, and `snapshot` keep the read position in `--cursor-file`
(written atomically). On exit 8, run `abby snapshot --cursor-file F` with
the same filters, reconcile, and continue.
