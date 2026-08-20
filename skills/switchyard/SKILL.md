---
name: switchyard
description: Manage local development worktrees and isolated environments through the Switchyard MCP. Use for creating, adopting, or archiving Switchyard-owned worktrees, preparing repository toolchains and dependencies, starting or stopping configured services, obtaining local URLs, inspecting health, or repairing the local control plane. Do not use for production deployments or direct Git/Docker/process lifecycle commands when Switchyard owns the resource.
---

# Switchyard

Use Switchyard as the sole lifecycle owner for local worktree services. Prefer its MCP tools over terminal commands.

## Inspect

1. For requests about the current worktree, call `switchyard_context` with the physical absolute workspace path. A child directory is valid. If a host path hint is rejected, use read-only `pwd -P` and retry that path; never infer identity from a branch fragment or inventory ordering.
2. Use `switchyard_inventory` only for deliberate cross-worktree inventory or to resolve a repository before creating a worktree.
3. Poll an accepted environment operation with `switchyard_environment_status` and its exact environment ID.
4. Use `switchyard_operation_diagnostics` only when bounded status context is insufficient. Do not reproduce sensitive log contents unnecessarily.
5. Run `switchyard_doctor` for discovery, identity, or daemon connectivity failures.

## Start an environment

1. Resolve the current exact worktree with `switchyard_context`.
2. Select only services and runtime targets published by its repository catalog.
3. If a target has `warnOnStart`, ask the human to approve that exact target for this one start. Do not infer approval from earlier work.
4. Call `switchyard_start` with unique non-secret request and idempotency IDs, the resolved worktree ID, selected target and services, and the current revision when replacing an environment.
5. Treat the receipt as acceptance. Poll the exact environment until the operation is terminal and selected services are healthy.

The first start verifies the repository fingerprint, provisions an adapter toolchain when needed, hydrates dependencies, and records workspace readiness before services build. Do not install those prerequisites separately.

## Stop an environment

Stop only when requested or when cleanup is an agreed part of the task. Read fresh exact environment status immediately before calling `switchyard_stop`, pass its latest revision, and wait until no owned leases or infrastructure remain.

Quitting `Switchyard.app` does not stop environments.

## Create or adopt a worktree

- For creation, resolve the repository ID from `switchyard_inventory`; never guess it. Call `switchyard_create_worktree` with a new branch, optional start point, and stable retry idempotency key. Poll inventory through the brief helper restart, then resolve the returned exact path.
- Adopt only an eligible non-primary worktree already shown as `adopted`. Resolve its exact worktree ID with `switchyard_context`, then call `switchyard_adopt_worktree`. Preserve refusals for dirty, detached, unpushed, upstream-less, foreign, out-of-root, symlinked, or unverifiable worktrees.

## Archive a managed worktree

Archive only when explicitly requested. Resolve the exact path, verify `managed` ownership, stop its environment, then call `switchyard_archive_worktree`. Never force archive or clean/reset a checkout to make it eligible.

## Safety boundaries

Never work around Switchyard by:

- launching persistent configured services through raw package-manager, process-manager, container, or terminal commands;
- creating or removing Switchyard-owned worktrees with raw Git commands;
- using `kill`, `pkill`, Docker stop/remove/prune, or broad cleanup commands;
- editing ownership records, runtime files, port leases, or generated `.switchyard.*` projections;
- modifying consuming-repository tracked files, public `.gitignore`, or private environment profiles.

Switchyard acts only on positively owned process groups, labelled Docker resources, and verified managed worktrees. Preserve a refusal when ownership cannot be proven.
