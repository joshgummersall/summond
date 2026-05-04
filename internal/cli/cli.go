package cli

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/joshgummersall/summond/internal/bootstrap"
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
	daemonStore  *state.Store
	runner       launchd.Runner
	boot         *bootstrap.Manager
	priv         privilegedOperator
	logger       *slog.Logger
	promptReader *bufio.Reader
	geteuid      func() int
}

type privilegedOperator interface {
	InstallDaemonSpecWithSudo(dirs []string, runtimeSource string, runtimeDest string, plistSource string, plistDest string, metadataSource string, metadataDest string) error
	RemoveDaemonArtifactsWithSudo(plistPaths []string, cleanupPaths []string, extraPaths []string) error
	RemovePathWithSudo(path string) error
	BootoutDaemonWithSudo(plistPath string) error
	WriteFileWithSudo(src, dst string) error
}

type osPrivilegedOperator struct{}

func NewApp(stdin io.Reader, stdout io.Writer, store *state.Store, daemonStore *state.Store, runner launchd.Runner, boot *bootstrap.Manager) *App {
	return &App{
		stdin:       stdin,
		stdout:      stdout,
		stderr:      io.Discard,
		store:       store,
		daemonStore: daemonStore,
		runner:      runner,
		boot:        boot,
		priv:        osPrivilegedOperator{},
		geteuid:     os.Geteuid,
	}
}

// Note: main.go entrypoint
func Run(args []string, stdout io.Writer) error {
	paths, err := state.DiscoverPathSet()
	if err != nil {
		return err
	}
	app := NewApp(
		os.Stdin,
		stdout,
		state.NewStore(paths.Agent),
		state.NewStore(paths.Daemon),
		launchd.LaunchCtl{},
		bootstrap.NewManager(paths, bootstrap.OSInstaller{}),
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
		a.newKillCommand(),
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

type managedSpec struct {
	spec  job.Spec
	store *state.Store
}

func (a *App) storeForTarget(target job.Target) *state.Store {
	if target == job.TargetDaemon {
		return a.daemonStore
	}
	return a.store
}

func (a *App) listManagedJobs() ([]managedSpec, error) {
	specs := []managedSpec{}
	agentSpecs, err := a.store.List()
	if err != nil {
		return nil, err
	}
	for _, spec := range agentSpecs {
		specs = append(specs, managedSpec{spec: spec, store: a.store})
	}
	daemonSpecs, err := a.daemonStore.List()
	if err != nil {
		return nil, err
	}
	for _, spec := range daemonSpecs {
		specs = append(specs, managedSpec{spec: spec, store: a.daemonStore})
	}
	sort.Slice(specs, func(i, j int) bool {
		if specs[i].spec.Group == specs[j].spec.Group && specs[i].spec.Name == specs[j].spec.Name {
			return specs[i].spec.Target < specs[j].spec.Target
		}
		if specs[i].spec.Group != specs[j].spec.Group {
			return specs[i].spec.Group < specs[j].spec.Group
		}
		return specs[i].spec.Name < specs[j].spec.Name
	})
	return specs, nil
}

func (a *App) loadManagedSpec(name string) (managedSpec, error) {
	specs, err := a.listManagedJobs()
	if err != nil {
		return managedSpec{}, err
	}
	var matches []managedSpec
	for _, managed := range specs {
		if managed.spec.Name == name || managed.spec.ManagedKey() == name {
			matches = append(matches, managed)
		}
	}
	switch len(matches) {
	case 0:
		return managedSpec{}, fmt.Errorf("read job metadata: %w", os.ErrNotExist)
	case 1:
		return matches[0], nil
	default:
		return managedSpec{}, fmt.Errorf("duplicate managed job name %q across groups or targets; use a unique group", name)
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
		return fmt.Sprintf("daily at %02d:%02d", schedule.Hour, schedule.Minute)
	case job.ScheduleWeekly:
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

func runSudoScript(script string, args ...string) error {
	var stderr bytes.Buffer
	commandArgs := append([]string{"/bin/sh", "-c", script, "summond-sudo"}, args...)
	cmd := exec.Command("sudo", commandArgs...)
	cmd.Stderr = &stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("%w: %s", err, stderr.String())
		}
		return err
	}
	return nil
}

func (osPrivilegedOperator) InstallDaemonSpecWithSudo(dirs []string, runtimeSource string, runtimeDest string, plistSource string, plistDest string, metadataSource string, metadataDest string) error {
	script := `
set -e
dir_count="$1"
shift 1
i=0
while [ "$i" -lt "$dir_count" ]; do
  install -d -m 0755 "$1"
  shift
  i=$((i + 1))
done
runtime_source="$1"
runtime_dest="$2"
plist_source="$3"
plist_dest="$4"
metadata_source="$5"
metadata_dest="$6"
install -m 0755 "$runtime_source" "$runtime_dest"
install -m 0644 "$plist_source" "$plist_dest"
install -m 0644 "$metadata_source" "$metadata_dest"
launchctl bootout system "$plist_dest" >/dev/null 2>&1 || true
launchctl bootstrap system "$plist_dest"
`
	args := []string{fmt.Sprintf("%d", len(dirs))}
	args = append(args, dirs...)
	args = append(args, runtimeSource, runtimeDest, plistSource, plistDest, metadataSource, metadataDest)
	return runSudoScript(script, args...)
}

func (osPrivilegedOperator) RemoveDaemonArtifactsWithSudo(plistPaths []string, cleanupPaths []string, extraPaths []string) error {
	script := `
plist_count="$1"
cleanup_count="$2"
extra_count="$3"
shift 3
i=0
while [ "$i" -lt "$plist_count" ]; do
  launchctl bootout system "$1" >/dev/null 2>&1 || true
  rm -rf "$1"
  shift
  i=$((i + 1))
done
i=0
while [ "$i" -lt "$cleanup_count" ]; do
  rm -rf "$1"
  shift
  i=$((i + 1))
done
i=0
while [ "$i" -lt "$extra_count" ]; do
  rm -rf "$1"
  shift
  i=$((i + 1))
done
`
	args := []string{fmt.Sprintf("%d", len(plistPaths)), fmt.Sprintf("%d", len(cleanupPaths)), fmt.Sprintf("%d", len(extraPaths))}
	args = append(args, plistPaths...)
	args = append(args, cleanupPaths...)
	args = append(args, extraPaths...)
	return runSudoScript(script, args...)
}

func (osPrivilegedOperator) RemovePathWithSudo(path string) error {
	return runSudoScript(`rm -rf "$1"`, path)
}

func (osPrivilegedOperator) BootoutDaemonWithSudo(plistPath string) error {
	return runSudoScript(`launchctl bootout system "$1"`, plistPath)
}

func (osPrivilegedOperator) WriteFileWithSudo(src, dst string) error {
	return runSudoScript(`install -m 0644 "$1" "$2"`, src, dst)
}
