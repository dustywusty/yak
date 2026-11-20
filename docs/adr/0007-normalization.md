# ADR-0007 Label, reaction, and handle normalization

Status: Accepted for labels and handles (2026-11-19); reactions in P3.

- Label keys: ASCII-lowercased by the server, then must match
  `[a-z0-9][a-z0-9._-]{0,63}`. `abby.` prefix reserved. Values: any valid
  UTF-8 up to 256 bytes, compared byte-exactly (no Unicode normalization).
- Handles: lowercased, `[a-z0-9][a-z0-9_-]{0,62}`, unique per deployment.
  In text, `@handle` is recognized when not preceded by `[A-Za-z0-9_@]`.
- Reactions (P3): proposal is NFC normalization plus a published alias
  table; decided with P3.
