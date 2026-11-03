# 1. The effective UID selects the job scope

Date: 2026-11-02

## Status

Accepted

## Context

summond manages two kinds of launchd jobs: LaunchAgents, which run as the logged-in user in the `gui/<uid>` domain, and LaunchDaemons, which run as root in the `system` domain. They differ in owner, state directory, env file, plist directory, and launchctl domain.

summond v1 let one process manage both kinds:

- Non-root summond escalated with `sudo` whenever a command touched daemon state. `apply`, `add`, `remove`, `env --daemon`, `install`, and `uninstall` each had their own sudo retry prompts and privileged helpers.
- Under `sudo`, summond mapped its agent paths back to the invoking user through `SUDO_USER`. As a result, root wrote into the user's home: `sudo summond env set` left the agent `env.json` owned by root and unreadable by agent jobs, and agent jobs applied under sudo loaded into `gui/0`.
- One config file could mix agent and daemon jobs through a per-job `target` key.
- `summond install` installed a newsyslog config under `/etc` with sudo. That config never matched the real log paths.

## Decision

- **The effective UID selects the scope.** `summond` manages agent jobs only. `sudo summond` manages daemon jobs only. Each scope has one state directory, one env file (mode 0600), one plist directory, and one launchctl domain. summond does not read `SUDO_USER`, and no command escalates with sudo or reaches into the other scope.
- **A config file targets one scope.** `target` is a top-level key that defaults to `agent` and applies to every job in the file. `apply` refuses a config for the other scope, with the command to run instead. A per-job `target` is an error.
- **There is no install step.** `apply` and `add` create a scope's state on first use. summond rotates its own logs in `exec` and does not use newsyslog.
- **The Homebrew cask's zap runs `summond uninstall` twice,** once as the user and once with sudo, which removes both scopes.

## Alternatives considered

- **Keep the `SUDO_USER` remap and patch each root write into the user's home.** This keeps the bug class, where root-owned files land in the user's home, and every new command must remember to avoid it.
- **Treat root as a plain user** with its own agent store under `/var/root`. Root agents are not useful on macOS, and daemons would still need special handling.
- **Allow mixed configs** and have each scope apply its own jobs while skipping the rest. One file would then need two runs to apply fully, and pruning would have to reason across scopes.
- **Infer the config target from sudo.** One mistaken `sudo summond apply` on an agent config would install every job as a root daemon.
- **Run setup from a Homebrew postflight hook.** Cask hooks run on every upgrade and inside Homebrew's sandbox, and the newsyslog file would have needed sudo on every fresh install.

## Consequences

- Plain `summond list` and `summond tui` show only agent jobs. Use `sudo summond list` for daemon jobs.
- A setup with both kinds of jobs needs two config files and two `apply` runs.
- Under sudo, `$VAR` expansion in a config uses root's environment, so `$HOME` may not resolve to the invoking user's home.
- The CLI has no sudo prompts left. A command run without sudo never modifies daemon state.
