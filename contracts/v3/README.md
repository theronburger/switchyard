# Local API v3

The JSON types are defined in [`internal/workspaces/types.go`](../../internal/workspaces/types.go).
[`status.json`](status.json) is a synthetic snapshot with independent running and
failed workspaces. [`config.json`](config.json) is a valid private configuration
for the same example. Its configuration file version is `schemaVersion: 1`;
the HTTP API and status snapshot use version `3`.

Every request requires `Authorization: Bearer <local token>` and
`X-Switchyard-Version: 3`. The helper publishes its loopback endpoint in
`daemon/runtime.json` and its separate token in `daemon/token` under the selected
support root. Browser-origin requests are rejected. No token belongs in a URL.

Requests reload valid external edits to `config.json`. If an edit is invalid,
status retains the last valid configuration and includes `configurationError`.
Stop and log reads remain available; configuration-dependent mutations are
refused until the file is fixed. `POST /api/config` can replace an invalid file.

| Request | Body or query | Result |
| --- | --- | --- |
| `GET /api/status` | None | `Snapshot` |
| `GET /api/context` | `path` query | One `Workspace`; existing child directories resolve through Git |
| `GET /api/config` | None | Private `Config`, including command environments |
| `POST /api/config` | `Config` | `{ "ok": true }` |
| `POST /api/choices` | Object with `path`, `target`, `services` | `{ "ok": true }` |
| `POST /api/run` | `RunRequest` | Accepted `Run` |
| `POST /api/prepare` | `RunRequest` | Accepted setup-only `Run` |
| `POST /api/stop` | Object with `path` | Stopped `Run`; also cancels setup or startup |
| `GET /api/logs` | `path` query | `LogOutput` for the workspace's latest run |
| `GET /api/prune-plan` | `path` query | Exact path, branch and removal blockers |
| `POST /api/prune` | Object with `path` | `{ "ok": true }` after rechecking blockers |
| `POST /api/create` | `CreateRequest` | `{ "path": "/absolute/new/worktree" }` |

Mutations use the exact physical workspace root. Run acceptance returns before
setup finishes. Poll the workspace's `run.id` and `run.state`; an older run or a
different workspace is not evidence that the accepted run succeeded. Reusing a
request ID with the same request returns that run while its request is retained
in the current helper instance. A retry after failure needs a new request ID.

Run states are `starting`, `running`, `stopping`, `stopped` and `failed`.
`step` describes current progress. Successful preparation ends at
`state: "stopped", step: "Prepared"`. Service states are `starting`, `running`
and `stopped`. A workspace with no run omits `run`.

Run state and bounded logs live in the current helper instance. The app closing
does not stop runs. A helper restart stops its processes and begins a new
`instanceId`; config and per-workspace choices remain saved.

Errors use `{ "error": "plain reason" }`. Invalid requests and refused actions
return HTTP 400, authentication failures 401, unknown routes 404, and a mismatched
or missing version header 426. Routine status contains neither command scripts
nor environment values. The explicit config endpoint is private configuration
access, and explicit log reads return bounded, redacted output.
