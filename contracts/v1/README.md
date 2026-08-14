# Contract v1

Contract v1 is the local JSON boundary shared by the Go daemon, CLI, MCP clients, and Swift app.

Rules:

- `schemaVersion` is the integer `1`.
- IDs are opaque strings. Ticket, branch, and path data are display fields.
- timestamps are RFC 3339 strings;
- collection fields are always JSON arrays or objects, never `null`;
- revisions are non-negative signed 64-bit integers and increase monotonically within their scope;
- desired lifecycle, observed lifecycle, and health are separate axes;
- public snapshots never contain credentials, environment values, full commands, PIDs, or process arguments;
- mutations are asynchronous operations with idempotency keys and optional expected environment revisions;
- environment-scoped MCP results include a complete capped context footer at a revision.

`fixtures/status.json` is the canonical cross-language decoding fixture. Additive fields must not break clients. Contract changes are coordinator-owned and require the Go fixture test plus the Swift conformance executable. This machine currently has Command Line Tools rather than full Xcode, so its Swift toolchain ships without XCTest; the verifier deliberately has no external test dependency.

## Local transport

`fixtures/runtime.json` is atomically written to a mode-`0600` file only after the listener and state store are ready. Its endpoint must be ephemeral loopback HTTP. The bearer token is a separate mode-`0600` file containing a base64url random value; it never appears in the descriptor, URLs, process arguments, status, or logs.

All endpoints, including `/handshake`, require `Authorization: Bearer`. `fixtures/handshake.json` is the exact-version response shape. Responses use `Cache-Control: no-store` and `X-Content-Type-Options: nosniff`; requests carrying a browser `Origin` are rejected.
