# summond

Schedule and manage macOS background jobs without writing a single plist.

`launchd` is powerful but painful to use directly. Summond wraps it with a CLI that handles plist generation, log management, and job lifecycle — so you can focus on what the job actually does.

## Install

```sh
brew tap joshgummersall/summond https://github.com/joshgummersall/summond
brew install summond
```

Or install from source (requires Go 1.24+):

```sh
go install github.com/joshgummersall/summond/cmd/summond@latest
```

## How it works

Define jobs in a TOML file and apply them:

```toml
[jobs.cleanup]
command  = "/usr/local/bin/my-script"
schedule = "daily"
hour     = 3
minute   = 0

[jobs.cleanup.env]
MODE = "nightly"
```

```sh
summond apply
```

That's it. Summond generates and loads the launchd plist, creates log files, configures `newsyslog` rotation, and tracks execution history. When you delete a job from the config, `summond apply --prune` removes it cleanly.

## Or skip the config file entirely

```sh
# Run a binary on a schedule
summond add agent cleanup --schedule daily --hour 3 --minute 0 -- /usr/local/bin/my-script

# Run a shell snippet
summond add agent rotate-logs --schedule daily --hour 3 --minute 30 <<'EOF'
find /tmp -type f -mtime +7 -delete
EOF
```

## Schedules

`login`, `boot`, `hourly`, `daily`, `weekly`, `interval`, and `calendar` — with `--hour`, `--minute`, `--weekday`, and `--interval-minutes` flags to tune them. Omit the timing flags and summond deterministically seeds them from the job name to spread load.

## File watch trigger

Run a job when files change instead of on a schedule:

```toml
[jobs.on-config-change]
command     = "/usr/local/bin/reload"
trigger     = "on_change"
watch_paths = ["./config.json"]
```

The job receives a `SUMMOND_CHANGED_PATHS` environment variable — a colon-separated list of entries from `watch_paths` that changed since the last successful run. Note that these are the paths you listed in `watch_paths`, so a watched directory appears as the directory itself, not the individual file that changed within it. On the first execution (no prior baseline), all `watch_paths` are included.

```sh
# shell_command example
for path in ${SUMMOND_CHANGED_PATHS//:/ }; do
  reload "$path"
done
```

## Logs, status, and execution history

```sh
summond list                  # all jobs with last-run status
summond logs cleanup          # output from the last execution
summond logs cleanup -f       # stream new output live
summond exec cleanup          # run immediately in the foreground
summond state cleanup         # full execution history as JSON
```

Stdout and stderr go to `~/Library/Application Support/summond/logs/` and are rotated by macOS's native `newsyslog`. No third-party log management needed.

## Agents and daemons

Jobs run as LaunchAgents (per-user) by default. Set `target = "daemon"` for system-wide LaunchDaemons — summond will prompt for `sudo` when needed.

## Retry and backoff

Jobs that exit non-zero can be automatically retried with exponential backoff:

```toml
[jobs.flaky-api-sync]
command              = "/usr/local/bin/sync"
schedule             = "hourly"
retry_attempts       = 3   # retries after the initial attempt
retry_delay_seconds  = 5   # wait before first retry (default 1)
retry_max_delay_seconds = 60  # cap the doubling delay (0 = no cap)
```

Or via `summond add`:

```sh
summond add agent flaky-api-sync --schedule hourly \
  --retry-attempts 3 \
  --retry-delay-seconds 5 \
  --retry-max-delay-seconds 60 \
  -- /usr/local/bin/sync
```

Delay doubles between each retry (5 s → 10 s → 20 s … up to the cap). Only exit-code failures are retried; if the binary can't be launched at all, summond gives up immediately.

Each job receives a `SUMMOND_ATTEMPT` environment variable (0-indexed) so scripts can adapt their behavior on retries:

```sh
# shell_command example — skip expensive setup on retries
if [ "$SUMMOND_ATTEMPT" -eq 0 ]; then
  do-expensive-preflight
fi
do-the-actual-work
```

Retry annotations (`[summond] retry attempt N/M after Xs`) are written to the job's stderr log between attempts.

## Shared environment

```sh
summond env set API_KEY=secret   # injected into every job at runtime
```

Per-job `[jobs.name.env]` values are merged on top.
