package cli

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/joshgummersall/summond/internal/job"
	"github.com/joshgummersall/summond/internal/launchd"
	"github.com/joshgummersall/summond/internal/state"
	"github.com/spf13/cobra"
)

var version = "dev"

const shellPreamble = "set -euo pipefail\n"

type ExitError struct {
	Code    int
	Message string
}

func (e ExitError) Error() string {
	return e.Message
}

type App struct {
	stdin        io.Reader
	stdout       io.Writer
	stderr       io.Writer
	store        *state.Store
	runner       launchd.Runner
	logger       *slog.Logger
	promptReader *bufio.Reader
	geteuid      func() int
}

// NewApp builds an App for one scope. store and runner must belong to the
// scope that the effective UID selects; see scopeTarget.
func NewApp(stdin io.Reader, stdout io.Writer, store *state.Store, runner launchd.Runner) *App {
	return &App{
		stdin:   stdin,
		stdout:  stdout,
		stderr:  io.Discard,
		store:   store,
		runner:  runner,
		geteuid: os.Geteuid,
	}
}

func Run(args []string, stdout io.Writer) error {
	euid := os.Geteuid()
	paths, err := state.DiscoverPaths(euid)
	if err != nil {
		return err
	}
	app := NewApp(
		os.Stdin,
		stdout,
		state.NewStore(paths),
		launchd.LaunchCtl{Domain: launchd.DomainFor(euid)},
	)
	app.stderr = os.Stderr
	return app.run(args)
}

func (a *App) run(args []string) error {
	a.promptReader = nil
	a.configureLogger(0)
	root := a.newRootCommand()
	root.SetArgs(args)
	root.SetIn(a.stdin)
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	return root.Execute()
}

func (a *App) newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "summond",
		Short:         "Manage friendly launchd jobs",
		SilenceUsage:  true,
		SilenceErrors: true,
		Long: `summond manages friendly launchd jobs with native LaunchAgents and LaunchDaemons.

summond manages agent jobs for the current user. sudo summond manages daemon jobs.
Every command acts on one scope only and never prompts for sudo on its own.

Exit codes:
  0   success
  1   error (bad arguments, job not found, command failed, etc.)
  N   exec: exits with the job's own exit code (non-zero means the job failed, not summond)
  N   cd: exits with the shell's exit code when used interactively`,
	}

	verbosity := root.PersistentFlags().CountP("verbose", "v", "increase verbosity")
	root.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		a.configureLogger(*verbosity)
	}
	root.CompletionOptions.DisableDefaultCmd = true

	root.AddCommand(
		a.newUninstallCommand(),
		a.newApplyCommand(),
		a.newAddCommand(),
		a.newRemoveCommand(),
		a.newListCommand(),
		a.newStateCommand(),
		a.newPlistCommand(),
		a.newLogsCommand(),
		a.newExecCommand(),
		a.newKillCommand(),
		a.newRestartCommand(),
		a.newEnvCommand(),
		a.newCDCommand(),
		a.newVersionCommand(),
		a.newTUICommand(),
	)

	return root
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

func (a *App) isLoaded(spec job.Spec) (bool, error) {
	_, err := a.runner.Print(spec)
	if err == nil {
		return true, nil
	}
	if isMissingServiceError(err) {
		return false, nil
	}
	return false, err
}

func isMissingServiceError(err error) bool {
	text := err.Error()
	return strings.Contains(text, "Could not find service") ||
		strings.Contains(text, "service not found") ||
		strings.Contains(text, "No such process") ||
		strings.Contains(text, "not found in domain")
}

func (a *App) verifyLoadedJob(spec job.Spec) error {
	text, err := a.runner.Print(spec)
	if err != nil {
		return fmt.Errorf("launchctl print: %w", err)
	}
	if spec.Checksum != "" && strings.Contains(text, spec.Checksum) {
		return nil
	}
	expected := []string{spec.Label, spec.StdoutPath, spec.StderrPath}
	expected = append(expected, spec.WatchPaths...)
	expected = append(expected, spec.RuntimeBinaryPath, "exec", spec.Name)
	for _, needle := range expected {
		if needle == "" {
			continue
		}
		if !strings.Contains(text, needle) {
			return fmt.Errorf("missing %q in loaded definition", needle)
		}
	}
	return nil
}

// prepareScope creates what this scope's jobs need on first use: the env
// file and the runtime copy of summond that launchd runs. It returns the
// runtime binary path. There is no separate install step.
func (a *App) prepareScope() (string, error) {
	if _, err := a.ensureEnvFile(); err != nil {
		return "", err
	}
	runtimeSource, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve current executable: %w", err)
	}
	return a.store.PrepareRuntimeBinary(runtimeSource)
}

// scopeTarget returns the job target this process manages: daemons as root,
// agents otherwise.
func (a *App) scopeTarget() job.Target {
	if a.geteuid() == 0 {
		return job.TargetDaemon
	}
	return job.TargetAgent
}

// scopeMismatchError explains how to manage a job whose target belongs to
// the other scope.
func scopeMismatchError(target job.Target, command string) error {
	if target == job.TargetDaemon {
		return fmt.Errorf("daemon jobs are managed as root; run: sudo summond %s", command)
	}
	return fmt.Errorf("agent jobs are managed without sudo; run: summond %s", command)
}

func (a *App) listManagedJobs() ([]job.Spec, error) {
	specs, err := a.store.List()
	if err != nil {
		return nil, err
	}
	sort.Slice(specs, func(i, j int) bool {
		if specs[i].Group != specs[j].Group {
			return specs[i].Group < specs[j].Group
		}
		return specs[i].Name < specs[j].Name
	})
	return specs, nil
}

func (a *App) loadManagedSpec(name string) (job.Spec, error) {
	specs, err := a.listManagedJobs()
	if err != nil {
		return job.Spec{}, err
	}
	var matches []job.Spec
	for _, spec := range specs {
		if spec.Name == name || spec.ManagedKey() == name {
			matches = append(matches, spec)
		}
	}
	switch len(matches) {
	case 0:
		return job.Spec{}, fmt.Errorf("read job metadata: %w", os.ErrNotExist)
	case 1:
		return matches[0], nil
	default:
		return job.Spec{}, fmt.Errorf("duplicate managed job name %q across groups; use a unique group", name)
	}
}

func (a *App) configureLogger(verbosity int) {
	if a.stderr == nil {
		a.stderr = io.Discard
	}
	level := slog.LevelWarn
	switch {
	case verbosity >= 2:
		level = slog.LevelDebug
	case verbosity == 1:
		level = slog.LevelInfo
	}
	a.logger = slog.New(slog.NewTextHandler(a.stderr, &slog.HandlerOptions{Level: level}))
}

func describeSchedule(schedule job.Schedule) string {
	switch schedule.Kind {
	case job.ScheduleHourly:
		return fmt.Sprintf("hourly at minute %d", schedule.Minute)
	case job.ScheduleDaily:
		if schedule.Window != "" {
			return fmt.Sprintf("daily at %02d:%02d (%s window)", schedule.Hour, schedule.Minute, schedule.Window)
		}
		return fmt.Sprintf("daily at %02d:%02d", schedule.Hour, schedule.Minute)
	case job.ScheduleWeekly:
		if schedule.Window != "" {
			return fmt.Sprintf("weekly weekday %d at %02d:%02d (%s window)", schedule.Weekday, schedule.Hour, schedule.Minute, schedule.Window)
		}
		return fmt.Sprintf("weekly weekday %d at %02d:%02d", schedule.Weekday, schedule.Hour, schedule.Minute)
	case job.ScheduleLogin:
		return "login"
	case job.ScheduleBoot:
		return "boot"
	case job.ScheduleInterval:
		return fmt.Sprintf("every %d minute(s)", schedule.IntervalMinutes)
	case job.ScheduleCalendar:
		var parts []string
		if schedule.Month > 0 {
			parts = append(parts, fmt.Sprintf("month=%d", schedule.Month))
		}
		if schedule.Day > 0 {
			parts = append(parts, fmt.Sprintf("day=%d", schedule.Day))
		}
		if schedule.Weekday > 0 {
			parts = append(parts, fmt.Sprintf("weekday=%d", schedule.Weekday))
		}
		parts = append(parts, fmt.Sprintf("time=%02d:%02d", schedule.Hour, schedule.Minute))
		return "calendar " + strings.Join(parts, " ")
	default:
		return string(schedule.Kind)
	}
}

func describeTriggerOrSchedule(spec job.Spec) string {
	if spec.Trigger == job.TriggerOnChange {
		return "on_change"
	}
	if spec.Trigger == job.TriggerUSBAttach {
		return fmt.Sprintf("on_usb_attach 0x%04x:0x%04x", spec.USBVendorID, spec.USBProductID)
	}
	return describeSchedule(spec.Schedule)
}

func describeLastRunStatus(spec job.Spec) string {
	if spec.LastStartedAt == nil {
		return "never"
	}
	if spec.LastFinishedAt == nil {
		return "running"
	}
	when := formatTimestamp(*spec.LastFinishedAt)
	if spec.LastExitCode != nil && *spec.LastExitCode == 0 && spec.LastError == "" {
		return "ok " + when
	}
	if spec.LastExitCode != nil {
		return fmt.Sprintf("exit %d %s", *spec.LastExitCode, when)
	}
	return "failed " + when
}

func formatTimestamp(ts time.Time) string {
	return ts.In(time.Local).Format("2006-01-02 15:04:05")
}

func appendIfMissing(values []string, value string) []string {
	if slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}
