# Private configuration

Configure the commands that make one fresh Git worktree ready, then the commands
that keep each service running. In Repositories, the configuration icons copy
the file path, open its folder, or open it in your default editor. Save the file
there; the helper validates and applies changes on its next request. The app
refreshes every two seconds. `sy config set FILE` also applies a configuration.
Later runs use it; running processes keep their current setup.

Invalid edits show an error and leave the last valid settings and running
services available. Fix and save the file to apply the changes. Stop still works
while the file is invalid; starting or saving choices waits for a valid file.

The release stores `config.json` in
`~/Library/Application Support/Switchyard/`. Development builds use
`~/Library/Application Support/Switchyard Rebuild/`; `--root` selects another
support directory. The file is saved atomically with mode `0600`. No configuration
file is added to the consuming repository.

This example assumes an API with an `npm run dev` script that uses `PORT` and
serves `/health`, plus a Vite frontend in `apps/web`. Both have their own lockfile.
Replace those commands and directories with your repository's existing tooling.
The complete [example file](../contracts/v3/config.json) also shows remembered
workspace choices. The frontend uses Vite's documented
[`--host`, `--port` and `--strictPort` options](https://vite.dev/guide/cli#dev-server)
to bind the assigned address and reject silent port changes.

```json
{
  "schemaVersion": 1,
  "repositories": [
    {
      "id": "sample",
      "name": "Sample app",
      "path": "/tmp/sample",
      "defaultTarget": "local",
      "targets": [{ "id": "local", "name": "Local" }],
      "setup": [
        { "script": "npm ci", "directory": "apps/api" },
        { "script": "npm ci", "directory": "apps/web" }
      ],
      "services": [
        {
          "id": "api",
          "name": "API",
          "command": { "script": "exec npm run dev", "directory": "apps/api" },
          "ports": [{ "name": "http", "environment": "PORT", "preferred": 8080, "url": true }],
          "readiness": { "port": "http", "path": "/health" }
        },
        {
          "id": "web",
          "name": "Web",
          "dependencies": ["api"],
          "command": {
            "script": "exec npm run dev -- --host 127.0.0.1 --port \"$PORT\" --strictPort",
            "directory": "apps/web",
            "environment": { "VITE_API_URL": "http://localhost:{port:api.http}" }
          },
          "ports": [{ "name": "http", "environment": "PORT", "preferred": 5173, "url": true }],
          "readiness": { "port": "http", "path": "/" }
        }
      ]
    }
  ]
}
```

## Setup must work on a fresh worktree

Every Run executes the repository's `setup` commands in order, then the selected
services' `prepare` commands, then starts services in dependency order. Selecting
`web` in this example also starts `api`. `sy prepare PATH --wait` executes only
repository setup. Dependency caches belong to the repository's tools; setup
should be safe to repeat.

Git worktrees do not bring over ignored `.env` files, generated configuration,
certificates or installed dependencies from another checkout. If the app needs
them, make their creation or copying an explicit setup command. Use a private
source outside the repository for local values, and follow the repository's
existing ignored-file conventions. Switchyard does not guess which files to
copy or change tracked files, public ignore rules or existing helpers for you.

Commands run in a noninteractive shell. `directory` is relative to each workspace
and defaults to `.`; it must exist and resolve inside that workspace. Keep service
commands in the foreground. Do not background them or start a second process
manager. Use absolute executable paths or set `PATH` explicitly if a tool is
available only through your interactive shell setup.

Container prerequisites can be ordinary configured services too. Give an attached
`docker run --rm` command its assigned ports, readiness check and a preparation
command that starts your local container runtime if necessary. Other services
declare it as a dependency. Switchyard itself does not require a container runtime.
Use an explicit local Docker context and keep its lifecycle commands in private
configuration. Do not use detached containers or global cleanup commands.

For finite setup and service preparation, `timeoutSeconds` defaults to 900.
The service's long-running `command` is stopped through Stop; its command timeout
does not limit service lifetime.

## Ports and command environment

`preferred` is a preference, not a fixed port. Each run gets available loopback
ports, including when another workspace or a foreign process already uses a
preferred number. The service must bind the assigned port instead of selecting
another one silently. `environment` on a port names the variable receiving its
number. `url: true` makes the URL available after that service passes readiness.

Target and command environment values support these substitutions:

| Value | Meaning |
| --- | --- |
| `{workspace}` | Exact physical workspace directory |
| `{repository}` | Configured repository directory |
| `{instance}` | This run's unique ID |
| `{port:api.http}` | Assigned `http` port of selected service `api` |

Substitution happens only in environment values. Put a path in an environment
variable, then use a quoted shell expansion such as `"$WORKSPACE"` in the script.
Scripts are never rewritten with workspace paths or port placeholders.

Every command also receives `SWITCHYARD_WORKSPACE`, `SWITCHYARD_REPOSITORY`, and
`SWITCHYARD_PORTS`. The last is a JSON object such as
`{"api.http":8080,"web.http":5173}` containing all ports assigned for that run.
This lets setup generate ignored runtime files without parsing status output.
The numbers can change on the next run.

The helper supplies basic host variables (`HOME`, `PATH`, `TMPDIR`, `LANG`,
`LC_ALL`, `SHELL`) when available. Target environment applies next, command
environment overrides it, and assigned service port variables take precedence.
Other interactive-shell environment variables are not inherited automatically.
Keep credentials in private local sources or explicit private configuration;
avoid printing them in scripts.

## Readiness and targets

`readiness.port` names one of the service's own ports. An empty `path` checks a
TCP connection; a path beginning with `/` performs an HTTP GET and requires a
2xx response. Readiness defaults to a 60-second timeout. Omitting readiness
checks only that the process remains alive through startup. Use a real readiness
endpoint when a listening process is not enough to prove the app works.

Targets are named environment choices. Set `defaultTarget` to a target ID, and
add `"confirm": true` to targets that should require confirmation before Run.
Each physical workspace remembers its target and selected service IDs. Run
choices survive app and helper restarts.

## Running, stopping and fixing a failed start

The run moves through `starting`, `running` and `stopping`, ending at `stopped`
or `failed`. Its current step identifies setup, service preparation or startup.
Stop also cancels an in-progress setup or start. A failed run stops the other
services launched for that run; another workspace keeps running. Fix the reason
shown in Logs, then Run again.

Closing the app leaves services running. Restarting the helper stops its runs;
open the workspace and Run again. Logs belong to the current helper instance.
Successful setup-only preparation reports `stopped` with step `Prepared`.

Worktree creation uses Git's ordinary branch/worktree behavior. Optional
`worktreesPath` selects an absolute parent directory; otherwise new worktrees
go beneath a sibling `<repository-name>-worktrees` directory. Prune previews the
exact directory and refuses primary, locked, dirty, unpushed, active or invalid
worktrees. It checks again immediately before asking Git to remove that one
worktree without force.

The [v3 contract](../contracts/v3/README.md) documents status, run, stop, logs,
configuration, choices, creation and prune endpoints used by the app and agents.
