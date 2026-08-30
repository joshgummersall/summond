# summond

Schedule and manage macOS background jobs without writing a single plist.

`launchd` is powerful but painful to use directly. Summond wraps it with a CLI that handles plist generation, log management, and job lifecycle — so you can focus on what the job actually does.

## Install

```sh
brew install --cask joshgummersall/summond/summond
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

`command`, `args`, `shell_command`, `working_dir`, `watch_paths`, and `env` values all support `$VAR`/`${VAR}` expansion, resolved against summond's own environment when you run `apply`. Referencing an unset variable is an error rather than silently resolving to an empty string:

```toml
[jobs.cleanup]
command     = "$HOME/bin/my-script"
working_dir = "$HOME/projects/foo"
```

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

`login`, `boot`, `hourly`, `daily`, `weekly`, `interval`, and `calendar` — with `--hour`, `--minute`, `--weekday`, and `--interval-minutes` flags to tune them. Omit the timing flags and summond deterministically seeds them from the job spec to spread load.

`daily` and `weekly` schedules also accept a `window`, which constrains the seeded hour to a named time-of-day range instead of the full day:

| Window      | Default hours |
| ----------- | ------------- |
| `morning`   | 05:00-11:59   |
| `afternoon` | 12:00-16:59   |
| `evening`   | 17:00-21:59   |

```toml
[jobs.report]
command  = "/usr/local/bin/weekly-report"
schedule = "weekly"
weekday  = 1
window   = "morning"
```

Override the default window bounds in `summond.toml`:

```toml
[windows]
morning = { start_hour = 6, end_hour = 10 }
```

## File watch trigger

Run a job when files change instead of on a schedule:

```toml
[jobs.on-config-change]
command = "/usr/local/bin/reload"
trigger = "on_change"

[jobs.on-config-change.watch]
paths            = ["./config.json"]
throttle_seconds = 2   # debounce delay; defaults to 2
```

The flat keys `watch_paths` and `throttle_interval_seconds` are also accepted for backwards compatibility.

The job receives a `SUMMOND_CHANGED_PATHS` environment variable — a colon-separated list of entries from `watch_paths` that changed since the last successful run. Note that these are the paths you listed in `watch_paths`, so a watched directory appears as the directory itself, not the individual file that changed within it. On the first execution (no prior baseline), all `watch_paths` are included.

```sh
# shell_command example
for path in ${SUMMOND_CHANGED_PATHS//:/ }; do
  reload "$path"
done
```

## USB attach trigger

Run a job whenever a specific USB device is plugged in — for example, reapplying a webcam setting that doesn't persist across unplug/replug:

```toml
[jobs.brio-zoom]
shell_command = "uvc-util -I 0 -s zoom-abs=150"
trigger       = "on_usb_attach"

[jobs.brio-zoom.usb]
vendor_id  = "0x046d"   # Logitech
product_id = "0x085e"   # Brio
```

`vendor_id` and `product_id` identify the device and are both required. They can be hex strings (`"0x046d"`), TOML hex integers (`0x046d`), or decimal (`1133`). Find them with `system_profiler SPUSBDataType`, `ioreg -p IOUSB -l`, or `uvc-util --list-devices`.

Under the hood this renders a launchd `LaunchEvents` > `com.apple.iokit.matching` subscription, so launchd itself watches for the device — no summond process stays resident. The trigger fires whenever the device enumerates, including behind a hub or dock. Two things to know:

- If the device is already attached when the job loads (e.g. after a reboot), the matching event fires once at load. For idempotent device-setup commands this is usually what you want.
- There is no `on_usb_detach`: launchd only delivers IOKit *matching* (arrival) events, not termination events, and watching for removal would require a resident daemon. To react to an unmounted volume, watch its mount point with the `on_change` trigger instead.

Like `on_change`, the trigger debounces via `throttle_interval_seconds` (default 2) in case the device enumerates more than once on attach.

## Interactive TUI

```sh
summond tui
```

Opens a two-pane terminal UI. The left pane lists all jobs; the right pane shows details for the selected job across three tabs:

- **State** — schedule, command, last run status, execution counts, and file paths
- **Plist** — the generated launchd plist XML
- **Logs** — last 500 lines of stdout and stderr

Keybindings:

| Key | Action |
|---|---|
| `↑` / `↓` or `j` / `k` | Navigate job list |
| `tab` / `shift+tab` | Cycle tabs forward / backward |
| scroll wheel | Scroll right pane |
| `X` | Run selected job immediately in the foreground |
| `r` | Force refresh |
| `q` / `ctrl+c` | Quit |

Shell commands and plist XML are syntax-highlighted if [`bat`](https://github.com/sharkdp/bat) is installed, with graceful fallback to plain text.

## Logs, status, and execution history

```sh
summond list                  # all jobs with last-run status
summond logs cleanup          # output from the last execution
summond logs cleanup -f       # stream new output live
summond exec cleanup          # run immediately in the foreground
summond kill cleanup          # stop a running job and reconcile its state
summond state cleanup         # full execution history as JSON
```

Stdout and stderr go to `~/Library/Application Support/summond/logs/` and are rotated by macOS's native `newsyslog`. No third-party log management needed.

## Agents and daemons

Jobs run as LaunchAgents (per-user) by default. Set `target = "daemon"` for system-wide LaunchDaemons — summond will prompt for `sudo` when needed.

## Retry and backoff

Jobs that exit non-zero can be automatically retried with exponential backoff:

```toml
[jobs.flaky-api-sync]
command  = "/usr/local/bin/sync"
schedule = "hourly"

[jobs.flaky-api-sync.retry]
attempts         = 3   # retries after the initial attempt
delay_seconds    = 5   # wait before first retry (default 1)
max_delay_seconds = 60  # cap the doubling delay (0 = no cap)
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

## Concurrency

By default, a job's next scheduled trigger is skipped while a previous run of that job is still in progress:

```toml
[jobs.long-running-sync]
command  = "/usr/local/bin/sync"
schedule = "interval"
interval_minutes = 5
```

If `sync` takes longer than 5 minutes, the overlapping trigger is skipped (logged to stderr) rather than starting a second instance. Set `concurrency = true` to allow overlapping runs instead:

```toml
[jobs.long-running-sync]
command     = "/usr/local/bin/sync"
schedule    = "interval"
interval_minutes = 5
concurrency = true
```

Or via `summond add --concurrency`.

## Shared environment

```sh
summond env set API_KEY=secret   # injected into every job at runtime
```

Per-job `[jobs.name.env]` values are merged on top.
