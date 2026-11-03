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

type addOptions struct {
	name                 string
	schedule             string
	workingDir           string
	hour                 int
	hourSet              bool
	minute               int
	minuteSet            bool
	weekday              int
	weekdaySet           bool
	window               string
	intervalMinutes      int
	intervalSet          bool
	abandonProcessGroup  bool
	concurrency          bool
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

func (a *App) newAddCommand() *cobra.Command {
	var schedule string
	var workingDir string
	var hour int
	var minute int
	var weekday int
	var window string
	var intervalMinutes int
	var abandonProcessGroup bool
	var concurrency bool
	var retryAttempts int
	var retryDelaySeconds int
	var retryMaxDelaySeconds int
	var dryRun bool
	scheduleNote := "login (agents only), boot (daemons only), hourly, daily, weekly, interval, calendar"
	cmd := &cobra.Command{
		Use:   "add <name> -- <command> [args]",
		Short: "Add and install a managed job",
		Long: `Add and install a managed job without editing summond.toml.

summond adds a LaunchAgent for the current user. sudo summond adds a LaunchDaemon.

The command to run must be provided either after -- or as a shell script on stdin:

  summond add <name> --schedule <kind> -- /path/to/binary [args]
  echo 'script' | summond add <name> --schedule <kind>
  summond add <name> --schedule <kind> <<'EOF'
  shell commands here
  EOF

Schedule kinds:
  login     runs at user login (agents only)
  boot      runs at system boot (daemons only)
  hourly    runs once per hour; use --minute to set the minute (0-59, auto-seeded if omitted)
  daily     runs once per day; use --hour (0-23) and --minute (0-59) (auto-seeded if omitted)
  weekly    runs once per week; use --weekday (0-7, 0=Sun), --hour, --minute (auto-seeded if omitted)
  interval  runs every N minutes; requires --interval-minutes
  calendar  flexible calendar schedule; use --weekday, --hour, --minute

For daily/weekly schedules, --window constrains the auto-seeded hour to a
time-of-day range instead of the full day: morning (05:00-11:59), afternoon
(12:00-16:59), or evening (17:00-21:59). Ignored if --hour is also set.`,
		Args: cobra.MinimumNArgs(1),
		Example: `  summond add my-job --schedule login -- /bin/echo hello --flag
  summond add my-job --schedule hourly --minute 30 -- /path/to/binary
  summond add my-job --schedule daily --hour 2 --minute 0 -- /path/to/binary
  summond add my-job --schedule interval --interval-minutes 15 -- /path/to/binary
  sudo summond add my-daemon --schedule boot -- /usr/local/bin/task
  summond add my-script --schedule daily <<'EOF'
  echo hi
  EOF`,
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
				window:               window,
				intervalMinutes:      intervalMinutes,
				intervalSet:          cmd.Flags().Changed("interval-minutes"),
				abandonProcessGroup:  abandonProcessGroup,
				concurrency:          concurrency,
				retryAttempts:        retryAttempts,
				retryAttemptsSet:     cmd.Flags().Changed("retry-attempts"),
				retryDelaySeconds:    retryDelaySeconds,
				retryDelaySet:        cmd.Flags().Changed("retry-delay-seconds"),
				retryMaxDelaySeconds: retryMaxDelaySeconds,
				retryMaxDelaySet:     cmd.Flags().Changed("retry-max-delay-seconds"),
				dryRun:               dryRun,
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
			target := a.scopeTarget()
			a.logger.Debug("add start", "target", target, "name", opts.name)
			if opts.schedule == "" {
				return errors.New("add requires --schedule")
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
				Target:              target,
				WorkingDir:          opts.workingDir,
				AbandonProcessGroup: opts.abandonProcessGroup,
				Concurrency:         opts.concurrency,
				Schedule: job.Schedule{
					Kind:   job.ScheduleKind(opts.schedule),
					Window: job.WindowKind(opts.window),
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
			spec.EnvironmentFilePath = a.store.EnvFilePath()
			if opts.dryRun {
				return a.printAddPlan(spec)
			}
			runtimePath, err := a.prepareScope()
			if err != nil {
				return err
			}
			spec.RuntimeBinaryPath = runtimePath
			if _, err := a.applySpec(spec); err != nil {
				return err
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
	cmd.Flags().StringVar(&window, "window", "", "constrain the auto-seeded hour for daily/weekly schedules to a window: morning, afternoon, evening")
	cmd.Flags().IntVar(&intervalMinutes, "interval-minutes", 0, "run interval in minutes; required for --schedule interval")
	cmd.Flags().BoolVar(&abandonProcessGroup, "abandon-process-group", false, "allow child processes to continue after the job exits")
	cmd.Flags().BoolVar(&concurrency, "concurrency", false, "allow overlapping runs instead of skipping a trigger while a previous run is still in progress")
	cmd.Flags().IntVar(&retryAttempts, "retry-attempts", 0, "number of retries after initial failure (0 = no retry)")
	cmd.Flags().IntVar(&retryDelaySeconds, "retry-delay-seconds", 0, "initial delay in seconds before first retry (default 1 when retries are enabled)")
	cmd.Flags().IntVar(&retryMaxDelaySeconds, "retry-max-delay-seconds", 0, "cap on delay between retries in seconds (0 = no cap)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what add would do without changing job state")
	return cmd
}

func (a *App) printAddPlan(spec job.Spec) error {
	spec.RuntimeBinaryPath = a.store.RuntimeBinaryPath()
	prepared, _, err := a.store.PrepareInstall(spec)
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
	if _, err := fmt.Fprintf(a.stdout, "- write metadata: %s\n", a.store.MetadataPathForSpec(prepared)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.stdout, "- create log files: %s, %s\n", prepared.StdoutPath, prepared.StderrPath); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.stdout, "- install runtime binary: %s\n", prepared.RuntimeBinaryPath); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.stdout, "- load launchd job: %s\n", prepared.Label); err != nil {
		return err
	}
	_, err = fmt.Fprintln(a.stdout, "dry run: no changes made")
	return err
}
