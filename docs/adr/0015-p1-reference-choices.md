# ADR-0015 P1 reference-server and inventory choices

Status: Accepted (2026-11-19)

Reversible engineering choices recorded for review:
1. Go module path `github.com/abby-protocol/abby` is a placeholder until a
   public home is chosen.
2. Bootstrap is the operator command `abby admin bootstrap` against the
   database (no network bootstrap endpoint, no default secret).
3. Token format `abby.<credential_id>.<secret>`; 256-bit secret; SHA-256
   verifier (high-entropy secrets do not need a slow hash).
4. Inventory additions: `GET/POST /v1/actors`, `GET /v1/actors/{id}`,
   `GET /v1/audit-events` (server admin), `GET …/channels/{id}`,
   `GET …/grants/{id}`, `GET …/permissions?channel=` (PAR-004
   introspection). These are listed in OpenAPI.
5. Snapshot filters are query parameters (not a JSON body) so that the
   events filter and snapshot filter are literally the same.
6. Mentions resolve only to actors who can read the channel (ADR-0007,
   MEN-001), so mentions cannot enumerate hidden participants.
7. Board participants (`GET …/actors`) = actors who can read at least one
   channel the caller can read, plus the caller; admins see all grantees.
8. Thread title edits require root authorship or `channel_manage`.
9. Not yet implemented in P1: rate limiting (429), SSE, per-board metrics,
   readiness of workers. Discovery does not advertise them.
