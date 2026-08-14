# Contract v1

Contract v1 is the local JSON boundary shared by the Go daemon, CLI, MCP clients, and Swift app.

Rules:

- `schemaVersion` is the integer `1`.
- IDs are opaque strings. Ticket, branch, and path data are display fields.
- timestamps are RFC 3339 strings;
- revisions are non-negative signed 64-bit integers and increase monotonically within their scope;
- desired lifecycle, observed lifecycle, and health are separate axes;
- public snapshots never contain credentials, environment values, full commands, PIDs, or process arguments;
- mutations are asynchronous operations with idempotency keys and optional expected environment revisions;
- environment-scoped MCP results include a complete capped context footer at a revision.

`fixtures/status.json` is the canonical cross-language decoding fixture. Additive fields must not break clients. Contract changes are coordinator-owned and require the Go fixture test plus the Swift conformance executable. This machine currently has Command Line Tools rather than full Xcode, so its Swift toolchain ships without XCTest; the verifier deliberately has no external test dependency.
