# Fable architecture review triage

Reviewed on 2026-08-14 with the personal Fable profile. The CLI reported canonical model `claude-fable-5`. The review was read-only and made no file changes.

## Accepted before Wave 1

- Launchd, not the GUI process, owns daemon lifetime. The app owns setup, approval, upgrade, and repair UX.
- Mutations are persisted asynchronous operations, serialized per environment, with idempotency keys and expected-revision checks.
- Environment IDs are opaque; ticket and branch names are display data.
- MCP environment context is a complete capped snapshot, never a diff that requires hidden client state.
- Managed children write to owned log files directly so the daemon can restart without losing their output.
- Process-group membership is revalidated before every destructive signal to defend against PID and group reuse.
- Docker ownership labels are present in create specifications rather than added after creation.
- SQLite is daemon-only, WAL-backed state. Clients never open it directly.
- Crash-mid-launch intent is journaled before side effects and reconciled after restart.
- Mutable infrastructure is isolated in v1; shared mutable reference counting is deferred until a real need exists.
- The Marketplace adapter must characterize direct service process trees rather than assuming a Turbo wrapper owns every descendant.

## Accepted with a different implementation

Fable preferred a Unix-domain socket. Switchyard uses authenticated ephemeral loopback HTTP because it gives Swift `URLSession`, SSE, Go, CLI, and MCP one proven transport. The security boundary is explicit: loopback-only binding, separate mode-`0600` endpoint and token files, bearer headers, constant-time comparison, hostile-origin rejection, and no-store responses.

## Deferred deliberately

- Developer ID signing and notarization must be settled before packaged distribution, not before the contract and daemon spine compile locally.
- Worktree create/remove and union materialization follow the existing-worktree runtime milestone.
- Generic shared-infrastructure proofs and compatibility negotiation are excluded from v1.

## Marketplace discoveries that resolved review risks

- Vite and dotenv behavior allow direct environment injection, so v1 does not edit or co-own personal `.env.development.local` files.
- Persistent services should not run through Turbo. Turbo remains useful for affected-service discovery and prebuilds; Switchyard launches workspace commands directly into owned groups.
- Existing unlabelled ElasticMQ containers are foreign and report-only. Per-environment ElasticMQ uses labelled resources and an owned generated Serverless overlay where required.
