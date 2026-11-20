# ADR-0002 Grant precedence, private boundaries, and administration

Status: Accepted (2026-11-19). Implements design §4.1.

## Decision
1. Default deny. A grant = subject (`actor` | `any_actor`) + pattern +
   capabilities + effect. Patterns: `*`, exact `a/b`, descendants `a/*`.
2. For each capability check on path P: any matching **deny wins**.
3. **Private boundary:** let B be the deepest `private` channel whose subtree
   contains P. An allow counts only if its pattern is not `*` and its base
   path is within subtree(B). Nested private channels need their own allow.
4. Effective rights = grants ∩ credential ceiling (`scopes`, `board_ids`).
5. `board_admin` is a role: owner OR an allow grant (pattern `*` only), and
   no deny of `board_admin`, and the credential scope includes it.
   Ownership never overrides a deny and grants no content capability.
6. Creating actors and boards requires server administration in P1. Board
   admins create grants for themselves when they need content access; that
   action is audited. There is no invisible administrator read bypass.
7. Server recovery: the bootstrap credential is a server administrator,
   but in P1 it has no API to change grants on a board it does not
   administer. Recovery therefore needs the operator path in "Open issue".

## Example (tested in internal/authz)
Board-wide `any_actor` allow `read,post` on `*`; `eng/security` private;
Alice has allow `read,post` on `eng/security`. Bob is denied inside the
boundary without any deny rule. Alice's read-only credential cannot post.

## Open issue
Server administrators currently have no API to change grants on a board
they do not administer. Recovery today = operator SQL. A documented
`POST /v1/boards/{id}/recovery-grants` (server_admin, audited, reason
required) is planned before P2 (tracked on the yak-dev board).

## Consequences
Private channel creation bumps the board policy revision; cursors and
snapshots of affected actors require resync (ADR-0006).
