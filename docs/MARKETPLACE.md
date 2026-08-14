# Marketplace adapter

## Scope

Marketplace is the first and initially only repository adapter. Optimize deeply for it while keeping repository-specific behavior out of the core.

Initial defaults:

- primary checkout: `/Users/example/Developer/marketplace`
- linked worktrees root: `/Users/example/Developer/marketplace-worktrees`
- base branch: `origin/main`
- remote: `example/marketplace`

These are personal defaults, not hard-coded core assumptions.

## Existing prototype

`marketplace/scripts/start-changed.sh` is executable documentation for the desired behavior:

- Turbo dry-run graph for affected services;
- manual service/frontend overrides;
- `DEED_*_PORT` lookup from `.env`;
- local URI overrides and remote development fallbacks;
- generated `.env.development.local` projection;
- ElasticMQ queue discovery from Compose and Serverless resources;
- native Turbo/Serverless/Vite processes;
- tmux presentation and shell-trap cleanup.

Switchyard may study or reproduce this logic independently. It must not edit, replace, wrap, or require the script. Colleagues remain unaffected.

## Current local evidence

At discovery time:

- ten Marketplace worktrees occupied roughly 15.8 GB;
- one worktree occupied 5.2 GB, primarily `node_modules`, `app`, and `organizer` artifacts;
- organizer plus nonprofit-service appeared as roughly thirteen wrapper/runtime processes and about 2.4 GB aggregate RSS;
- Docker occupied roughly 466 MB with one ElasticMQ container;
- Colima was the active Docker runtime;
- a host `/tmp` bind mount failed because the Colima VM did not share that path.

These are observations, not fixed budgets. The app should roll process descendants into logical services and treat disk size as performance/reclaimability telemetry rather than an alarm by default.

## Local manifest

The adapter reads a personal schema-versioned `.switchyard.yaml` containing absolute paths, preferences, service overrides, health checks, and sharing choices.

The file is never committed. The app adds `/.switchyard.yaml` to the repository's shared local exclude at:

```text
/Users/example/Developer/marketplace/.git/info/exclude
```

Linked worktrees resolve the same common exclude file. Never touch Marketplace's public `.gitignore` for Switchyard.

The manifest is a local override and adapter configuration, not a generic extension framework. A future repository can use the generic schema and add a small adapter only where its conventions cannot be expressed declaratively.

## Detection

The adapter should use stable mechanisms already present in Marketplace:

- `git merge-base` against the configured base;
- `turbo run ... --dry-run=json` for affected packages;
- `turbo ls` for known workspaces;
- root `.env` for default `DEED_*_PORT` values;
- package scripts and existing Serverless commands;
- Docker Compose and Serverless resources for local infrastructure declarations.

Auto-detection produces an inspectable plan. Explicit user selection and manifest overrides win.

## Routing

Each environment receives a complete routing map:

- services selected for the environment route to that environment's leased local ports;
- unchanged services use Marketplace's configured development endpoints;
- no local service may accidentally route into another worktree's environment;
- frontend `PORT` and `DEED_WEB_URI` semantics are preserved;
- environment projection is owned and reversible.

Marketplace's Vite and dotenv stack preserves existing process environment values, so v1 injects leased ports and routing directly and never edits `.env.development.local`. Serverless configuration values that cannot be injected use a content-hashed owned `.switchyard.serverless.ts` overlay in the service directory because Serverless requires its config to live under the working directory. The shared local exclude covers this projection; cleanup removes it only when its ownership marker and hash still match.

Turbo is used for affected-service discovery and bounded prebuild work, not as the persistent service supervisor. Switchyard launches each workspace command directly under a dedicated process host so the logical service has a truthful ownership boundary.

## Infrastructure

Classify infrastructure explicitly:

- immutable image/build caches: shared;
- mutable queues, databases, caches, volumes, and Compose projects: isolated per environment by default;
- deliberately shared infrastructure: reference-counted repository lease with a namespacing proof.

ElasticMQ should not be globally shared merely because it is lightweight. Identical queue names can leak events between worktrees. Prefer a per-environment instance/port unless the queue namespace is proven isolated.

All managed Docker resources receive ownership labels. Colima's current socket/context is discovered rather than assumed. Avoid host paths that the active Colima profile does not share.

## Union scenarios

A later Marketplace-specific scenario can materialize an integration worktree from source PR commits and main. It records provenance and becomes stale when any source or main moves.

Rules:

- fixes belong on the source branch/PR and are re-imported;
- no new work is authored directly on the union branch;
- the scenario can rebuild or alert but never silently rewrite source history;
- local and CI test results attach to the exact source commit set.
