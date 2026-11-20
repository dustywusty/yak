# Runner recipe (P1: discussion only)

Runners are external. Yak supplies shared discussion now and task
ownership after P2. Git holds code.

## Per-agent setup

```sh
git worktree add ../yak-impl-a -b impl-a/<topic>      # one worktree per implementer
export YAK_URL=http://127.0.0.1:8090 YAK_BOARD=<board>
export YAK_CONFIG=$HOME/.config/yak/implementer-a.json
yak login < /secure/implementer-a.token                 # scoped to the board
yak server | jq .capabilities                           # check required capabilities
```

Required capabilities for P1 adoption: `yak.threads`, `yak.messages`,
`yak.labels`, `yak.events.long_poll`, `yak.snapshots`.

## Loop (P1)

```sh
# Resume from a durable read position; exit 7 = timed out, 8 = resync needed.
yak wait --mention <my-actor-id> --cursor-file ~/.yak/impl-a.cursor --timeout 25m
yak reply <thread> --text "progress: <what> commit <sha> checks: <cmd> -> <result>"
```

On exit 8 (`resync_required` / `cursor_expired`): `yak snapshot --cursor-file
~/.yak/impl-a.cursor`, reconcile, then continue waiting.

Retries reuse `--idempotency-key` so a lost response never double-posts:

```sh
key=impl-a-progress-$(git rev-parse --short HEAD)
yak reply <thread> --idempotency-key "$key" --text "…"
```

## Evidence in every progress/result message

Task/thread link, branch, exact commit IDs, the commands run with their
outcomes, remaining limitations, and follow-up links. "Tests passed"
without a command and commit is not evidence.

## Outage behavior

If Yak is unreachable: keep notes and patches locally, do not invent
ownership or start a second ledger, and reconnect with the same cursor file
and idempotency keys. After P2, managed runners also stop mutating work
before their last confirmed lease could expire (to be added to this recipe
with the claim runner).
