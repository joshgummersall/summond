package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/standardlabs/summond/internal/job"
)

func (a *App) newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "summond",
		Short:         "Manage friendly launchd jobs",
		SilenceUsage:  true,
		SilenceErrors: true,
		Long:          "summond manages friendly launchd jobs with native LaunchAgents and LaunchDaemons.",
	}
	verbosity := root.PersistentFlags().CountP("verbose", "v", "increase verbosity")
	root.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		a.configureLogger(*verbosity)
	}
	root.CompletionOptions.DisableDefaultCmd = true

	root.AddCommand(
		a.newInstallCommand(),
		a.newUninstallCommand(),
		a.newApplyCommand(),
		a.newAddCommand(),
		a.newRemoveCommand(),
		a.newListCommand(),
		a.newStateCommand(),
		a.newPlistCommand(),
		a.newLogsCommand(),
		a.newExecCommand(),
		a.newEnvCommand(),
		a.newCDCommand(),
		a.newVersionCommand(),
	)
	return root
}

func (a *App) newInstallCommand() *cobra.Command {
	var overwrite bool
	var skipNewsyslog bool
	var yes bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Scaffold config and newsyslog setup",
		Long: `Set up summond in the current directory and configure system log rotation.

Install creates the following files and directories:
  - summond.toml      starter config in the current directory (skipped if it already exists, unless --overwrite)
  - ~/.summond/       agent job state directory
  - /var/db/summond/  daemon job state directory
  - newsyslog config  log rotation config installed to /etc/newsyslog.d/ (requires sudo)

Run 'summond apply' after install to load jobs from summond.toml.
Run 'summond uninstall' to remove all managed jobs and state.`,
		Example: `  summond install
  summond install --overwrite
  summond install --skip-newsyslog`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runInstall(installOptions{
				overwrite:     overwrite,
				skipNewsyslog: skipNewsyslog,
				yes:           yes,
			})
		},
	}
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "overwrite generated files if they already exist (summond.toml, newsyslog config)")
	cmd.Flags().BoolVar(&skipNewsyslog, "skip-newsyslog", false, "skip generating and installing the newsyslog log rotation config")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation prompt")
	return cmd
}

func (a *App) newUninstallCommand() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove all Summond-managed jobs and setup",
		Long: `Remove all Summond-managed jobs, plists, logs, state directories, and the newsyslog config.

This removes every job managed by summond (both agents and daemons), boots them out of
launchd, deletes their plists and log files, and removes the summond state directories.
Daemon jobs and system-owned files may require sudo.

This is a destructive operation. Run 'summond list' first to see what will be removed.`,
		Example: `  summond uninstall
  summond uninstall --yes`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runUninstall(uninstallOptions{yes: yes})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation prompt")
	return cmd
}

func (a *App) newApplyCommand() *cobra.Command {
	var prune bool
	cmd := &cobra.Command{
		Use:   "apply [file]",
		Short: "Apply jobs from a TOML file",
		Long: `Apply jobs from a TOML config file, creating or updating launchd jobs as needed.

If [file] is omitted, reads ./summond.toml.

Jobs are identified by their name within a group. The group defaults to a hash of the
config file path, or the 'group' key at the top of the config file. Apply only manages
jobs in groups present in the config — jobs in other groups are never pruned.

If the config file removes a job that summond previously managed (within the same group),
apply will prompt to prune it. Use --prune to remove orphaned jobs without prompting.`,
		Example: `  summond apply
  summond apply path/to/jobs.toml
  summond apply --prune`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath := "summond.toml"
			if len(args) == 1 {
				filePath = args[0]
			}
			return a.runApply(applyOptions{
				filePath: filePath,
				prune:    prune,
			})
		},
	}
	cmd.Flags().BoolVar(&prune, "prune", false, "remove orphaned managed jobs (within the same group) without prompting")
	return cmd
}

func (a *App) newAddCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <target> ...",
		Short: "Add and install a managed job",
		Long:  "Add and install a managed job without editing summond.toml.\n\nRun 'summond add <target> --help' for target-specific usage.",
	}
	cmd.AddCommand(
		a.newAddTargetCommand("agent"),
		a.newAddTargetCommand("daemon"),
	)
	return cmd
}

func (a *App) newAddTargetCommand(target string) *cobra.Command {
	var schedule string
	var workingDir string
	var hour int
	var minute int
	var weekday int
	var intervalMinutes int
	targetLabel := "LaunchAgent"
	exampleSchedule := "login"
	exampleBinary := "/bin/echo hello --flag"
	stdinName := "my-script"
	scheduleNote := "login (agent only), hourly, daily, weekly, interval, calendar"
	if target == "daemon" {
		targetLabel = "LaunchDaemon"
		exampleSchedule = "boot"
		exampleBinary = "/usr/local/bin/task"
		stdinName = "my-daemon-script"
		scheduleNote = "boot (daemon only), hourly, daily, weekly, interval, calendar"
	}
	loginOrBootLine := "  login     runs at user login (agent only)\n"
	if target == "daemon" {
		loginOrBootLine = "  boot      runs at system boot (daemon only)\n"
	}
	cmd := &cobra.Command{
		Use:   fmt.Sprintf("%s <name> -- <command> [args]", target),
		Short: fmt.Sprintf("Add a %s job", targetLabel),
		Long: fmt.Sprintf(`Add and install a %s job without editing summond.toml.

The command to run must be provided either after -- or as a shell script on stdin:

  summond add %s <name> --schedule <kind> -- /path/to/binary [args]
  echo 'script' | summond add %s <name> --schedule <kind>
  summond add %s <name> --schedule <kind> <<'EOF'
  shell commands here
  EOF

Schedule kinds (%s):
%s  hourly    runs once per hour; use --minute to set the minute (0-59, auto-seeded if omitted)
  daily     runs once per day; use --hour (0-23) and --minute (0-59) (auto-seeded if omitted)
  weekly    runs once per week; use --weekday (0-7, 0=Sun), --hour, --minute (auto-seeded if omitted)
  interval  runs every N minutes; requires --interval-minutes
  calendar  flexible calendar schedule; use --weekday, --hour, --minute`, targetLabel, target, target, target, scheduleNote, loginOrBootLine),
		Args: cobra.MinimumNArgs(1),
		Example: fmt.Sprintf(`  summond add %s my-job --schedule %s -- %s
  summond add %s my-job --schedule hourly --minute 30 -- /path/to/binary
  summond add %s my-job --schedule daily --hour 2 --minute 0 -- /path/to/binary
  summond add %s my-job --schedule interval --interval-minutes 15 -- /path/to/binary
  summond add %s %s --schedule daily <<'EOF'
  echo hi
  EOF`,
			target, exampleSchedule, exampleBinary,
			target, target, target,
			target, stdinName),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := addOptions{
				name:            args[0],
				schedule:        schedule,
				workingDir:      workingDir,
				hour:            hour,
				hourSet:         cmd.Flags().Changed("hour"),
				minute:          minute,
				minuteSet:       cmd.Flags().Changed("minute"),
				weekday:         weekday,
				weekdaySet:      cmd.Flags().Changed("weekday"),
				intervalMinutes: intervalMinutes,
				intervalSet:     cmd.Flags().Changed("interval-minutes"),
			}
			if target == "daemon" {
				opts.target = job.TargetDaemon
			} else {
				opts.target = job.TargetAgent
			}
			if len(args) > 1 {
				opts.commandArgs = append([]string(nil), args[1:]...)
			} else if stdinHasData(a.stdin) {
				data, err := io.ReadAll(a.stdin)
				if err != nil {
					return fmt.Errorf("read shell command from stdin: %w", err)
				}
				opts.stdinScript = strings.TrimRight(string(data), "\n")
			}
			return a.runAddTarget(opts)
		},
	}
	cmd.Flags().StringVar(&schedule, "schedule", "", fmt.Sprintf("schedule kind (%s)", scheduleNote))
	cmd.Flags().StringVar(&workingDir, "working-dir", "", "working directory (must be an absolute path; defaults to current directory)")
	cmd.Flags().IntVar(&hour, "hour", 0, "hour for daily/weekly/calendar schedules (0-23)")
	cmd.Flags().IntVar(&minute, "minute", 0, "minute for hourly/daily/weekly/calendar schedules (0-59)")
	cmd.Flags().IntVar(&weekday, "weekday", 0, "weekday for weekly/calendar schedules (0-7, 0 and 7 = Sunday)")
	cmd.Flags().IntVar(&intervalMinutes, "interval-minutes", 0, "run interval in minutes; required for --schedule interval")
	return cmd
}

func (a *App) newRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a managed job and all state",
		Long: `Remove a managed job, booting it out of launchd and deleting its plist, logs, and persisted state.

Daemon jobs are owned by root and may prompt for sudo to remove system-owned files.
Use 'summond list' to see job names.`,
		Example: `  summond remove my-job`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runRemove(removeOptions{name: args[0]})
		},
	}
}

func (a *App) newListCommand() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List managed jobs",
		Long: `List all managed jobs with their target, schedule, and last run status.

Output columns:
  NAME      job name (or group.name if the job belongs to a group)
  TARGET    agent or daemon
  SCHEDULE  schedule kind and parameters, or trigger type
  STATUS    never / running / ok <timestamp> / exit <code> <timestamp>

Use --json to output a JSON array of job objects instead of the table.`,
		Example: `  summond list
  summond list --json
  summond list --json | jq '.[].name'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runList(listOptions{json: jsonOutput})
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output jobs as a JSON array")
	return cmd
}

func (a *App) newStateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "state <name>",
		Short: "Print the job state JSON",
		Long: `Print the managed job's persisted state as JSON.

The state file contains the job spec, schedule, runtime paths, and execution history
(last run time, exit code, recent runs). Useful for inspecting job configuration or
scripting based on run history.`,
		Example: `  summond state my-job
  summond state my-job | jq '.last_exit_code'`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runState(namedJobOptions{name: args[0]})
		},
	}
}

func (a *App) newPlistCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "plist <name>",
		Short: "Print the job plist file",
		Long: `Print the managed job's installed launchd plist file.

Useful for verifying the generated plist or passing it to launchctl directly.
Daemon plists are installed to /Library/LaunchDaemons/; agent plists to ~/Library/LaunchAgents/.`,
		Example: `  summond plist my-job`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runPlist(namedJobOptions{name: args[0]})
		},
	}
}

func (a *App) newLogsCommand() *cobra.Command {
	var lineCount int
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs <name>",
		Short: "Print job logs",
		Long: `Print the last N lines of a managed job's stdout and stderr log files.

Both stdout and stderr are printed in order. Use -f to stream new output as it is appended,
similar to 'tail -f'. Press Ctrl-C to stop following.

Log files are managed by newsyslog and rotated automatically. If the job has never run,
the log files may not exist yet.`,
		Example: `  summond logs my-job
  summond logs my-job -n 100
  summond logs my-job -f`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runLogs(logsOptions{name: args[0], lineCount: lineCount, follow: follow})
		},
	}
	cmd.Flags().IntVarP(&lineCount, "lines", "n", 40, "number of lines to print per log file")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "stream appended output (like tail -f); press Ctrl-C to stop")
	return cmd
}

func (a *App) newExecCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "exec <name>",
		Short: "Run a managed job immediately",
		Long: `Run a managed job immediately in the foreground and record its execution result.

The job runs with the same command, working directory, and environment as it would on
its normal schedule. stdout and stderr are written to the terminal. The execution is
recorded in the job's state (last run time, exit code, run count).

Exits with the job's exit code. A non-zero exit means the job itself failed, not summond.`,
		Example: `  summond exec my-job`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runExec(namedJobOptions{name: args[0]})
		},
	}
}

func (a *App) newEnvCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Manage the shared job environment",
		Long: `Manage environment variables shared across all managed jobs.

Variables are stored in a JSON file and injected into every job at runtime before the
job command runs. Use 'set', 'get', and 'list' to manipulate the environment non-interactively.`,
		Example: `  summond env set API_KEY=abc123
  summond env get API_KEY
  summond env list`,
	}
	cmd.AddCommand(
		a.newEnvSetCommand(),
		a.newEnvGetCommand(),
		a.newEnvListCommand(),
	)
	return cmd
}

func (a *App) newEnvSetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "set KEY=VALUE",
		Short: "Set an environment variable",
		Long: `Add or update an environment variable in the shared env file.

If KEY already exists its value is updated in place. Otherwise a new entry is appended.
Keys must match [A-Za-z_][A-Za-z0-9_]*.`,
		Example: `  summond env set API_KEY=abc123
  summond env set PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value, err := parseKeyValue(args[0])
			if err != nil {
				return err
			}
			return a.runEnvSet(envSetOptions{key: key, value: value})
		},
	}
}

func (a *App) newEnvGetCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "get KEY",
		Short:   "Print the value of an environment variable",
		Example: `  summond env get API_KEY`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runEnvGet(envGetOptions{key: args[0]})
		},
	}
}

func (a *App) newEnvListCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List all environment variables",
		Example: `  summond env list`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runEnvList()
		},
	}
}

func (a *App) newCDCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "cd <name>",
		Short: "Open a shell in the job state directory",
		Long: `Open a subshell in the job's state directory, or print a cd command for eval.

When stdout is a terminal (interactive), spawns a new shell ($SHELL or /bin/zsh) with
its working directory set to the job's state directory. Exit the shell to return.

When stdout is not a terminal (e.g. inside $(...) or eval), prints a cd command instead:

  eval $(summond cd my-job)

The job state directory contains the job's metadata JSON, log symlinks, and runtime files.`,
		Example: `  summond cd my-job
  eval $(summond cd my-job)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runCd(namedJobOptions{name: args[0]})
		},
	}
}

func (a *App) newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the CLI version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Fprintf(a.stdout, "summond %s\n", version)
			return err
		},
	}
}
