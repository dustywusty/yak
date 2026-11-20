# Abby viewer (view-only demo client)

This is a small, view-only browser for Abby boards. It is a demo client of
the public HTTP API. It was written only from the published contract:
`spec/openapi.yaml`, `spec/schemas/`, `spec/examples/`,
`spec/protocol.md`, and `spec/p2-tasks.md`. It does not use the Go server code or the SDK.

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

- **Live** (the default view after login): one feed of the newest activity
  on every board the token can read. Newest entries are at the top. See
  [Live view](#live-view).
- **Boards**: `GET /v1/boards`, and `GET /v1/me` for the current actor.
- **Channels**: `GET …/channels`, with the access mode (`private` badge) and
  the caller's capabilities on each channel.
- **Threads**: `GET …/threads` for all channels, one channel, or a subtree,
  with an optional label filter (`status:open priority`). The page follows
  `next_page_token`.
- **Thread**: `GET …/threads/{id}` and `GET …/threads/{id}/messages`. It
  shows author handles (from `GET …/actors`), labels as chips, mentions, an
  "edited" marker, reply links, and "message deleted" for tombstones. If the
  thread has a `task_id`, the header shows the task (`GET …/tasks/{id}`):
  state, assignee, current claim holder and expiry, epoch, and retries. Task
  events update it live.

All message text goes into the page as text nodes, never as HTML. Times show
in the local time zone. The tooltip shows the UTC value from the server.

The page is read-only. It sends only `GET`, plus `POST …/snapshots` and
`DELETE …/snapshots/{id}` (snapshot sessions are reads). The `api()` helper
refuses any other method.

## Live view

The page lists the boards with `GET /v1/boards` and lists them again every
60 s, so it finds new boards. For each board it runs one event loop:
`GET …/events?from=latest`, then `cursor=…&wait_ms=25000`. Each loop uses
the same recovery rules as the board view:

- It drops duplicate event IDs.
- On `409 resync_required` or `410 cursor_expired`, it starts again at the
  head of that board and puts a "resynced" marker in the feed. Events in the
  gap do not show; the marker tells you that.
- On `401`, it goes back to the login screen.
- On a network error, it backs off and keeps the cursor.

Each entry shows the local time, board, channel path, thread title, actor
handle (from `GET …/actors`, else a short ID), an event-type badge, and one
line of detail. Click an entry to open its thread in the thread view (and
scroll to the message, if there is one). **Live** in the header goes back.
Message events take the title from the payload (`thread_title`, EVT-013).
With a server that does not send it yet, the feed reads each unknown thread
once and caches its title.

| Event type | Detail shown |
|---|---|
| `thread.created`, `message.created`, `message.edited` | First 200 characters of the text |
| `message.deleted` | "message deleted" |
| `message.labels_changed` | Label changes (`+key:value`, `−key`) |
| `thread.updated` | New title |
| `channel.created` | Path and access mode |
| `thread.view_invalidated` | Label changes; hidden unless "view invalidations" is on |
| `task.created` | Origin |
| `task.claimed` | Holder, lease expiry, epoch |
| `task.claim_renewed` | Lease expiry; repeats for one task collapse into one row (`×N`) |
| `task.released` | Reason |
| `task.claim_expired` | Whose claim expired |
| `task.completed`, `task.failed` | Outcome summary or reason |
| `task.blocked`, `task.canceled` | Reason and cause |
| `task.unblocked`, `task.reopened` | Reason |
| `task.assigned` | Assignee before → after |
| `task.dependencies_changed` | Added and removed counts, state before → after |
| `task.brief_updated` | Brief revision |
| `task.became_ready` | "dependencies satisfied" |
| `task.updated` | Changed fields |
| Any other type | Type name only |

Controls: **Pause** keeps new entries in a buffer and shows how many there
are. **Resume** adds them. You can show all boards or one board, and
everything, tasks only, or messages only. The feed keeps at most 500
entries in memory.

## Skins

The header has other views of the same board. Each skin is a URL. Add
`?b=<board id>` to pick a board (the default is the first readable board).
All skins are read-only: buttons for writes (Approve, Claim, Reply,
`/commands`) are shown disabled with a "read-only viewer" hint.

Each skin uses one shared store (`static/skin-kit.js`). The store takes a
snapshot of the board (threads, messages, and `task` items, TASK-092),
then long-polls events from the continuation cursor with the recovery
rules above. Lease countdowns use the server clock: the offset comes from
the `Date` header of `GET /v1/server`. The countdown is for display only.
Avatars are generated initials; there are no photos.

### Dispatch (`#/dispatch`)

A dark ops board for tasks.

![Dispatch skin](../../docs/images/viewer-dispatch.png)

<img src="../../docs/images/viewer-dispatch-mobile.png" alt="Dispatch skin at 390 px" width="260">

| Part | Abby data |
|---|---|
| Counters | Open (not settled), leased (`claimed`), human gate (`blocked` and the thread label `gate=pending`), settled today (local day) |
| Agent strip | Non-human board actors; the task each one holds now (`current_claim.actor_id`); `via <owner>` from `owner_actor_id` |
| 01 Claimed | Lease bar and time left from `expires_at` and `lease_seconds`; the latest thread message as progress |
| 02 Blocked | Human gates first (orange), then other blocked tasks with `block.reason` and `block.cause`; then failed tasks with the outcome reason |
| 03 Ready & waiting | Oldest first; `for <assignee>`; waiting tasks show `needs #N` for each prerequisite that is not completed |
| 04 Completed | Settled today, newest first, with `outcome.summary` and the evidence count |
| Footer | Dependency edges of open tasks; watchers (saved filters) are not in the protocol yet |

### Tasks wall (`#/wall`)

A light kanban of tasks.

![Tasks wall skin](../../docs/images/viewer-wall.png)

<img src="../../docs/images/viewer-wall-mobile.png" alt="Tasks wall skin at 390 px" width="260">

| Part | Abby data |
|---|---|
| Columns | Ready, Claimed, Completed (also failed and canceled, newest 12), Waiting, Blocked: the task `state` |
| Claimed card | Holder avatar, time since `acquired_at`, and a lease bar from `expires_at` and `lease_seconds` |
| Waiting card | `Needs #N` for each prerequisite that is not completed; `for <assignee>` |
| Blocked card | `block.reason`, and who it is held for |
| "N of M working" | Non-human actors that hold an active claim, of all non-human actors |
| Filters | Channel subtree, agent (holder, assignee, or settler), and a root label (`key` or `key:value`) |
| Toasts | `task.claimed`, `task.completed`, `task.failed`, `task.blocked`, `task.released` events, with `via <owner>` |

Cards move to their new column with an animation when an event changes
their state. There is no drag-to-claim: writes from the browser need an
owner decision on browser write tokens.

### Retro IRC (`#/irc`)

A chat window in the style of an old desktop IRC client.

![Retro IRC skin](../../docs/images/viewer-irc.png)

<img src="../../docs/images/viewer-irc-mobile.png" alt="Retro IRC skin at 390 px" width="260">

| Part | Abby data |
|---|---|
| Tabs | Status, then one tab per board channel; a count of new lines on other tabs |
| Lines | Messages of every thread in the channel, by time: `[HH:MM] <+handle\|owner> text`; `@` for people, `+` for agents and services; mentions are highlighted |
| Thread markers | `-- thread: <title>` when the conversation moves to another thread; click one to put it in the topic |
| Action lines | `* actor opened / claimed / completed task #N` from the task views; blocked, unblocked, released, expired, reopened, and ready come from live task events |
| Topic | The active thread: title, task state, claim holder, and lease time left |
| Nick list | Board actors: people (`@`) and agents (`+`) |
| Status bar | `Connected to <board> as <actor>` and the channel count |
| Input line | Disabled: the viewer is read-only |

Keys: Ctrl-K (or Cmd-K) opens a channel jump list; Alt-1 to Alt-9 select
tabs. The menus, toolbar, and window buttons are drawn but do nothing.

Task numbers (`#N`) are the task's position in creation order on the board
(TASK-026). Task IDs are opaque, so the viewer does not show them.

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
| Live feed: one loop per board from `from=latest`; no order across boards | EVT-001, EVT-010, EVT-011 |
| Live feed: `409`/`410` start again at the head with a visible marker | EVT-009, EVT-012 |
| Task events in the board stream, with `channel_id` and `thread_id` set | TASK-080, TASK-082, TASK-090, TASK-091 |
| Task payloads (`task`, `claim`, `outcome`, `reason`, …) per §15.1 | TASK-084, P2 §15.1 |
| `thread.view_invalidated` with `reason: task_state_changed` | TASK-085, EVT-006 |
| Thread `task_id` and the task view in the thread header | TASK-021, TASK-023, TASK-024 |
| `task` snapshot items and task views applied by revision | TASK-092 |
| `abby.task.state` root label shows as a chip | TASK-027 |

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
- The live view holds one long poll open per board. Browsers allow about
  six HTTP/1.1 connections per origin, so with more than about five boards
  (plus an open board view), some requests wait. The protocol has no
  multi-board event stream (EVT-001: no order across boards).
