# Fable environment integration review

You are the independent principal engineer reviewing Switchyard, a private personal macOS control plane for one developer's Marketplace worktrees and local services. Work read-only. Do not edit files, run services, start or stop processes, mutate Docker, or change Marketplace.

Read these first:

- `README.md`
- `AGENTS.md`
- `docs/DECISIONS.md`
- `docs/ARCHITECTURE.md`
- `docs/MARKETPLACE.md`
- `docs/SAFETY.md`
- `internal/control/environment/**`
- `internal/state/**`
- `internal/runtime/portlease/**`
- `internal/runtime/processhost/**`
- `internal/runtime/containerhost/**`
- `internal/runtime/health/**`
- `internal/adapters/marketplace/**`

Review the environment coordinator and its intended Marketplace integration adversarially. In particular, look for:

1. any side effect that can occur before its operation and compensation are durable;
2. port assignment or plan rendering that happens outside the coordinator's persistence boundary;
3. cancellation, crash, retry, or restart windows that can leak a process, port, projection, container, or incorrect public snapshot;
4. ownership checks that could stop a foreign or PID/container-reused resource;
5. per-environment serialization that accidentally blocks unrelated worktrees or permits same-environment overlap;
6. rollback order, partial rollback, and idempotency flaws;
7. ways mutable infrastructure or routing could cross between two worktrees;
8. API/contract choices required for safe asynchronous start/stop actions from Swift, CLI, and thin MCP clients;
9. unnecessary generic machinery or names that obscure the domain;
10. the smallest concrete integration path to the organizer + nonprofit-service two-worktree golden run.

Return a concise report ordered P0, P1, P2. Cite exact files and symbols. Distinguish proven defects from design questions. End with a recommended integration sequence and five must-have end-to-end failure tests. Do not praise the design or restate the architecture.
