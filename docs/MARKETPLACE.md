# Marketplace adapter

## Scope

Marketplace is the first and initially only repository adapter. Optimize deeply for it while keeping repository-specific behavior out of the core.

Initial defaults:

- primary checkout: `~/Developer/marketplace`
- linked worktrees root: `~/Developer/marketplace-worktrees`
- base branch: `origin/main`
- remote: the user's existing Marketplace origin

These are personal defaults, not hard-coded core assumptions.

## Existing prototype

The existing repository start script is executable documentation for the desired behavior:

- Turbo dry-run graph for affected services;
- manual service/frontend overrides;
- repository port-default lookup from `.env`;
- local URI overrides and remote development fallbacks;
- generated `.env.development.local` projection;
- ElasticMQ queue discovery from Compose and Serverless resources;
- native Turbo/Serverless/Vite processes;
- tmux presentation and shell-trap cleanup.

Switchyard may study or reproduce this logic independently. It must not edit, replace, wrap, or require the script. Colleagues remain unaffected.

## Local manifest

The adapter reads a personal schema-versioned `.switchyard.yaml` containing local runtime targets and service choices. Runtime discovery is Marketplace-first but the manifest shape stays repository-neutral:

```yaml
schemaVersion: 1
adapter: marketplace
display:
  name: marketplace
workspace:
  managedRoot: ~/Developer/marketplace-worktrees
  defaultBase: origin/main
runtime:
  defaultTarget: testing
  targets:
    - development
    - testing
    - demo
    - production
  warnOnStart:
    - demo
    - production
  services:
    - api
    - app
    - example-worker
```

The example is intentionally synthetic. A real local manifest lists the user's selected Marketplace runtime services. Every available service has a validated direct launch, leased loopback ports, readiness and health probes, routing aliases, and an owned teardown path. Discovery still derives availability from the executable catalog rather than assuming that a manifest entry is safe.

`workspace` is repository-neutral. It selects only where Switchyard-created worktrees live and the default Git base. Toolchain and hydration behavior comes from the selected adapter: Marketplace contributes `.nvmrc`, Yarn, and immutable install semantics; a future Go adapter can contribute Go/toolchain/module steps without adding Node concepts to the coordinator or manifest schema.

Existing Git worktrees are auto-discovered with `adopted` inventory ownership: Switchyard may prepare and run them but never removes them. Worktrees created through the app, CLI, or MCP are `managed`. The user may explicitly promote an existing checkout to `managed` through the app, `sy adopt-worktree`, or MCP; adoption requires a clean, pushed branch with an upstream, exact Marketplace Git identity, and a non-symlinked direct-child path under `workspace.managedRoot`. Managed creation writes durable local ownership intent before exact-argv `git worktree add`; archive refuses primary, adopted/foreign, dirty, unpushed, active, or unverifiable trees and never uses force.

From any Marketplace checkout or child directory, `sy status` resolves the exact containing Git worktree from the daemon inventory and shows only that context. `sy status --all` is the explicit cross-worktree inventory. Agents pass the physical absolute checkout path to `switchyard_context`, retrying a rejected logical host hint with read-only `pwd -P`; the shared MCP process directory and branch-name similarity are never used as identity.

Unavailable services expose their isolation reason in the app. The info popover can copy a worktree-aware agent prompt that describes the exact lifecycle, routing, ownership, readiness, and test work required before the service may be marked available.

The file is never committed. The app adds `/.switchyard.yaml` to the repository's shared local exclude at:

```text
~/Developer/marketplace/.git/info/exclude
```

Linked worktrees resolve the same common exclude file. Never touch Marketplace's public `.gitignore` for Switchyard.

The manifest is a local override and adapter configuration, not a generic extension framework. A future repository can use the generic schema and add a small adapter only where its conventions cannot be expressed declaratively.

## Runtime targets

Marketplace targets map to `DEPLOYMENT_ENVIRONMENT` and `NODE_ENV`; non-development targets use the workload build stage. `testing` is the local default. Targets listed under `warnOnStart` are protected: the app confirms every start, and CLI/MCP callers must send an exact per-request target acknowledgement. The daemon rejects missing or mismatched acknowledgements before creating an operation. The local Marketplace manifest protects both `demo` and `production` because local services may read from or write to those remote dependencies.

Targets are selected when a stopped environment starts. Switchyard does not rewrite a live process cluster in place: stop the environment, select another configured target, and start it again. The environment identity remains associated with the worktree, while each run durably records its selected target.

## Detection

The adapter should use stable mechanisms already present in Marketplace:

- `git merge-base` against the configured base;
- `turbo run ... --dry-run=json` for affected packages;
- `turbo ls` for known workspaces;
- root `.env` for allowlisted repository port defaults;
- package scripts and existing Serverless commands;
- Docker Compose and Serverless resources for local infrastructure declarations.

Auto-detection produces an inspectable plan. Explicit user selection and manifest overrides win.

Git change telemetry uses `origin/main`'s merge base through `HEAD` for committed work and `HEAD` through the working tree plus untracked text files for uncommitted work. Numstat paths rooted in `api`, `app`, `organizer`, or a known `services/*` runtime are attributed directly to that service. Root files and shared packages remain a separate shared total; Switchyard never duplicates those lines across every potentially affected service merely to force an attribution.

## Routing

Each environment receives a complete routing map:

- services selected for the environment route to that environment's leased local ports;
- unchanged dependencies use the selected target's configured endpoints;
- no local service may accidentally route into another worktree's environment;
- frontend `PORT` and `DEED_WEB_URI` semantics are preserved;
- environment projection is owned and reversible.

Native processes, port ownership, and health probes bind to literal `127.0.0.1`. Browser-facing App and Organizer URLs use `localhost` on the same leased ports because Marketplace's remote development, testing, and demo CORS policies explicitly trust local `localhost` origins but not arbitrary loopback-IP origins. API and service-to-service routes remain literal loopback addresses. Bind identity and browser identity must not be conflated.

Marketplace's Vite and dotenv stack preserves existing process environment values. Switchyard therefore applies environment values in a strict order: assigned local ports and routes, the primary checkout's ignored local overlay for the selected target, the linked worktree's selected target profile, then the development profile as a missing-value-only fallback for prerequisites that deployed targets receive outside dotenv. Target values are never replaced by development values. This lets every linked worktree reuse personal files such as `.env.testing.local` without copying them into the worktree. A content-hashed `.switchyard.env.cjs` preload references the primary checkout and performs the layering inside the Marketplace process, so secret values are neither copied into Switchyard state nor rendered into the projection. Switchyard never edits any `.env*.local` file.

Serverless configuration values that cannot be injected use content-hashed owned `.switchyard.serverless.ts` overlays in each selected service directory because Serverless requires its config to live under the working directory. The root environment preload, endpoint shims, and Serverless overlays apply and roll back together under one durable operation. The shared local exclude covers every projection; cleanup removes them only when their ownership markers and hashes still match.

Turbo is used for affected-service discovery and bounded prebuild work, not as the persistent service supervisor. Switchyard launches each workspace command directly under a dedicated process host so the logical service has a truthful ownership boundary.

## Infrastructure

Classify infrastructure explicitly:

- immutable image/build caches: shared;
- mutable queues, databases, caches, volumes, and Compose projects: isolated per environment by default;
- deliberately shared infrastructure: reference-counted repository lease with a namespacing proof.

ElasticMQ is one owned container per running environment, never global. Every selected SQS service gets its own leased loopback endpoint into that environment's broker, so services in the same worktree can exchange events while different worktrees cannot. Switchyard seeds the union of the selected services' standard and FIFO queues before service launch.

Slack also requires DynamoDB Local. Its Marketplace source fixes the local endpoint at `localhost:8000`, while Donation Batch fixes its local SQS endpoint at port `9324`. Switchyard owns content-hashed, process-scoped Node preload projections that rewrite only those declared local endpoints to each environment's assigned leases. The shims, dedicated infrastructure, and Serverless overlays are all local-only, positively owned, and removed through the same rollback contract; no tracked Marketplace source is changed.

Auth is the one Serverless service without an ordinary HTTP route: it exposes only the Serverless Offline Lambda listener. Switchyard still reserves the HTTP base port needed to derive its Lambda configuration, but readiness, health, and public URL projection use only the listener that actually exists.

All managed Docker resources receive ownership labels. Colima's current socket/context is discovered rather than assumed. Avoid host paths that the active Colima profile does not share.

## Union scenarios

A later Marketplace-specific scenario can materialize an integration worktree from source PR commits and main. It records provenance and becomes stale when any source or main moves.

Rules:

- fixes belong on the source branch/PR and are re-imported;
- no new work is authored directly on the union branch;
- the scenario can rebuild or alert but never silently rewrite source history;
- local and CI test results attach to the exact source commit set.
