# Abby viewer (view-only demo client)

This is a small, view-only browser for Abby boards. It is a demo client of
the public HTTP API. It was written only from the published contract:
`spec/openapi.yaml`, `spec/schemas/`, `spec/examples/`, and
`spec/protocol.md`. It does not use the Go server code or the SDK.

The page is plain HTML, CSS, and JavaScript (ES modules). There is no build
step and no external dependency. `main.go` is a small Go program (standard
library only). It serves the embedded page and reverse-proxies `/v1/*` to an
Abby server, so the page and the API have the same origin. The proxy passes
the `Authorization` header through. It does not read, add, or log it.

## Run

```sh
go run ./examples/viewer -api http://127.0.0.1:8090
```

Then open <http://127.0.0.1:8099/>. Flags:

| Flag | Default | Meaning |
|---|---|---|
| `-listen` | `127.0.0.1:8099` | Address of the viewer |
| `-api` | `http://127.0.0.1:8090` | Base URL of the Abby server |

## Get a read-only token

Make a credential with only the `read` scope, limited to one board:

```sh
abby credential create --actor <viewer actor> --scopes read --boards <board>
```

Paste the token into the page. The page keeps it in `sessionStorage` for
this tab only. Click **Forget token** to remove it. A `401` from any request
removes the token and asks for a new one.

## What it shows

- **Boards**: `GET /v1/boards`, and `GET /v1/me` for the current actor.
- **Channels**: `GET …/channels`, with the access mode (`private` badge) and
  the caller's capabilities on each channel.
- **Threads**: `GET …/threads` for all channels, one channel, or a subtree,
  with an optional label filter (`status:open priority`). The page follows
  `next_page_token`.
- **Thread**: `GET …/threads/{id}` and `GET …/threads/{id}/messages`. It
  shows author handles (from `GET …/actors`), labels as chips, mentions, an
  "edited" marker, reply links, and "message deleted" for tombstones.

All message text goes into the page as text nodes, never as HTML. Times show
in the local time zone. The tooltip shows the UTC value from the server.

The page is read-only. It sends only `GET`, plus `POST …/snapshots` and
`DELETE …/snapshots/{id}` (snapshot sessions are reads). The `api()` helper
refuses any other method.

## Protocol features it exercises

| Behavior | Requirements |
|---|---|
| Bearer token; `401` stops the live loop and asks for a token | AUTH-001, AUTH-004, EVT-011 |
| Read-scoped credential; only readable channels and threads appear | AUTH-005, AUTH-015, AUTH-020 |
| Snapshot on board open: `POST …/snapshots`, page with `GET …/snapshots/{id}`, keep `continuation_cursor`, `DELETE` the session | SNAP-001, SNAP-002, SNAP-003 |
| `413 limit_exceeded` on snapshot: follow from `from=latest` instead | SNAP-004, EVT-010 |
| Replay `GET …/events?cursor=…&wait_ms=25000` with the same (empty) filter as the snapshot | SNAP-002, EVT-005, EVT-009, EVT-011 |
| Apply `channel.created`, `thread.created`, `message.created`, `message.edited`, `message.deleted`, `message.labels_changed`, `thread.updated`, `thread.view_invalidated` (refetch); ignore unknown types | EVT-004, EVT-006, EVT-007, WIRE-001 |
| Deduplicate events by `id` | EVT-003 |
| Always continue from `next_cursor`, also on `timed_out: true` | EVT-008, EVT-011 |
| `409 resync_required` or `410 cursor_expired` / `snapshot_expired`: banner, new snapshot, then replay | EVT-009, EVT-012, SNAP-003 |
| Network error: back off and retry with the same cursor | EVT-002 |
| Thread list filters (`channel`, `subtree`, `label`) | EVT-005, LBL-003 |
| Page tokens on threads and messages | PAGE-001 |
| Tombstones show "message deleted" | MSG-003, RED-001 |
| Problem documents (`code`, `reason`) drive error handling | WIRE-002 |

The status pill in the header shows **live**, **reconnecting**, or
**resyncing**.

## Design notes

- The snapshot and the event stream use no filter (the whole board). The
  thread list uses `GET …/threads` with the selected filter, and the page
  applies the same thread-level filter to live updates. A filtered event
  stream is bound to its filter (EVT-009), so a filter change would need a
  new snapshot. One board-wide stream avoids that.
- `channel.created` events carry `caller_capabilities: []`, so the page
  reloads the channel list to show capabilities.
