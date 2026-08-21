# Private repository profiles

Status: accepted architecture and implementation plan for the next breaking release.

## Outcome

Switchyard is a repository-neutral, machine-local control plane. Repository behavior is data in private configuration. The application, daemon, CLI, MCP server, documentation, fixtures, and tests contain no consuming-repository identity or catalog.

One installed daemon manages many repositories concurrently:

```text
private machine configuration
        |
        v
validated immutable configuration revision
        |
        +-- repository registry and generic Git discovery
        |
        +-- declarative workspace compiler
        |       `-- workspace coordinator
        |
        +-- declarative environment compiler
        |       `-- environment coordinator
        |
        +-- private artifact compiler
        |       `-- Application Support only
        |
        `-- cleanup and action compiler
                `-- revisioned plan/apply operations
```

The existing coordinators, journals, process ownership, port leasing, container ownership, health probing, authenticated API, CLI, MCP boundary, and Swift contract remain valuable. Repository-local configuration, built-in catalogs, repository-specific planners, and repository-resident projections do not.

## Non-negotiable invariants

1. The tracked Switchyard tree has no consuming-repository names, commands, service IDs, environment variables, package names, queue names, or special cases. A CI assertion enforces this after migration.
2. Switchyard-owned configuration, markers, projections, shims, logs, ownership records, and generated artifacts never appear in a checkout, worktree, public ignore file, or Git administrative directory.
3. An explicit `git worktree add` or `git worktree remove` operation may ask Git to update its own administrative data. Switchyard never edits that data directly.
4. Repository tools may create their normal declared outputs, such as dependency directories or compiler output, when an approved profile command runs. Those are tool outputs, not Switchyard configuration or Switchyard-owned artifacts, and are retained by default.
5. Consuming-repository logic never enters product code. Profiles compose supported generic commands, typed value sources, ports, probes, infrastructure, private artifacts, actions, and cleanup declarations. A genuine capability gap requires a repository-neutral primitive with independent tests, not a repository-specific branch.
6. Commands are executable plus argv. There is no implicit shell. Accepting a configuration revision authorizes its compiled executable behavior as one reviewed transaction. The activation preview identifies exact executables, shells, interpreters, and generated wrappers; fingerprint drift or a changed revision requires re-acceptance. Per-run confirmation remains limited to explicitly high-risk targets and actions.
7. The daemon remains the only runtime-state writer. Configuration mutations from the app or CLI use compare-and-swap through the daemon.
8. Every workspace or environment operation is pinned to an immutable accepted configuration payload and digest. Reloading configuration cannot change a running process cluster in place, including after a daemon restart.
9. Repository credentials remain in repository-managed local environment files and are loaded by the repository's normal tooling. Switchyard does not read, copy, map, persist, or log those values.
10. Cleanup is always inspectable plan then revision-checked apply. Foreign or unverifiable resources are report-only.

These rules deliberately supersede the earlier built-in-adapter and repository-local-manifest decisions.

## Private configuration

The desired configuration lives outside every repository:

```text
~/Library/Application Support/Switchyard/
├── configuration.yaml
├── configuration-revisions/
├── daemon/
│   └── state-v2.sqlite
├── runtime/
│   ├── repositories/<repository-key>/<worktree-id>/<run-id>/
│   │   ├── artifacts/
│   │   ├── logs/
│   │   └── ownership/
│   ├── actions/<repository-key>/<operation-id>/
│   ├── preparation-runs/<launch>/
│   └── managed-workspaces/
└── caches/<cache-key>/
```

The configuration directory is mode `0700`; desired and revision files are regular owner-only files with mode `0600`. Writes use a bounded temporary file, fsync, an atomic commit, and parent-directory fsync. A daemon write of the desired file is a compare-and-swap against the held inode, not just a digest check before writing: a new file is linked with `RENAME_EXCL` so a file that appears concurrently is never overwritten, and an existing file is replaced with `RENAME_SWAP` and re-verified afterwards (same inode at the path, same mode, owner, link count, size, mtime, and byte digest through the descriptor held since validation). If a manual editor changed or replaced the file inside that window, the exchange is undone and the editor's version stays in place; if the undo itself fails, the editor's version is preserved at the temporary path and named in the error. No version of the file is destroyed on any refusal path. Symlinks, aliases, duplicate mapping keys, YAML anchors, merge keys, custom tags, unknown fields, excessive nesting, and unsupported schema versions are rejected.

`configuration.yaml` is desired state. The canonical accepted payload persisted by the daemon is runtime authority. If the desired file is invalid or differs from the accepted head without completing validation, all new prepare, start, and profile-command mutations fail closed with `CONFIGURATION_INVALID`; reads, reconciliation, stop, and positively owned cleanup continue from pinned accepted revisions. A malformed attempt to disable a risky profile therefore cannot be silently ignored while new starts continue.

A disabled repository permits stop and cleanup of existing resources but no new preparation or start. A repository key is permanently bound to one physical Git identity. Repointing it is remove plus add and is rejected while any operation, result, artifact, cache ownership, approval, or stop-only resource references the old binding. Safe removal proceeds through disable, cleanup/archive, then remove.

Configuration, local API, database, and plan-compiler versions are independent.

### Revision and activation protocol

Every accepted configuration has a monotonic global revision, canonical digest, compiler version, source-file digest, canonical payload, and per-repository subtree digests. The accepted payload and every compiled recovery specification referenced by a live, incomplete, or stop-only resource remain available. A digest without its exact payload is insufficient.

An app or CLI mutation compares the expected accepted revision and desired-file digest, validates the complete candidate, durably records a pending mutation with both expected digests, writes the desired file atomically, then commits the canonical accepted revision and head and marks the mutation complete. On startup, the daemon resumes only a desired file that exactly matches its pending record. Any other desired/accepted mismatch is treated as a manual edit, revalidated, and kept pending owner acceptance; it never guesses which side won.

The app's **Add Repository**, **Edit**, **Enable**, **Disable**, and **Remove** actions are one generic daemon mutation on a repository entry (`POST /v1/configuration/repositories`). The daemon compares the accepted revision and the desired-file digest, edits only that entry in the owner's YAML node tree so comments and untouched sections survive, recompiles the complete document, writes it atomically as an owner-only file, and stages a candidate; the owner still accepts that candidate's exact digest. An existing key never changes root; disabling requires that no non-stopped environment still belongs to the repository (an accepted disable restarts the daemon without a registration for it); and removal requires an entry that is disabled in both the desired file and the accepted revision with no managed worktree or non-stopped environment still belonging to it. Symlinked, hard-linked, foreign-owned, group-readable, malformed, or concurrently edited desired files are refused with the original preserved.

Manual edits enter through the same validation and acceptance path. Retention keeps the head, explicit rollback revisions, and every revision referenced by a durable resource. Unreferenced revisions expire under a bounded policy.

Parsing and cross-reference validation are globally atomic, while activation diffs per-repository digests. Resolve/start acquires one immutable repository-profile snapshot before it persists an operation; reload cannot swap that snapshot between resolution and operation creation. Old pinned profiles coexist with the current profile until no durable resource references them. Global port-range changes are rejected or staged while incompatible active resources exist.

### Shape

```yaml
schemaVersion: 1

machine:
  ports:
    first: 30000
    last: 49999
  execution:
    inheritedEnvironment: []
    shellDefault: deny

repositories:
  aurora-console:
    enabled: true
    displayName: Aurora Console
    root: /Users/example/Developer/aurora-console

    git:
      remote: origin
      defaultBase: origin/main
      managedWorktreesRoot: /Users/example/Developer/aurora-console-worktrees

    values: {}
    toolchains: {}
    caches: {}
    preparation: {}
    targets: {}
    defaultTarget: local
    services: {}
    infrastructure: {}
    artifacts: {}
    actions: {}
    cleanup: {}
```

Repository keys and nested IDs are stable opaque identifiers. Display names may change. A repository key binds to the accepted physical common-Git-directory identity plus normalized remote identity, not a directory basename, configured label, or remote name alone.

### Generic references

The schema uses validated tagged unions instead of free-form interpolation:

- `ExecutableRef`: absolute executable, compile-time PATH lookup, or resolved toolchain.
- `PathRef`: read-only primary checkout path, read-only worktree path, working directory, private runtime path, or private cache path.
- `ValueRef`: literal, extracted non-secret metadata value, target value, leased port, URL assembled from a port, toolchain property, cache path, or private artifact path.
- `ValueSource`: bounded non-secret metadata from a text file, JSON pointer, or YAML scalar. Repository credentials are not value sources.
- `CommandSpec`: executable reference, argv, working directory, explicit environment, timeout, output cap, and declared effects.
- `ProbeSpec`: process, TCP, HTTP, or exact-command probe.
- `ArtifactSpec`: bounded private text or structured content with run, worktree, or repository lifetime.
- `ActionSpec`: lifecycle action or exact command with scope, presentation, risk, and confirmation policy.
- `CleanupSpec`: retention and eligibility for positively owned resources; never a free-form deletion glob.

Every reference is resolved and containment-checked before a mutation is accepted. Executables become absolute regular executable files during compilation, not child launch. Ambient environment inheritance is empty by default.

Target selectors, local endpoints, ports, and routes belong in private configuration. Repository credentials remain in the repository's existing local environment files and are loaded by its normal commands; Switchyard never enumerates or injects their keys. A profile may read bounded non-secret repository metadata such as a tracked version file or package-manager declaration, but Switchyard does not use an ignored checkout-local file as its personal configuration store and never copies private values into a worktree.

## Workspace preparation

Preparation is a generic recipe compiled into the existing `workspace.Plan`:

```yaml
values:
  runtime-version:
    source:
      kind: text-file
      root: worktree
      path: .nvmrc
      trim: true
      trimPrefix: v

  package-manager-entry:
    source:
      kind: yaml-scalar
      root: worktree
      path: .yarnrc.yml
      key: yarnPath

toolchains:
  javascript-runtime:
    requestedVersion: { value: runtime-version }
    resolver:
      kind: versioned-directory
      root: /Users/example/.nvm/versions/node
      directoryPrefix: v
      executablePath: bin/node
      match: requested-prefix
      select: highest
    provision:
      execution: shell
      executable: /bin/zsh
      arguments:
        - -c
        - '. "$1" --no-use && nvm install "$2"'
        - switchyard-toolchain
        - /Users/example/.nvm/nvm.sh
        - { value: runtime-version }
      timeout: 20m

preparation:
  fingerprint:
    files: [.nvmrc, .yarnrc.yml, yarn.lock, package.json]
    globs: [packages/*/package.json]
    toolchains: [javascript-runtime]
  steps:
    - id: install-dependencies
      command:
        executable: { toolchain: javascript-runtime }
        arguments:
          - { worktree-value-path: package-manager-entry }
          - install
          - --immutable
        workingDirectory: { worktree-path: . }
        environment:
          NODE_USE_SYSTEM_CA: { literal: "1" }
          YARN_GLOBAL_FOLDER: { cache: package-manager }
        timeout: 15m
      effects:
        repositoryToolOutputs:
          - node_modules
          - .yarn/install-state.gz
  verify:
    - { kind: directory, path: node_modules }
    - { kind: regular-file, path: .yarn/install-state.gz }
```

The names and example files above are profile data. Switchyard itself knows only generic resolution, command, fingerprint, and verification primitives. This is where certificate trust behavior, package-manager selection, cache placement, provisioning, timeouts, and verification belong.

The workspace fingerprint hashes the compiler version, repository configuration subtree digest, canonical worktree identity, sorted bounded input digests, requested and resolved toolchains, and command fingerprints. A matching fingerprint is still re-verified before it becomes a cache hit. Declared tool outputs are never fingerprint inputs.

Toolchain setup is two-phase because a final exact workspace plan cannot name an executable that does not exist yet:

1. resolve the requested toolchain from read-only user-managed locations and Switchyard's private toolchain cache;
2. if absent, acquire a machine-wide provision lock for the toolchain key and durably journal an exact provision operation;
3. run the approved provision command, with Switchyard-owned installs confined to the private cache;
4. re-resolve and verify the executable identity and version;
5. compile and fingerprint the concrete workspace plan;
6. continue preparation under the per-worktree coordinator.

Provisioning is independently timeout-bound, resumable after restart, and deduplicated across repositories and worktrees. A profile may explicitly call a user-managed external version manager, but its outputs are descriptive external tool effects, not Switchyard-owned files.

## Environments and services

The compiler maps targets, selected services, assigned leases, declared infrastructure, initialization steps, probes, and private artifacts into the existing `environment.ExecutionPlan`.

Targets are deterministic single-parent inheritance graphs. Environment precedence is:

1. a small trusted process base;
2. inherited and selected target values, including the repository's environment selector;
3. service command values;
4. Switchyard-owned leases, routes, caches, and private artifact paths.

Maps override explicitly. Lists replace unless the schema defines set semantics. Remote-write targets require confirmation on every start.

Switchyard invokes the repository's normal configured command. That command owns dotenv loading and selects its existing environment files from the target selector already present in the child environment. Switchyard never parses those files or compiles their entries into the child. Allocated ports and derived local URLs are present before repository tooling loads dotenv, so ordinary non-overriding dotenv behavior preserves the runtime values Switchyard owns.

Services declare exact commands, dependencies, ports, published URLs, readiness, health, infrastructure, initialization, and private artifacts. Dependency and target graphs must be acyclic. The flat v1 `ExecutionPlan` must become ordered stages: each stage launches its independent services and passes a readiness barrier before dependants in the next stage may launch. Rollback and stop traverse the accepted concrete stages in reverse. Dependencies are not merely presentation or sort hints.

Mutable infrastructure remains environment-scoped in v1. Only immutable content-addressed caches may be shared until namespacing and reference-counted teardown have independent runtime proofs.

## Private artifacts instead of repository projections

`ProjectionApplier` becomes `ArtifactMaterializer`. Its only writable root is the Switchyard private runtime directory. It cannot receive or derive a destination under a repository, worktree, or Git directory. Artifact paths include the repository-profile and content digests. Creation is no-follow, exclusive, owner-only, and single-link verified; content is never replaced in place.

Switchyard-owned runtime values should be passed directly to child processes. When a tool needs a generated non-secret file, the profile declares a private artifact and passes its private absolute path in argv or environment. Active references protect immutable artifacts from cleanup, and every loaded or executed artifact digest participates in the command fingerprint.

A profile can also declare a private wrapper artifact when a third-party tool cannot consume an external configuration directly. The configuration activation preview fingerprints behavior by repository key, repository-profile digest, executable identity, argv, wrapper and referenced artifact digests, working directory, and non-secret environment shape. Shells, interpreters, and generated wrappers are additionally labelled executable profile code. The owner accepts or revokes the complete configuration revision through the app or human CLI. Noninteractive MCP and setup hooks may execute an accepted revision, but cannot accept a pending or changed revision; they return `CONFIGURATION_NOT_ACCEPTED` without exposing the complete preview.

Declared effects make plans and cleanup boundaries inspectable; they do not sandbox a command or prove ownership of what it writes. Repository credentials never enter Switchyard argv, persisted plans, artifacts, configuration history, or diagnostics; the repository's normal loader keeps them outside the control-plane model.

Before the breaking release, a parity spike must prove that every currently supported service can run without a Switchyard-owned file in its checkout. A failed parity case keeps the release incomplete; the boundary is not weakened.

## Multi-repository product model

The status contract already carries a repository array and the Swift sidebar already groups worktrees by repository. The backend must make that structure real:

- discover all enabled repository roots concurrently with bounded fan-out;
- isolate observation failures to one repository;
- compile every worktree from its owning repository key and configuration digest;
- allow simultaneous environments from different repositories;
- calculate stable environment identity from repository identity plus worktree identity;
- present **Add Repository**, **Edit Configuration**, **Validate**, **Disable**, and **Remove** actions in the app;
- show configuration health and active revision per repository;
- retain the existing repository picker for managed worktree creation.

Adding a repository is a private configuration mutation. It never creates a file in the selected checkout.

## Profile actions

Profile actions are a real operation domain, not UI aliases for raw terminal scripts. The generic action compiler produces an exact approved command for one machine, repository, worktree, environment, or service scope. The daemon persists an action operation before execution, runs it in a positively owned process group with bounded logs and timeout, and publishes a receipt and audit event.

The contract, API client, CLI, MCP metadata, Swift model, approval store, operation journal, and action runner all consume the same action definition. Lifecycle actions such as prepare, start, stop, and cleanup dispatch their existing dedicated operations; command actions use the generic runner. A toolbar button never executes a command directly in the Swift process.

## Cleanup

The current code has useful ownership and planning foundations but not a complete generic cleanup API or UI. The breaking release adds one cleanup domain shared by the app, CLI, and MCP:

1. `plan(scope, selections)` inventories exact owned candidates, protections, estimated bytes, and current revisions;
2. the daemon persists a short-lived plan with an opaque ID;
3. `apply(planID, expectedRevision, candidateIDs)` revalidates every candidate immediately before mutation;
4. the daemon records results and foreign survivors.

The app exposes **Cleanup…** globally and at repository, worktree, and environment scope. It opens a review sheet; it is not an instant destructive button. Safe stopped runtime artifacts and expired private caches may be preselected. Managed worktrees, ordinary repository tool outputs, ambiguous legacy files, and anything dirty, unpushed, active, locked, foreign, or agent-occupied are not cleanup candidates.

Cleanup candidate kinds include stopped owned processes, labelled infrastructure, private run artifacts, private caches, and Switchyard state maintenance. Each kind has its own ownership proof and protections. Worktree archive remains a separate protected lifecycle action. Dependency cleaning or other repository maintenance may be a separately confirmed exact profile action, but arbitrary commands and declared paths never acquire trusted cleanup or deletion authority.

## Agent integration

The portable foundation remains the generic MCP server plus its managed skill. Tools accept exact worktree paths or stable IDs and return bounded structured context. New cleanup tools use the same plan/apply contract. MCP contains no repository or lifecycle logic.

The native app adds agent-launch actions on every worktree. For Codex, **Start Codex Task** first prepares the exact worktree, then uses the documented `codex://new` deep link with the absolute `path` and a bounded prefilled prompt. A capability spike against the installed app must verify the link before the UI promises task creation; the fallback opens the exact folder with `codex app PATH`. This avoids guessing a branch and avoids making Switchyard configuration part of the repository. Other agent hosts use capability-specific launchers behind the same app-facing interface.

A fire-and-forget launcher cannot prove that an agent still occupies a worktree. Switchyard records only an explicit conservative handoff lease for tasks it launches (`POST /v1/worktrees/{id}/occupancy` after the launch succeeded, holder kind `agent-task`) and requires owner release unless a host integration later provides authenticated lifecycle state. It never infers occupancy from a deep link, scans transcripts, or weakens archive protections: a held lease makes archive refuse with `WORKTREE_OCCUPIED`, and the app shows the handoff on the worktree with an explicit **Release** action.

Codex local-environment settings are project-scoped and stored by Codex under `.codex`. Switchyard therefore does not create or manage that file. A user may independently choose the one-line `sy prepare . --wait` setup hook, but the zero-footprint Switchyard flow is to create or select and prepare a worktree in Switchyard before launching the agent task.

MCP Apps UI is an optional later enhancement. Tools remain fully useful without embedded UI, and any UI uses the portable MCP Apps bridge rather than branching on a host product name.

Direct Codex app-server integration is not required for this release. It is a separate product decision because it would make Switchyard a rich Codex client responsible for conversation history, approvals, and streamed agent events.

## Bounded state

The 0.1.0 database retained every full status snapshot. That was unbounded and has been replaced.

The current schema stores the atomic snapshot in one row with a monotonic revision. Durable operations, ownership, configuration revisions, and event history remain separate and are bounded transactionally: 10,000 events, 500 terminal operations (current environment and workspace results and incomplete operations are always retained), and 16 unreferenced accepted configuration revisions (the head and every revision pinned by a live environment result or incomplete operation are always retained; stopped results pin nothing). Staged candidates keep the most recent 16. Unchanged observations do not create durable history merely to update a timestamp.

The 0.2.0 daemon opens a fresh `state-v2.sqlite` and never reads, imports, or migrates a 0.1.x database, runtime root, or ownership record. Because Switchyard is single-user, the cutover deletes or archives the 0.1.x Application Support contents rather than carrying them forward.

## Cutover and release

The design is a breaking configuration, state, and composition change. It ships as `0.2.0`, not as an expansion of the unshipped `0.1.1` candidate.

Development and parity testing use an isolated Application Support root, token, logs, database, helper path, and development LaunchAgent label. The development daemon uses fixtures or synthetic repositories and cannot claim the same resources as the installed daemon. Building or opening a development app cannot inspect, replace, repair, or start the canonical helper.

Cutover is a clean install, not a migration. Switchyard is single-user, so no compatibility path for 0.1.x state, operations, or runtime ownership exists in the product:

1. validate the complete private repository profile and a synthetic second repository;
2. stop every 0.1.x environment and quit the 0.1.x app and daemon;
3. delete or archive the 0.1.x Application Support directory, LaunchAgent, and helper; nothing inside a consuming repository is touched, and any 0.1.x files there remain for manual review;
4. install the 0.2.0 Cask, let the app install and bootstrap its own helper, and require a successful handshake before repairing agent registrations;
5. accept the private profile, then pass doctor, multi-repository inventory, prepare, start, stop, cleanup-plan, and agent-launch smoke tests.

Switchyard does not edit a checkout or `.git/info/exclude`, even during cutover. Normal Homebrew uninstall removes only Cask-owned paths.

## Implementation slices

1. Add an explicit development channel so development builds cannot touch canonical installation paths, labels, state, repositories, or agent registrations.
2. Run an isolated zero-footprint parity spike for every currently supported service and use it to inventory the required generic primitives.
3. Amend accepted decisions and agent invariants; freeze the private schema with synthetic fixtures and the spike evidence.
4. Add the strict private configuration loader, crash-consistent immutable revision store, CAS API, approval store, and fail-closed desired/accepted behavior.
5. Add bounded snapshot storage and cutover planning before adding more observers.
6. Move generic Git discovery, source snapshots, diff accounting, command execution, preparation, and readiness out of repository-specific packages.
7. Replace adapter fields with repository key plus configuration payload/digest in the core, contract v2, SQLite records, API, CLI, MCP, and Swift models. Persist concrete recovery behavior for start, readiness, rollback, stop, and cleanup. Done: contract v2 publishes `profileKey`, pinned intent and results carry `ProfileDigest`, 0.2.0 starts from a fresh database, and pinned payloads recover from retained revisions across restarts.
8. Add the two-phase toolchain provision lifecycle and declarative workspace compiler while preserving the resilient `prepare --wait` behavior from the candidate branch.
9. Add staged service launch/readiness barriers and compile declarative targets, services, ports, infrastructure, initialization, probes, and immutable private artifacts.
10. Add the generic action operation/compiler/runner and the cleanup plan/apply domain across journal, contract, API, CLI, MCP, and Swift clients.
11. Switch daemon composition to the multi-repository registry and atomically activate per-repository profile revisions.
12. Add configuration management, Add Repository, Review Cleanup, profile actions, and capability-tested agent-launch UI.
13. Build the real private profile outside the source tree and prove full service parity plus simultaneous synthetic repositories.
14. Delete repository-local configuration, exclusion, projection, and built-in catalog code; replace all product copy, fixtures, prompts, and skill text with synthetic terminology.
15. Pass the zero-reference and zero-footprint gates, full safety suite, race suite, Swift checks, fresh-app visual verification, fresh-install rehearsal, and release dry run.

## Release gates

- No consuming-repository reference exists in the tracked tree.
- No test or live run creates a Switchyard-owned path inside a checkout or Git directory.
- Two unrelated configured repositories prepare and run concurrently.
- A profile containing certificate, package-manager, toolchain, cache, target, service, and infrastructure details requires no application-code change.
- Every service supported before the redesign passes the zero-footprint parity suite.
- Configuration reload cannot mutate a running environment and stop/cleanup still work after a profile is disabled.
- Every executable behavior is included in the configuration-revision preview, authorized by the accepted revision digest, revocable as a revision, and tested through rejection and failure paths; executable profile code is additionally labelled.
- Concurrent missing-toolchain preparation provisions once, resumes safely after restart, and re-resolves before workspace compilation.
- Service dependency stages enforce readiness barriers and reverse-order rollback.
- Foreign processes, ports, containers, files, worktrees, and caches survive every cleanup test.
- The database remains bounded under a multi-day accelerated observer test.
- The installed 0.1.x version remains untouched throughout development; the cutover is a deliberate fresh install after the 0.1.x state is deleted or archived.
- Every discoverable `sy` path resolves to the accepted release after cutover, and removal of an obsolete link requires exact ownership proof.
