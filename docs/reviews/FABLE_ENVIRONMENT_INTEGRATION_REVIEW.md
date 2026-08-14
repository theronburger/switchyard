# Fable environment integration review

Reviewed 2026-08-14 with `claude-fable-5` through the personal Fable profile. The read-only review cost `$7.349239` and inspected the coordinator, SQLite store, Marketplace adapter, process/container hosts, health primitives, and public daemon surfaces.

## Findings that changed the implementation

- Environment orchestration needed a real SQLite journal rather than test-only durability. The journal now persists private phases and rollback records, advances the public snapshot atomically, and gives boot a paged current-result seam.
- Port ownership needed exact restart restoration. The allocator now restores persisted leases without probing or falling back to a different port, and daemon startup refuses to serve if restoration is inconsistent.
- Docker plan freshness was incorrectly global. Unrelated foreign resource activity and writable-size changes could abort concurrent worktrees. Freshness is now scoped to the exact create collision or owned target being changed.
- ElasticMQ goals could not publish their assigned ports. Container goals now carry canonical literal-`127.0.0.1` TCP bindings through planning, inventory, reinspection, and exact Docker argv.
- Environment work could not inherit an HTTP request lifetime. The action service now returns an operation receipt immediately and runs accepted work under daemon lifetime, with bounded shutdown cleanup.
- A second environment could overwrite another environment's Serverless projection. Marketplace projections now carry environment ownership and refuse cross-environment replacement.
- LaunchAgent's minimal environment made implicit `node`, Yarn, Docker, `HOME`, `PATH`, and `TMPDIR` assumptions unsafe. Runtime registrations now resolve absolute executables and inject a controlled baseline environment.
- Finite Marketplace preparation commands were being discarded. A dedicated exact-argv, bounded preparation phase now runs before persistent services; persistent services remain direct process-host launches and never run through Turbo.

## Already resolved independently before the review returned

- The authenticated start/stop HTTP boundary and shared mutation contract.
- Environment-scoped container names and Switchyard ownership labels.
- Strict local-only Serverless projection materialization.

## Follow-up safety work

- The narrow fork-before-ownership-record crash window now has a durable pre-launch intent and report-only orphan evidence. An intent alone never authorizes a signal.
- Live reconciliation and health refresh continue after daemon restart rather than relying only on the last terminal snapshot.
- A transient terminal publication failure can still cause conservative teardown of a verified-healthy environment; retrying publication first remains a follow-up.
- Cleanup remains bounded and restart-idempotent, but scaling the budget from the concrete plan remains a follow-up.

The raw model response is intentionally not committed. This file records the actionable engineering conclusions and their disposition.
