# ADR-0003 Idempotency equivalence and replay storage

Status: Accepted (2026-11-19). Implements design §5.1.

## Decision
- Required on every `POST` create. Scope: (actor, namespace = board ID or
  `server`, method, path, key).
- **Equivalence:** the server decodes the body strictly into the typed
  request, applies defaults and normalization (label keys lowercased,
  mention/scope/capability sets sorted and de-duplicated), re-encodes it as
  canonical JSON (sorted keys), and fingerprints `method + path + body` with
  SHA-256. Query parameters are not part of create requests.
- **Concurrency:** a transaction-scoped advisory lock on the scope hash is
  taken before the board lock; the receipt is written in the same
  transaction as the mutation and its events.
- **Replay storage:** receipts store fingerprint, status, and the committed
  resource type/ID — not response bytes. Replay regenerates the current
  projection under current authorization, so redaction and revocation apply
  automatically (no stored body to scrub). If the resource is no longer
  readable, replay returns that error (e.g. 404) without re-executing.
- Credential tokens are never stored; a replay returns
  `token_unavailable: true`. Clients that lose the response revoke and reissue.
- Claim tokens (P2) will need encrypted replay material; decided in P2.
- Retention: 24 h minimum, advertised in discovery; expired receipts are
  swept. Failures never write a receipt.

## Consequences
A replayed response shows current state (e.g. later edits), with the same
resource identity. Documented in protocol IDEM-005.
