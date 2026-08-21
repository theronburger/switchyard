# Key-session secrets for daemon-managed children: blocker report and smallest viable design

Status: blocked on a key-session primitive. No launch path is implemented; the accepted schema fails closed.

## Requested capability

A private profile names a key-session profile through a non-secret `secretProviders` reference, and a daemon-managed child (service, initialization, preparation, or profile action) receives that profile's Keychain value in its environment through an already user-approved key-session lease. Nothing secret — value, consumer capability, lease — may enter the accepted canonical payload, SQLite, persisted plans, ownership metadata, argv, command fingerprints, logs, diagnostics, snapshots, or worktree files. The daemon remains the only runtime writer, key-session remains the only Keychain path, approval and expiry are explicit, and a missing or expired lease fails closed.

## Audit of the present tree (0.2.0, `fable/key-session-secrets`)

| Layer | State |
| --- | --- |
| Schema (`internal/configuration/types.go`) | `secretProviders` exists with a single `kind` field. `ValueRef` has no secret variant; a `secret:` or `lease:` reference is rejected as an unknown field. |
| Loader | Before this branch any `kind` string was accepted silently. It now accepts only `kind: key-session`, bounds the map, and validates keys (`validateSecretProviders`). |
| Plan compiler (`internal/control/profile/plan_builder.go`) | Resolves literal, segment, host-home, target, port, URL, paths, artifact, cache, and extracted values. No secret source. The compiled plan is late-bound from `PlanIntent` on every start and is never persisted, so an in-memory secret layer *would* have a safe home here (the `environmentSources` layer added in `c600425` proves the pattern). |
| Process host (`internal/runtime/processhost`) | Launch intent and ownership persist executable, argv fingerprint, PIDs, start times, and log paths. Environment is never persisted or fingerprinted. A child launched by the host is a positively owned process group with owned append-only logs that the daemon tails. |
| Daemon API / CLI / app | No lease, consumer, or approval surface exists. |

Nothing in the tree can currently leak a secret because nothing can reference one. The gap is the entire runtime path, and it is not a Switchyard-only gap.

## The blocker

key-session 0.6.0 (`github.com/theronburger/key-session`) offers exactly one way to use a lease: `key-session exec --lease <id> -- <program> <args>`, authenticated by the consumer capability in `KEY_SESSION_CONSUMER_TOKEN`. The CLI does not fork the program. It posts an `ExecRequest` to the key-session daemon (`/v2/exec`), and **the key-session daemon forks the child** (`internal/daemon/service.go` → `execution.ExecuteWithSecret`). There is deliberately no API that returns a secret value to a caller; the skill forbids reading the value through any other path.

That execution model conflicts with every Switchyard supervision invariant at once:

1. **Ownership.** The real program is a descendant of the key-session daemon, not of a Switchyard-owned process group. Switchyard would own only the `key-session` CLI client. Stop, group revalidation, orphan recovery, and boot-time recovery (D-019, AGENTS "never kill by name") cannot positively act on the actual workload.
2. **Environment.** The child receives key-session's own minimal environment (filtered from the *key-session daemon's* `os.Environ()`) plus the one leased variable. Switchyard's compiled environment — leased ports, routes, target values, allowlisted environment sources, cache and artifact paths — never reaches it. A service cannot find its port.
3. **Logs and readiness.** stdout/stderr are captured in memory (1 MiB cap per stream) and returned only when the program exits. There are no owned rotating log files to tail, no operation diagnostics, and readiness/health observation has nothing to observe.
4. **Lifetime.** `exec` enforces a hard 1s–30m timeout. Long-running services are impossible by construction.
5. **Argv.** The lease ID must be an argument (`--lease`), so it would enter the launch fingerprint, launch intent, and ownership record. A lease ID is a selector rather than a secret, but per-lease argv breaks the accepted-revision fingerprint model (D-025): every new grant would look like executable drift.

The only two ways to satisfy the request inside this tree are both disallowed: extract the value (run `env`-style programs through `exec`, or a trampoline that pipes the value back to the daemon), which bypasses key-session's containment; or run workloads inside the key-session daemon and abandon positive ownership. Neither is a bounded change; both are unsafe. Per the task rule, no runtime code was written.

A secondary, solvable gap: the daemon has no consumer capability. `key-session grant` issues one only to the interactive caller after Touch ID and never to a background daemon. It must be handed to the daemon explicitly and held in memory only.

## Smallest viable design

Two halves. The key-session half is the blocker; the Switchyard half is small and can follow without a redesign.

### key-session: lease hand-off to a verified owned child

Add one capability-authenticated operation, `POST /v2/lease-handoff` (or an equivalent Unix-socket exchange), that releases the leased variable **only into a process the caller already spawned**:

- Request: consumer capability, exact lease ID, and the PID + start time of a not-yet-`execve`'d trampoline process.
- key-session verifies the lease (consumer, expiry, profile), verifies the peer (same UID, audit token / code identity of the connecting process matching the named PID), writes the variable name and value once over the connection, and audits `lease.handoff` with the lease, consumer, and PID — never the value.
- The trampoline is the only process that ever holds the value outside key-session, and it becomes the workload via `execve`. This is the same containment as `exec` today (the workload process holds the value in its environment), with the fork moved to the caller.
- The operation never returns the value over HTTP to an arbitrary caller: it is one-shot, bound to a live PID, and closes after a single write.

Until this exists, `kind: key-session` validates but authorizes nothing.

### Switchyard

1. **Schema.** `secretProviders.<key>: { kind: key-session }` (done). Add `ValueRef.Secret *SecretReference{ Provider, Profile string }`; both are identifiers, validated against the provider map and the key-session profile-name grammar. `Secret` is valid **only** inside `environment` maps of targets, services, commands, and actions — never in `arguments`, `segments`, artifacts, infrastructure environment (containers are a different process tree), or `values`. The canonical payload therefore carries provider and profile names only.
2. **Approval surface (CLI/app, owner only).** `sy secrets grant <provider> <profile> --reason … --duration …` runs the exact resolved `key-session grant` binary interactively (Touch ID), parses the `Consumer capability:` and `Lease … grants profile …` lines, and posts `{provider, profile, consumerToken, leaseID, expiresAt}` to `POST /v1/secrets/leases` over the authenticated loopback API. MCP and setup hooks cannot call it (same class as configuration acceptance, D-025). The CLI never prints the capability. The app uses the same route from a sheet.
3. **In-memory lease store (daemon).** `map[providerKey+profile] → {consumerToken, leaseID, expiresAt}`, mode: memory only. Never written to SQLite, snapshot, events, or logs; the status contract publishes only `{provider, profile, expiresAt, active}`. A daemon restart empties it: starts needing a secret fail closed with `SECRET_LEASE_REQUIRED` naming provider and profile until the owner grants again. `DELETE /v1/secrets/leases/{provider}/{profile}` revokes through `key-session revoke --lease` and drops the entry.
4. **Compile-time check.** The plan builder resolves `Secret` to a placeholder, not a value, and records the set of `(provider, profile)` the plan needs. Before `PhaseLaunchingServices` the coordinator asks the store; a missing or expired entry fails the operation closed (no partial launch). It re-checks `key-session status` through the capability at that moment so a revoked lease is also caught.
5. **Launch.** For each child that needs secrets, the process host spawns the owned group leader as `switchyard secret-trampoline` with the compiled non-secret environment and the workload's executable + argv passed over a private pipe (not argv). The trampoline asks key-session for the hand-off (capability and lease read from a pipe, never argv or inherited environment), sets the variable(s), scrubs `KEY_SESSION_*`, and `execve`s the workload. The launch fingerprint binds the workload executable and argv as today; the trampoline's own identity is constant. Ownership, logs, readiness, stop, and recovery are unchanged because the workload *is* the owned leader after `execve`.
6. **Expiry.** A lease that expires while a service runs does not kill it (the value is already in the child, as with key-session today); the status entry flips to expired and the next start fails closed. The app surfaces "secret lease expired" as attention, not as health.
7. **Tests.** Loader rejects `Secret` outside environment maps and unknown providers; canonical payload and SQLite fixtures never contain the value or capability; launch intent, ownership, fingerprint, and diagnostics redaction tests with a sentinel value; fail-closed start with no lease, with an expired lease, and after daemon restart; trampoline refuses when the hand-off peer is not key-session; cleanup/stop are unaffected.

## What this branch changes

- `internal/configuration/loader.go`: `validateSecretProviders` — only `kind: key-session`, bounded, identifier keys, fail closed on anything else.
- `internal/configuration/loader_test.go`: `TestParseSecretProvidersFailClosed` proves other kinds, malformed keys, secret data on a provider, and `secret:`/`lease:` value references are all rejected and the canonical payload never carries a secret or lease value.
- This document and the profile-document example (`kind: key-session`).

No runtime, daemon, CLI, app, or state code was changed. Private configuration, consuming repositories, and release versioning are untouched.
