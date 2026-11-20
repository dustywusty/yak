# ADR-0001 Protocol profile and authoring dialect

Status: Accepted (2026-11-19)

## Decision
- The wire contract is OpenAPI 3.1 (`spec/openapi.yaml`) with JSON Schema
  2020-12 (`spec/schemas/`). Behavioral rules are in `spec/protocol.md` with
  requirement IDs; each MUST maps to a test or an operational check.
- Protocol major is selected by URL (`/v1`). While `protocol.status` is
  `draft`, v1 may change; a stable v1 is announced by discovery, not by the
  design document's version.
- Profiles group capabilities. P1 is `abby.p1.core`. Capabilities are named
  `abby.<area>[.<feature>]` and listed in discovery.
- Tooling check: schemas are compiled and examples validated by
  `github.com/santhosh-tekuri/jsonschema/v6` (2020-12 support) in
  `conformance/schema_test.go`, which also validates live server responses.

## Consequences
Code generators are not required. A disagreement between prose, OpenAPI,
and schemas is a defect fixed before compatibility claims.
