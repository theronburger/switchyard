# Parallel build plan

Current status: Waves 0 through 3 are complete. The live app, daemon, Marketplace runtime, CLI/MCP surfaces, workspace lifecycle, and golden two-worktree demonstration have passed their acceptance gates. Public distribution and signed update operations are tracked in `RELEASING.md`.

## Coordination rule

Parallelism begins after the coordinator versions a minimal contract and freezes directory ownership. Contract amendments remain cheap but coordinator-mediated: update fixtures and pass Go/Swift conformance tests before lanes consume the new revision. Lanes use separate worktrees and do not edit shared dependency, contract, generated, or manifest files without reassignment.

Codex has four collaboration slots including the coordinator. Fable runs externally through the personal Claude profile and does not consume a Codex slot.

## Wave 0: spine — complete

Coordinator owns:

- repository initialization and private remote creation;
- `AGENTS.md` and accepted decisions;
- top-level Go/Swift scaffolding;
- local API schema and fixture snapshots;
- shared dependency files;
- directory ownership declarations;
- integration branch and merge order.
- authenticated loopback transport and exact-version handshake.

Fable performs read-only architecture, lifecycle, and product critiques through the installed `claude-personal-fable` skill. Findings are triaged; Fable does not silently change accepted decisions.

Exit gate:

- a fixture can represent repositories, worktrees, environments, services, leases, health, alerts, and the MCP state footer;
- Go and Swift can both decode the fixture;
- invalid lifecycle transitions have named errors;
- all lane directories exist and are disjoint.

## Wave 1: implementation fan-out — complete

| Lane | Owner | Exclusive paths | Outcome |
| --- | --- | --- | --- |
| Coordination | root Codex | contracts, root manifests, integration tests, docs | Stable contract and integration |
| Daemon | Codex agent A | `internal/daemon`, `internal/state`, `internal/events`, `internal/runtime` | SQLite, daemon lifecycle, reconciliation, owned process supervision |
| Marketplace | Codex agent B | `internal/adapters/marketplace`, Marketplace fixtures | discovery, service plans, routing, infrastructure model |
| Agent surfaces | Codex agent C | `internal/mcp`, CLI command packages | MCP/CLI clients and state footer |
| Native app | Fable | `app/` | SwiftUI fixture-driven app, setup and status UX |

The coordinator may move the process supervisor or lease allocator into a separate lane if directory ownership remains exclusive.

Exit gate:

- each lane has focused tests;
- no lane requires a live Marketplace service for its unit tests;
- the app renders fixture states;
- daemon, CLI, and MCP communicate through the frozen contract;
- Marketplace detection runs read-only against the real checkout.

## Wave 2: integration fan-out — complete

Parallel tracks:

- daemon plus Marketplace adapter;
- Swift app plus real daemon;
- MCP and CLI plus real daemon;
- install/startup/Connection Doctor;
- fake-service end-to-end harness;
- Fable adversarial engineering review and screenshot review.

Exit gate:

- app launch ensures the daemon is running and compatible;
- both agent clients can be configured and validated from the app;
- service runs are grouped and stopped by ownership;
- crash, restart, conflict, and stale-state paths are visible;
- the safety suite proves foreign resources survive.

## Wave 3: Marketplace golden demonstration — complete

Run representative web and API services in two simultaneous worktrees.

Required scenarios:

- both request the same default ports;
- a foreign process already owns a candidate port;
- one service forks the normal Yarn/Turbo/Serverless/Vite descendant tree;
- app closes and reopens;
- daemon restarts with child services alive;
- Colima stops and returns;
- a service crash-loops;
- a worktree disappears outside Switchyard;
- cleanup is requested for dirty/unpushed/active worktrees;
- foreign Docker resources exist;
- disk scan exceeds its time budget.

The private acceptance capture was intentionally removed before publication; the durable safety assertions remain in the automated contract and lifecycle suites.

## Fable usage

The local `claude-personal-fable` skill is authoritative. The verified invocation is:

```bash
zsh -lic 'claude-personal --model fable --print --output-format json --no-session-persistence "PROMPT"'
```

The verified `canonicalModel` is `claude-fable-5`. Do not fall back silently.

High-value Fable work:

- architecture and concurrency critique;
- SwiftUI implementation against a frozen contract;
- visual review from screenshots;
- adversarial lifecycle, cleanup, and recovery review;
- end-to-end audit after integration.

Avoid giving Fable shared contract ownership or live destructive Marketplace actions.
