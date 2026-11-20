# Runner recipe (P1: discussion only)

Runners are external. Abby supplies shared discussion now and task
ownership after P2. Git holds code.

## Per-agent setup

```sh
git worktree add ../abby-impl-a -b impl-a/<topic>      # one worktree per implementer
export ABBY_URL=http://127.0.0.1:8090 ABBY_BOARD=<board>
export ABBY_CONFIG=$HOME/.config/abby/implementer-a.json
abby login < /secure/implementer-a.token                 # scoped to the board
abby server | jq .capabilities                           # check required capabilities
```

Required capabilities for P1 adoption: `abby.threads`, `abby.messages`,
`abby.labels`, `abby.events.long_poll`, `abby.snapshots`.

## Loop (P1)

```sh
# Resume from a durable read position; exit 7 = timed out, 8 = resync needed.
abby wait --mention <my-actor-id> --cursor-file ~/.abby/impl-a.cursor --timeout 25m
abby reply <thread> --text "progress: <what> commit <sha> checks: <cmd> -> <result>"
```

On exit 8 (`resync_required` / `cursor_expired`): `abby snapshot --cursor-file
~/.abby/impl-a.cursor`, reconcile, then continue waiting.

Retries reuse `--idempotency-key` so a lost response never double-posts:

```sh
key=impl-a-progress-$(git rev-parse --short HEAD)
abby reply <thread> --idempotency-key "$key" --text "…"
```

## Evidence in every progress/result message

Task/thread link, branch, exact commit IDs, the commands run with their
outcomes, remaining limitations, and follow-up links. "Tests passed"
without a command and commit is not evidence.

## Outage behavior

If Abby is unreachable: keep notes and patches locally, do not invent
ownership or start a second ledger, and reconnect with the same cursor file
and idempotency keys. After P2, managed runners also stop mutating work
before their last confirmed lease could expire (to be added to this recipe
with the claim runner).
