package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/joshgummersall/summond/internal/job"
	"github.com/spf13/cobra"
)

func stdinHasData(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	if !ok {
		return true
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice == 0
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

type addOptions struct {
	target               job.Target
	name                 string
	schedule             string
	workingDir           string
	hour                 int
	hourSet              bool
	minute               int
	minuteSet            bool
	weekday              int
	weekdaySet           bool
	intervalMinutes      int
	intervalSet          bool
	abandonProcessGroup  bool
	retryAttempts        int
	retryAttemptsSet     bool
	retryDelaySeconds    int
	retryDelaySet        bool
	retryMaxDelaySeconds int
	retryMaxDelaySet     bool
	commandArgs          []string
	stdinScript          string
	dryRun               bool
}

func (a *App) newAddTargetCommand(target string) *cobra.Command {
	var schedule string
	var workingDir string
	var hour int
	var minute int
	var weekday int
	var intervalMinutes int
	var abandonProcessGroup bool
	var retryAttempts int
	var retryDelaySeconds int
	var retryMaxDelaySeconds int
	var dryRun bool
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
				name:                 args[0],
				schedule:             schedule,
				workingDir:           workingDir,
				hour:                 hour,
				hourSet:              cmd.Flags().Changed("hour"),
				minute:               minute,
				minuteSet:            cmd.Flags().Changed("minute"),
				weekday:              weekday,
				weekdaySet:           cmd.Flags().Changed("weekday"),
				intervalMinutes:      intervalMinutes,
				intervalSet:          cmd.Flags().Changed("interval-minutes"),
				abandonProcessGroup:  abandonProcessGroup,
				retryAttempts:        retryAttempts,
				retryAttemptsSet:     cmd.Flags().Changed("retry-attempts"),
				retryDelaySeconds:    retryDelaySeconds,
				retryDelaySet:        cmd.Flags().Changed("retry-delay-seconds"),
				retryMaxDelaySeconds: retryMaxDelaySeconds,
				retryMaxDelaySet:     cmd.Flags().Changed("retry-max-delay-seconds"),
				dryRun:               dryRun,
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
			a.logger.Debug("add target start", "target", opts.target, "name", opts.name)
			if opts.schedule == "" {
				return fmt.Errorf("add %s requires --schedule", opts.target)
			}
			installed, err := a.boot.IsInstalled()
			if err != nil {
				return err
			}
			if !installed {
				return fmt.Errorf("add %s requires install to be run first", opts.target)
			}
			wd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("resolve current working directory: %w", err)
			}
			if _, err := a.loadManagedSpec(opts.name); err == nil {
				return fmt.Errorf("job %q already exists", opts.name)
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			spec := job.Spec{
				Name:                opts.name,
				Target:              opts.target,
				WorkingDir:          opts.workingDir,
				AbandonProcessGroup: opts.abandonProcessGroup,
				Schedule: job.Schedule{
					Kind: job.ScheduleKind(opts.schedule),
				},
			}
			switch {
			case len(opts.commandArgs) > 0:
				spec.Command = opts.commandArgs[0]
				spec.Args = append([]string(nil), opts.commandArgs[1:]...)
			case opts.stdinScript != "":
				spec.ShellCommand = strings.TrimRight(opts.stdinScript, "\n")
				if strings.TrimSpace(spec.ShellCommand) == "" {
					return errors.New("shell stdin produced an empty command")
				}
			default:
				return errors.New("add requires a command after -- or shell script input on stdin")
			}
			if spec.WorkingDir == "" {
				spec.WorkingDir = wd
			}
			if opts.hourSet {
				spec.Schedule.Hour = opts.hour
				spec.Schedule.HourSet = true
			}
			if opts.minuteSet {
				spec.Schedule.Minute = opts.minute
				spec.Schedule.MinuteSet = true
			}
			if opts.weekdaySet {
				spec.Schedule.Weekday = opts.weekday
				spec.Schedule.WeekdaySet = true
			}
			if opts.intervalSet {
				spec.Schedule.IntervalMinutes = opts.intervalMinutes
				spec.Schedule.IntervalSet = true
			}
			if opts.retryAttemptsSet {
				spec.RetryAttempts = opts.retryAttempts
			}
			if opts.retryDelaySet {
				spec.RetryDelaySeconds = opts.retryDelaySeconds
			}
			if opts.retryMaxDelaySet {
				spec.RetryMaxDelaySeconds = opts.retryMaxDelaySeconds
			}
			if err := spec.Normalize(); err != nil {
				return err
			}
			if opts.target == job.TargetDaemon {
				spec.EnvironmentFilePath = a.daemonStore.EnvFilePath()
			} else {
				spec.EnvironmentFilePath = a.store.EnvFilePath()
			}
			if opts.dryRun {
				return a.printAddPlan(spec)
			}
			runtimeSource, err := os.Executable()
			if err != nil {
				return fmt.Errorf("resolve current executable: %w", err)
			}
			if opts.target == job.TargetDaemon {
				spec.RuntimeBinaryPath = a.daemonStore.RuntimeBinaryPath()
				sudoApproved, sudoPrompted := false, false
				installedSpec, err := a.applyDaemonSpec(spec, runtimeSource, &sudoApproved, &sudoPrompted)
				if err != nil {
					return err
				}
				if err := a.reconcileDaemonRuntime(installedSpec); err != nil {
					return err
				}
			} else {
				agentRuntimePath, err := a.store.PrepareRuntimeBinary(runtimeSource)
				if err != nil {
					return err
				}
				spec.RuntimeBinaryPath = agentRuntimePath
				if _, err := a.applySpec(a.store, spec); err != nil {
					return err
				}
			}
			_, err = fmt.Fprintf(a.stdout, "added %s\n", spec.Name)
			return err

		},
	}
	cmd.Flags().StringVar(&schedule, "schedule", "", fmt.Sprintf("schedule kind (%s)", scheduleNote))
	cmd.Flags().StringVar(&workingDir, "working-dir", "", "working directory (must be an absolute path; defaults to current directory)")
	cmd.Flags().IntVar(&hour, "hour", 0, "hour for daily/weekly/calendar schedules (0-23)")
	cmd.Flags().IntVar(&minute, "minute", 0, "minute for hourly/daily/weekly/calendar schedules (0-59)")
	cmd.Flags().IntVar(&weekday, "weekday", 0, "weekday for weekly/calendar schedules (0-7, 0 and 7 = Sunday)")
	cmd.Flags().IntVar(&intervalMinutes, "interval-minutes", 0, "run interval in minutes; required for --schedule interval")
	cmd.Flags().BoolVar(&abandonProcessGroup, "abandon-process-group", false, "allow child processes to continue after the job exits")
	cmd.Flags().IntVar(&retryAttempts, "retry-attempts", 0, "number of retries after initial failure (0 = no retry)")
	cmd.Flags().IntVar(&retryDelaySeconds, "retry-delay-seconds", 0, "initial delay in seconds before first retry (default 1 when retries are enabled)")
	cmd.Flags().IntVar(&retryMaxDelaySeconds, "retry-max-delay-seconds", 0, "cap on delay between retries in seconds (0 = no cap)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what add would do without changing job state")
	return cmd
}

func (a *App) printAddPlan(spec job.Spec) error {
	store := a.storeForTarget(spec.Target)
	spec.RuntimeBinaryPath = store.RuntimeBinaryPath()
	prepared, _, err := store.PrepareInstall(spec)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(a.stdout, "add will:"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.stdout, "- create %s job: %s (%s)\n", prepared.Target, prepared.Name, describeTriggerOrSchedule(prepared)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.stdout, "- write plist: %s\n", prepared.PlistPath); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.stdout, "- write metadata: %s\n", store.MetadataPathForSpec(prepared)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.stdout, "- create log files: %s, %s\n", prepared.StdoutPath, prepared.StderrPath); err != nil {
		return err
	}
	if prepared.Target == job.TargetAgent {
		if _, err := fmt.Fprintf(a.stdout, "- install runtime binary: %s\n", prepared.RuntimeBinaryPath); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(a.stdout, "- load launchd job: %s\n", prepared.Label); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintln(a.stdout, "- may require sudo for daemon-owned files"); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(a.stdout, "dry run: no changes made")
	return err
}
