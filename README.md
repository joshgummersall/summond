# summond

A macOS-focused CLI for managing friendly `launchd` jobs with native LaunchAgents and LaunchDaemons.

## Prerequisites

- `mise` installed
- macOS

## Setup

```sh
mise install
```

## Getting started

```sh
mise test
mise install
summond version
summond install
```

## Examples

Bootstrap the default config and log rotation:

```sh
summond install
```

This creates `./summond.toml`, generates a `newsyslog` snippet for Summond-managed logs, and will ask to retry with `sudo` if the system install step needs elevated privileges.

Apply the generated config:

```sh
summond apply
```

Apply and automatically remove managed jobs that were removed from the config:

```sh
summond apply --prune
```

Create an hourly user job directly:

```sh
summond add cleanup \
  --command /bin/echo \
  --schedule hourly \
  --minute 15 \
  "hello from summond"
```

Create a daily shell-based job:

```sh
summond add rotate-logs \
  --shell 'find /tmp -type f -mtime +7 -delete' \
  --schedule daily \
  --hour 3 \
  --minute 30
```

Apply jobs from TOML:

```toml
[jobs.cleanup]
command = "/bin/echo"
args = ["cleanup"]
target = "agent"
schedule = "daily"
hour = 3
minute = 45

[jobs.cleanup.env]
MODE = "nightly"
```

```sh
summond apply
```

If you later delete jobs from `summond.toml`, `summond apply` will prompt to remove the orphaned managed jobs and their managed logs/state. To skip the prompt:

```sh
summond apply --prune
```

## Logs

Summond writes stdout and stderr to managed log files under `~/Library/Application Support/summond/logs/` by default. Those files are created during install/update. `summond install` scaffolds `newsyslog` configuration so those files can be rotated using the native macOS mechanism.

```sh
summond logs cleanup
```

## File Watch Triggers

Use `trigger = "on_change"` with `watch_paths` to run a job when files change. Relative `watch_paths` are resolved against the TOML file being applied.

```toml
[jobs.watcher]
command = "/bin/echo"
args = ["config changed"]
target = "agent"
trigger = "on_change"
watch_paths = ["../fixtures/input.txt"]
```

## Supported schedules

- `hourly` with `--minute`
- `daily` with `--hour` and `--minute`
- `weekly` with `--weekday`, `--hour`, and `--minute`
- `login` for LaunchAgents
- `boot` for LaunchDaemons
- `interval` with `--interval-minutes`
- `calendar` with `--month`, `--day`, `--weekday`, `--hour`, and `--minute`
