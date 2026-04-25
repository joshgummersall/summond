package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/joshgummersall/summond/internal/bootstrap"
	"github.com/joshgummersall/summond/internal/config"
	"github.com/joshgummersall/summond/internal/job"
	"github.com/joshgummersall/summond/internal/launchd"
	"github.com/joshgummersall/summond/internal/state"
)

const version = "1.0.0"
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

type installOptions struct {
	overwrite     bool
	skipNewsyslog bool
	yes           bool
}

type uninstallOptions struct {
	yes bool
}

type applyOptions struct {
	filePath string
	prune    bool
	dryRun   bool
}

type addOptions struct {
	target              job.Target
	name                string
	schedule            string
	workingDir          string
	hour                int
	hourSet             bool
	minute              int
	minuteSet           bool
	weekday             int
	weekdaySet          bool
	intervalMinutes     int
	intervalSet         bool
	abandonProcessGroup bool
	commandArgs         []string
	stdinScript         string
	dryRun              bool
}

type removeOptions struct {
	name   string
	dryRun bool
}

type listOptions struct {
	json bool
}

type namedJobOptions struct {
	name string
}

type logsOptions struct {
	name      string
	lineCount int
	follow    bool
	lastRun   bool
}

type envSetOptions struct {
	key    string
	value  string
	daemon bool
}

type envGetOptions struct {
	key    string
	daemon bool
}

type envRemoveOptions struct {
	key    string
	daemon bool
}

type envListOptions struct {
	daemon bool
}

type privilegedOperator interface {
	InstallDaemonSpecWithSudo(dirs []string, runtimeSource string, runtimeDest string, plistSource string, plistDest string, metadataSource string, metadataDest string) error
	RemoveDaemonArtifactsWithSudo(plistPaths []string, cleanupPaths []string, extraPaths []string) error
	RemovePathWithSudo(path string) error
	BootoutDaemonWithSudo(plistPath string) error
	WriteFileWithSudo(src, dst string) error
}

type osPrivilegedOperator struct{}

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
	return app.Run(args)
}

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

func (a *App) Run(args []string) error {
	a.promptReader = nil
	a.configureLogger(0)
	root := a.newRootCommand()
	root.SetArgs(args)
	root.SetIn(a.stdin)
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	return root.Execute()
}

func (a *App) runInstall(opts installOptions) error {
	a.logger.Debug("install start")
	installOpts := bootstrap.Options{
		SkipNewsyslog: opts.skipNewsyslog,
		Overwrite:     opts.overwrite,
	}
	if err := a.printInstallPlan(installOpts); err != nil {
		return err
	}
	if !opts.yes {
		approved, err := a.confirmWithDefault("Proceed with install? [y/N]: ", false)
		if err != nil {
			return err
		}
		if !approved {
			_, err = fmt.Fprintln(a.stdout, "install cancelled")
			return err
		}
	}

	result, err := a.boot.Install(installOpts)
	if err != nil {
		a.logger.Debug("install failed", "error", err)
		var permissionErr *bootstrap.PermissionError
		if errors.As(err, &permissionErr) && !opts.skipNewsyslog {
			approved, promptErr := a.confirmWithDefault("system setup requires sudo (newsyslog + daemon state). Retry with sudo? [Y/n]: ", true)
			if promptErr != nil {
				return promptErr
			}
			if approved {
				if retryErr := a.boot.InstallNewsyslogWithSudo(&result); retryErr != nil {
					return retryErr
				}
				if !result.DaemonStateInitialized {
					_ = a.boot.InitDaemonStateWithSudo(&result)
				}
				return a.printInstallSummary(result)
			}
			if summaryErr := a.printInstallSummary(result); summaryErr != nil {
				return summaryErr
			}
			_, summaryErr := fmt.Fprintf(a.stdout, "install manually with: sudo install -m 0644 %s %s\n", result.NewsyslogGeneratedPath, result.NewsyslogInstallPath)
			return summaryErr
		}
		if errors.As(err, &permissionErr) {
			if summaryErr := a.printInstallSummary(result); summaryErr != nil {
				return summaryErr
			}
			_, summaryErr := fmt.Fprintf(a.stdout, "install manually with: sudo install -m 0644 %s %s\n", result.NewsyslogGeneratedPath, result.NewsyslogInstallPath)
			return summaryErr
		}
		return err
	}
	// If daemon state wasn't initialized (requires sudo for /Library paths), prompt and retry.
	if !result.DaemonStateInitialized {
		approved, promptErr := a.confirmWithDefault("daemon state directory requires sudo. Initialize now? [Y/n]: ", true)
		if promptErr != nil {
			return promptErr
		}
		if approved {
			if initErr := a.boot.InitDaemonStateWithSudo(&result); initErr != nil {
				a.logger.Debug("daemon state init failed", "error", initErr)
			}
		}
	}
	a.logger.Info("install completed")
	return a.printInstallSummary(result)
}

func (a *App) runUninstall(opts uninstallOptions) error {
	a.logger.Debug("uninstall start")
	managedSpecs, err := a.listManagedJobs()
	if err != nil {
		return err
	}
	if err := a.printUninstallPlan(managedSpecs); err != nil {
		return err
	}
	if !opts.yes {
		approved, err := a.confirmWithDefault("Proceed with uninstall? [y/N]: ", false)
		if err != nil {
			return err
		}
		if !approved {
			_, err = fmt.Fprintln(a.stdout, "uninstall cancelled")
			return err
		}
	}
	a.logger.Debug("loaded managed jobs", "count", len(managedSpecs))
	var sudoPlists []string
	var sudoMetadata []string
	removedJobs := 0
	for _, managed := range managedSpecs {
		spec := managed.spec
		if err := a.runner.Bootout(spec); err != nil && spec.Target == job.TargetDaemon {
			sudoPlists = append(sudoPlists, spec.PlistPath)
		}
		if err := removeIfExists(spec.PlistPath); err != nil {
			if errors.Is(err, os.ErrPermission) && spec.Target == job.TargetDaemon {
				sudoPlists = appendIfMissing(sudoPlists, spec.PlistPath)
			} else {
				return err
			}
		}
		if _, err := managed.store.Remove(spec.Name); err != nil {
			if spec.Target == job.TargetDaemon && errors.Is(err, os.ErrPermission) {
				for _, path := range managedCleanupPaths(managed.store, spec) {
					sudoMetadata = appendIfMissing(sudoMetadata, path)
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		removedJobs++
	}

	uninstallResult, uninstallErr := a.boot.Uninstall()
	needsSudoNewsyslog := false
	if uninstallErr != nil {
		var permissionErr *bootstrap.PermissionError
		if errors.As(uninstallErr, &permissionErr) {
			needsSudoNewsyslog = true
			uninstallResult.NewsyslogInstallStatus = "permission_denied"
		} else {
			return uninstallErr
		}
	}

	if err := os.RemoveAll(a.store.Paths().Home); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	needsSudoDaemonHome := false
	if err := os.RemoveAll(a.daemonStore.Paths().Home); err != nil && !errors.Is(err, os.ErrNotExist) {
		if errors.Is(err, os.ErrPermission) {
			needsSudoDaemonHome = true
		} else {
			return err
		}
	}

	if len(sudoPlists) > 0 || len(sudoMetadata) > 0 || needsSudoNewsyslog || needsSudoDaemonHome {
		approved, err := a.confirmWithDefault("some system-owned files require sudo to remove. Retry with sudo? [Y/n]: ", true)
		if err != nil {
			return err
		}
		if approved {
			if len(sudoPlists) > 0 || len(sudoMetadata) > 0 || needsSudoDaemonHome {
				var extraPaths []string
				if needsSudoDaemonHome {
					extraPaths = append(extraPaths, a.daemonStore.Paths().Home)
				}
				if err := a.priv.RemoveDaemonArtifactsWithSudo(sudoPlists, sudoMetadata, extraPaths); err != nil {
					return err
				}
			}
			if needsSudoNewsyslog {
				if err := a.boot.UninstallNewsyslogWithSudo(&uninstallResult); err != nil {
					return err
				}
				needsSudoNewsyslog = false
			}
		}
	}

	if err := a.printUninstallSummary(uninstallResult, removedJobs, needsSudoNewsyslog); err != nil {
		return err
	}
	if (len(sudoPlists) > 0 || len(sudoMetadata) > 0 || needsSudoDaemonHome) && !uninstallResult.UsedSudo {
		return a.printUninstallManual(uninstallResult, sudoPlists)
	}
	a.logger.Info("uninstall completed", "removed_jobs", removedJobs)
	return nil
}

func (a *App) runApply(opts applyOptions) error {
	a.logger.Debug("apply start")
	installed, err := a.boot.IsInstalled()
	if err != nil {
		return err
	}
	if !installed {
		return errors.New("apply requires install to be run first")
	}
	specs, err := config.LoadFile(opts.filePath)
	if err != nil {
		return err
	}
	a.logger.Debug("loaded config", "path", opts.filePath, "jobs", len(specs))
	orphanedSpecs, err := a.orphanedManagedJobs(specs)
	if err != nil {
		return err
	}
	if opts.dryRun {
		return a.printApplyPlan(specs, orphanedSpecs, opts.prune)
	}
	runtimeSource, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve current executable: %w", err)
	}
	agentRuntimePath, err := a.store.PrepareRuntimeBinary(runtimeSource)
	if err != nil {
		return err
	}
	daemonRuntimePath := a.daemonStore.RuntimeBinaryPath()
	applied := 0
	var failures []string
	for _, spec := range specs {
		if spec.Target == job.TargetDaemon {
			spec.EnvironmentFilePath = a.daemonStore.EnvFilePath()
		} else {
			spec.EnvironmentFilePath = a.store.EnvFilePath()
		}
		if spec.Target == job.TargetDaemon {
			spec.RuntimeBinaryPath = daemonRuntimePath
			installedSpec, applyErr := a.applyDaemonSpec(spec, runtimeSource)
			if applyErr != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", spec.Name, applyErr))
				continue
			}
			if err := a.reconcileDaemonRuntime(installedSpec); err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", spec.Name, err))
				continue
			}
			applied++
			continue
		}
		spec.RuntimeBinaryPath = agentRuntimePath
		installedSpec, applyErr := a.applySpec(a.store, spec)
		if applyErr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", spec.Name, applyErr))
			continue
		}
		_ = installedSpec
		applied++
	}
	if _, err := fmt.Fprintf(a.stdout, "applied %d job(s)\n", applied); err != nil {
		return err
	}
	if len(failures) > 0 {
		if _, err := fmt.Fprintf(a.stdout, "failed %d job(s):\n", len(failures)); err != nil {
			return err
		}
		for _, failure := range failures {
			if _, err := fmt.Fprintf(a.stdout, "- %s\n", failure); err != nil {
				return err
			}
		}
		return ExitError{Code: 1}
	}
	if len(orphanedSpecs) > 0 {
		pruned, err := a.pruneManagedSpecs(orphanedSpecs, opts.prune)
		if err != nil {
			return err
		}
		if pruned > 0 {
			if _, err := fmt.Fprintf(a.stdout, "pruned %d job(s)\n", pruned); err != nil {
				return err
			}
		}
	}
	a.logger.Info("apply completed", "jobs", applied)
	return nil
}

func (a *App) runAddTarget(opts addOptions) error {
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
		installedSpec, err := a.applyDaemonSpec(spec, runtimeSource)
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
}

func (a *App) runRemove(opts removeOptions) error {
	a.logger.Debug("remove start", "name", opts.name)
	managed, err := a.loadManagedSpec(opts.name)
	if err != nil {
		return err
	}
	if opts.dryRun {
		return a.printRemovePlan(managed)
	}
	if err := a.removeManagedSpec(managed); err != nil {
		return err
	}
	_, err = fmt.Fprintf(a.stdout, "removed %s\n", managed.spec.Name)
	return err
}

func (a *App) pruneManagedSpecs(pruneSpecs []managedSpec, autoApprove bool) (int, error) {
	if len(pruneSpecs) == 0 {
		return 0, nil
	}
	if _, err := fmt.Fprintln(a.stdout, "jobs to prune:"); err != nil {
		return 0, err
	}
	for _, managed := range pruneSpecs {
		if _, err := fmt.Fprintf(a.stdout, "- %s\n", managed.spec.Name); err != nil {
			return 0, err
		}
	}

	if !autoApprove {
		approved, err := a.confirm("apply will remove these Summond-managed jobs. Continue? [y/N]: ")
		if err != nil {
			return 0, err
		}
		if !approved {
			_, err = fmt.Fprintln(a.stdout, "prune cancelled")
			return 0, err
		}
	}

	for _, managed := range pruneSpecs {
		if err := a.removeManagedSpec(managed); err != nil {
			return 0, err
		}
	}
	return len(pruneSpecs), nil
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

func (a *App) runList(opts listOptions) error {
	a.logger.Debug("list start")
	managedSpecs, err := a.listManagedJobs()
	if err != nil {
		return err
	}

	if opts.json {
		specs := make([]job.Spec, 0, len(managedSpecs))
		for _, managed := range managedSpecs {
			specs = append(specs, managed.spec)
		}
		data, err := json.MarshalIndent(specs, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal jobs: %w", err)
		}
		_, err = fmt.Fprintln(a.stdout, string(data))
		return err
	}

	if len(managedSpecs) == 0 {
		_, err = fmt.Fprintln(a.stdout, "no managed jobs")
		return err
	}
	writer := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "NAME\tTARGET\tSCHEDULE\tSTATUS"); err != nil {
		return err
	}
	for _, managed := range managedSpecs {
		spec := managed.spec
		_, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", spec.Name, spec.Target, describeTriggerOrSchedule(spec), describeLastRunStatus(spec))
		if err != nil {
			return err
		}
	}
	return writer.Flush()
}

func (a *App) runState(opts namedJobOptions) error {
	a.logger.Debug("state start", "name", opts.name)
	managed, err := a.loadManagedSpec(opts.name)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(managed.store.MetadataPathForSpec(managed.spec))
	if err != nil {
		return fmt.Errorf("read job metadata: %w", err)
	}
	_, err = a.stdout.Write(data)
	return err
}

func (a *App) runPlist(opts namedJobOptions) error {
	a.logger.Debug("plist start", "name", opts.name)
	managed, err := a.loadManagedSpec(opts.name)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(managed.spec.PlistPath)
	if err != nil {
		return fmt.Errorf("read job plist: %w", err)
	}
	_, err = a.stdout.Write(data)
	return err
}

func (a *App) runExec(opts namedJobOptions) error {
	a.logger.Debug("exec start", "name", opts.name)
	managed, err := a.loadManagedSpec(opts.name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && a.geteuid != nil && a.geteuid() == 0 && os.Getenv("SUDO_USER") != "" {
			return fmt.Errorf("read job metadata: %w (running with sudo uses root-managed jobs; if %s is an agent job, re-run without sudo: summond exec %s)", os.ErrNotExist, opts.name, opts.name)
		}
		return err
	}
	spec := managed.spec
	euid := 0
	if a.geteuid != nil {
		euid = a.geteuid()
	}
	if spec.Target == job.TargetDaemon && euid != 0 {
		return fmt.Errorf("daemon jobs are owned by root; re-run as: sudo summond exec %s", spec.Name)
	}
	if spec.Target != job.TargetDaemon && euid == 0 {
		return fmt.Errorf("agent jobs run as the logged-in user; re-run without sudo: summond exec %s", spec.Name)
	}
	startedAt := time.Now()
	if err := managed.store.RecordExecutionStart(spec.Name, startedAt); err != nil {
		return err
	}
	record := job.ExecutionRecord{StartedAt: startedAt}
	exitCode, runErr := a.executeSpec(spec)
	finishedAt := time.Now()
	record.FinishedAt = &finishedAt
	record.ExitCode = &exitCode
	if runErr != nil {
		record.Error = runErr.Error()
	}
	if err := managed.store.RecordExecutionFinish(spec.Name, record); err != nil {
		return err
	}
	if runErr != nil {
		return ExitError{Code: exitCode, Message: runErr.Error()}
	}
	return nil
}

func (a *App) envStore(daemon bool) *state.Store {
	if daemon {
		return a.daemonStore
	}
	return a.store
}

func (a *App) ensureEnvFile(store *state.Store) (string, error) {
	path := store.EnvFilePath()
	if _, err := os.Stat(path); err == nil {
		return path, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat env file: %w", err)
	}
	// File doesn't exist — create it with default contents.
	if err := a.writeEnvFileContents(path, []byte(bootstrap.RenderEnvFile())); err != nil {
		return "", fmt.Errorf("write env file: %w", err)
	}
	return path, nil
}

func (a *App) writeEnvFileContents(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && !errors.Is(err, os.ErrPermission) {
		return err
	}
	tmp, err := os.CreateTemp("", "summond-env-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	defer os.Remove(tmpPath)
	if err := os.Rename(tmpPath, path); err != nil {
		if errors.Is(err, os.ErrPermission) {
			approved, promptErr := a.confirmWithDefault("writing daemon env file requires sudo. Retry with sudo? [Y/n]: ", true)
			if promptErr != nil {
				return promptErr
			}
			if !approved {
				return err
			}
			return a.priv.WriteFileWithSudo(tmpPath, path)
		}
		return err
	}
	return nil
}

func (a *App) runEnvSet(opts envSetOptions) error {
	path, err := a.ensureEnvFile(a.envStore(opts.daemon))
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read env file: %w", err)
	}
	env, err := readEnvJSON(data)
	if err != nil {
		return fmt.Errorf("parse env file: %w", err)
	}
	_, existed := env[opts.key]
	env[opts.key] = opts.value
	data, err = json.MarshalIndent(env, "", "  ")
	if err != nil {
		return fmt.Errorf("encode env file: %w", err)
	}
	if err := a.writeEnvFileContents(path, append(data, '\n')); err != nil {
		return fmt.Errorf("write env file: %w", err)
	}
	if existed {
		_, err = fmt.Fprintf(a.stdout, "updated %s\n", opts.key)
	} else {
		_, err = fmt.Fprintf(a.stdout, "set %s\n", opts.key)
	}
	return err
}

func (a *App) runEnvGet(opts envGetOptions) error {
	path, err := a.ensureEnvFile(a.envStore(opts.daemon))
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read env file: %w", err)
	}
	env, err := readEnvJSON(data)
	if err != nil {
		return fmt.Errorf("parse env file: %w", err)
	}
	value, ok := env[opts.key]
	if !ok {
		return fmt.Errorf("key %q not found in env file", opts.key)
	}
	_, err = fmt.Fprintf(a.stdout, "%s\n", value)
	return err
}

func (a *App) runEnvRemove(opts envRemoveOptions) error {
	path, err := a.ensureEnvFile(a.envStore(opts.daemon))
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read env file: %w", err)
	}
	env, err := readEnvJSON(data)
	if err != nil {
		return fmt.Errorf("parse env file: %w", err)
	}
	if _, ok := env[opts.key]; !ok {
		return fmt.Errorf("key %q not found in env file", opts.key)
	}
	delete(env, opts.key)
	data, err = json.MarshalIndent(env, "", "  ")
	if err != nil {
		return fmt.Errorf("encode env file: %w", err)
	}
	if err := a.writeEnvFileContents(path, append(data, '\n')); err != nil {
		return fmt.Errorf("write env file: %w", err)
	}
	_, err = fmt.Fprintf(a.stdout, "removed %s\n", opts.key)
	return err
}

func (a *App) runEnvList(opts envListOptions) error {
	path, err := a.ensureEnvFile(a.envStore(opts.daemon))
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read env file: %w", err)
	}
	env, err := readEnvJSON(data)
	if err != nil {
		return fmt.Errorf("parse env file: %w", err)
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, err := fmt.Fprintf(a.stdout, "%s=%s\n", k, env[k]); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) runCd(opts namedJobOptions) error {
	managed, err := a.loadManagedSpec(opts.name)
	if err != nil {
		return err
	}
	dir, err := managed.store.JobDir(managed.spec.Name)
	if err != nil {
		return err
	}
	// Detect whether stdout is a TTY.
	// If non-TTY (e.g. eval $(summond cd <name>)), print a cd command for the shell to eval.
	// If TTY (interactive), spawn a subshell in the job directory.
	info, err := os.Stdout.Stat()
	if err != nil {
		return fmt.Errorf("stat stdout: %w", err)
	}
	isTTY := (info.Mode() & os.ModeCharDevice) != 0
	if !isTTY {
		_, err := fmt.Fprintf(a.stdout, "cd %s\n", dir)
		return err
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	cmd := exec.Command(shell)
	cmd.Dir = dir
	cmd.Stdin = a.stdin
	cmd.Stdout = a.stdout
	cmd.Stderr = a.stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return ExitError{Code: exitErr.ExitCode()}
		}
		return err
	}
	return nil
}

func (a *App) runLogs(opts logsOptions) error {
	a.logger.Debug("logs start", "name", opts.name, "follow", opts.follow, "lines", opts.lineCount, "lastRun", opts.lastRun)
	if opts.lineCount < 0 {
		return errors.New("logs requires -n >= 0")
	}
	managed, err := a.loadManagedSpec(opts.name)
	if err != nil {
		return err
	}
	spec := managed.spec
	type logTarget struct {
		logTailTarget
		offset int64
	}
	var targets []logTarget
	if spec.StdoutPath != "" {
		targets = append(targets, logTarget{logTailTarget: logTailTarget{path: spec.StdoutPath, output: a.stdout, name: "stdout"}, offset: spec.LastStdoutOffset})
	}
	if spec.StderrPath != "" {
		targets = append(targets, logTarget{logTailTarget: logTailTarget{path: spec.StderrPath, output: a.stderr, name: "stderr"}, offset: spec.LastStderrOffset})
	}
	if len(targets) == 0 {
		return errors.New("no log path configured")
	}
	if !opts.follow {
		for _, target := range targets {
			if opts.lastRun {
				if err := a.runTailFromOffset(target.logTailTarget, target.offset); err != nil {
					return err
				}
			} else {
				if err := a.runTail(target.logTailTarget, opts.lineCount, false); err != nil {
					return err
				}
			}
		}
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, len(targets))
	for _, target := range targets {
		target := target
		go func() {
			if opts.lastRun {
				errCh <- a.runTailFromOffsetContext(ctx, target.logTailTarget, target.offset, true)
			} else {
				errCh <- a.runTailContext(ctx, target.logTailTarget, opts.lineCount, true)
			}
		}()
	}
	var firstErr error
	for range targets {
		if err := <-errCh; err != nil && firstErr == nil && !errors.Is(err, context.Canceled) {
			firstErr = err
			cancel()
		}
	}
	return firstErr
}

func logsTailArgs(lineCount int, follow bool, path string) []string {
	args := []string{"-n", fmt.Sprintf("%d", lineCount)}
	if follow {
		args = append(args, "-f")
	}
	return append(args, path)
}

type logTailTarget struct {
	path   string
	output io.Writer
	name   string
}

func (a *App) runTail(target logTailTarget, lineCount int, follow bool) error {
	return a.runTailContext(context.Background(), target, lineCount, follow)
}

func (a *App) runTailContext(ctx context.Context, target logTailTarget, lineCount int, follow bool) error {
	tailArgs := logsTailArgs(lineCount, follow, target.path)
	a.logger.Debug("reading log", "path", target.path, "stream", target.name, "follow", follow, "lines", lineCount)
	cmd := exec.CommandContext(ctx, "tail", tailArgs...)
	cmd.Stdout = target.output
	cmd.Stderr = a.stderr
	return cmd.Run()
}

func (a *App) runTailFromOffset(target logTailTarget, offset int64) error {
	return a.runTailFromOffsetContext(context.Background(), target, offset, false)
}

func (a *App) runTailFromOffsetContext(ctx context.Context, target logTailTarget, offset int64, follow bool) error {
	// tail -c +N prints from byte N (1-based), so offset+1 skips the first `offset` bytes.
	args := []string{"-c", fmt.Sprintf("+%d", offset+1)}
	if follow {
		args = append(args, "-f")
	}
	args = append(args, target.path)
	a.logger.Debug("reading log from offset", "path", target.path, "stream", target.name, "offset", offset, "follow", follow)
	cmd := exec.CommandContext(ctx, "tail", args...)
	cmd.Stdout = target.output
	cmd.Stderr = a.stderr
	return cmd.Run()
}

func (a *App) removeManagedSpec(managed managedSpec) error {
	spec := managed.spec
	needsSudo := false
	if err := a.runner.Bootout(spec); err != nil && spec.Target == job.TargetDaemon {
		needsSudo = true
	}
	if _, err := managed.store.Remove(spec.Name); err != nil {
		if spec.Target == job.TargetDaemon && errors.Is(err, os.ErrPermission) {
			needsSudo = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if needsSudo {
		approved, promptErr := a.confirmWithDefault("removing daemon jobs requires sudo. Retry with sudo? [Y/n]: ", true)
		if promptErr != nil {
			return promptErr
		}
		if approved {
			return a.removeDaemonSpecWithSudo(spec)
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

func (a *App) orphanedManagedJobs(desiredSpecs []job.Spec) ([]managedSpec, error) {
	desired := make(map[string]struct{}, len(desiredSpecs))
	groupFilter := make(map[string]struct{}, len(desiredSpecs))
	for _, spec := range desiredSpecs {
		desired[spec.ManagedKey()] = struct{}{}
		groupFilter[spec.Group] = struct{}{}
	}
	existingSpecs, err := a.listManagedJobs()
	if err != nil {
		return nil, err
	}
	var orphaned []managedSpec
	for _, managed := range existingSpecs {
		if _, ok := groupFilter[managed.spec.Group]; !ok {
			continue
		}
		if _, ok := desired[managed.spec.ManagedKey()]; !ok {
			orphaned = append(orphaned, managed)
		}
	}
	return orphaned, nil
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

func (a *App) executeSpec(spec job.Spec) (int, error) {
	var cmd *exec.Cmd
	envFilePath := a.environmentFilePath(spec)
	envPreamble := jsonEnvPreamble(envFilePath)
	if spec.ShellCommand != "" {
		cmd = exec.Command("/bin/bash", "-c", shellPreamble+envPreamble+spec.ShellCommand)
	} else {
		args := []string{"-c", shellPreamble + envPreamble + `exec "$@"`, "bash", spec.Command}
		args = append(args, spec.Args...)
		cmd = exec.Command("/bin/bash", args...)
	}
	cmd.Dir = spec.WorkingDir
	cmd.Env = mergeEnvironment(nil, spec.Environment)
	cmd.Env = mergeEnvironment(cmd.Env, map[string]string{
		"SUMMOND_JOB_NAME":  spec.Name,
		"SUMMOND_JOB_LABEL": spec.Label,
		"SUMMOND_STATE_DIR": a.storeForTarget(spec.Target).JobDirForSpec(spec),
	})
	cmd.Stdout = a.stdout
	cmd.Stderr = a.stderr
	cmd.Stdin = a.stdin
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), err
		}
		return 1, err
	}
	return 0, nil
}

func (a *App) environmentFilePath(spec job.Spec) string {
	if spec.EnvironmentFilePath != "" {
		return spec.EnvironmentFilePath
	}
	if spec.Target == job.TargetDaemon {
		return a.daemonStore.EnvFilePath()
	}
	return a.store.EnvFilePath()
}

func (a *App) applySpec(store *state.Store, spec job.Spec) (job.Spec, error) {
	a.logger.Debug("reconciling job", "name", spec.Name, "target", spec.Target, "trigger", spec.Trigger, "schedule", spec.Schedule.Kind)
	previous, hadPrevious, previousLoaded, err := a.captureApplyState(store, spec.Name)
	if err != nil {
		return job.Spec{}, err
	}
	installed, err := store.Install(spec)
	if err != nil {
		return job.Spec{}, err
	}
	if loaded, err := a.isLoaded(installed); err != nil {
		a.logger.Debug("load-state check failed", "name", installed.Name, "error", err)
		return installed, a.rollbackApplyFailure(store, installed, previous, hadPrevious, previousLoaded, false, fmt.Errorf("load-state check failed: %w", err))
	} else if loaded {
		a.logger.Debug("job already loaded, bootout before bootstrap", "name", installed.Name)
		if err := a.runner.Bootout(installed); err != nil {
			a.logger.Debug("bootout before bootstrap failed", "name", installed.Name, "error", err)
			return installed, a.rollbackApplyFailure(store, installed, previous, hadPrevious, previousLoaded, false, fmt.Errorf("bootout before bootstrap failed: %w", err))
		}
	}
	if err := a.runner.Bootstrap(installed); err != nil {
		a.logger.Debug("bootstrap failed", "name", installed.Name, "error", err)
		return installed, a.rollbackApplyFailure(store, installed, previous, hadPrevious, previousLoaded, false, fmt.Errorf("bootstrap failed: %w", err))
	}
	if err := a.verifyLoadedJob(installed); err != nil {
		a.logger.Debug("loaded job verification failed", "name", installed.Name, "error", err)
		return installed, a.rollbackApplyFailure(store, installed, previous, hadPrevious, previousLoaded, true, fmt.Errorf("loaded job verification failed: %w", err))
	}
	return installed, nil
}

func (a *App) captureApplyState(store *state.Store, name string) (job.Spec, bool, bool, error) {
	previous, err := store.Load(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return job.Spec{}, false, false, nil
		}
		return job.Spec{}, false, false, err
	}
	loaded, err := a.isLoaded(previous)
	if err != nil {
		return job.Spec{}, false, false, fmt.Errorf("load-state check failed before update: %w", err)
	}
	return previous, true, loaded, nil
}

func (a *App) rollbackApplyFailure(store *state.Store, installed job.Spec, previous job.Spec, hadPrevious bool, previousLoaded bool, unloadInstalled bool, applyErr error) error {
	if rollbackErr := a.rollbackApplyState(store, installed, previous, hadPrevious, previousLoaded, unloadInstalled); rollbackErr != nil {
		return fmt.Errorf("%v (rollback failed: %w)", applyErr, rollbackErr)
	}
	return applyErr
}

func (a *App) rollbackApplyState(store *state.Store, installed job.Spec, previous job.Spec, hadPrevious bool, previousLoaded bool, unloadInstalled bool) error {
	if unloadInstalled {
		if err := a.runner.Bootout(installed); err != nil && !isMissingServiceError(err) {
			return fmt.Errorf("bootout failed job: %w", err)
		}
	}
	if hadPrevious {
		restored, err := store.Install(previous)
		if err != nil {
			return fmt.Errorf("restore previous managed state: %w", err)
		}
		if previousLoaded {
			if err := a.runner.Bootstrap(restored); err != nil {
				return fmt.Errorf("restore previous launchd job: %w", err)
			}
		}
		return nil
	}
	if _, err := store.Remove(installed.Name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove failed managed state: %w", err)
	}
	return nil
}

func (a *App) applyDaemonSpec(spec job.Spec, runtimeSource string) (job.Spec, error) {
	installed, err := a.daemonStore.Install(spec)
	if err == nil {
		return installed, nil
	}
	if !errors.Is(err, os.ErrPermission) {
		return job.Spec{}, err
	}
	approved, promptErr := a.confirmWithDefault("applying daemon jobs requires sudo. Retry with sudo? [Y/n]: ", true)
	if promptErr != nil {
		return job.Spec{}, promptErr
	}
	if !approved {
		return job.Spec{}, err
	}
	return a.installDaemonSpecWithSudo(spec, runtimeSource)
}

func (a *App) installDaemonSpecWithSudo(spec job.Spec, runtimeSource string) (job.Spec, error) {
	installed, plistContent, err := a.daemonStore.PrepareInstall(spec)
	if err != nil {
		return job.Spec{}, err
	}
	if existing, loadErr := a.daemonStore.Load(installed.Name); loadErr == nil {
		preserveRuntimeFields(&installed, existing)
	}
	metadata, err := json.MarshalIndent(installed, "", "  ")
	if err != nil {
		return job.Spec{}, fmt.Errorf("encode job metadata: %w", err)
	}
	tempDir, err := os.MkdirTemp("", "summond-daemon-*")
	if err != nil {
		return job.Spec{}, err
	}
	defer os.RemoveAll(tempDir)
	plistPath := filepath.Join(tempDir, installed.Name+".plist")
	if err := os.WriteFile(plistPath, plistContent, 0o644); err != nil {
		return job.Spec{}, err
	}
	metadataPath := filepath.Join(tempDir, installed.Name+".json")
	if err := os.WriteFile(metadataPath, metadata, 0o644); err != nil {
		return job.Spec{}, err
	}
	if err := a.priv.InstallDaemonSpecWithSudo(
		daemonRequiredDirs(a.daemonStore, installed),
		runtimeSource,
		installed.RuntimeBinaryPath,
		plistPath,
		installed.PlistPath,
		metadataPath,
		a.daemonStore.MetadataPathForSpec(installed),
	); err != nil {
		return job.Spec{}, err
	}
	return installed, nil
}

func daemonRequiredDirs(store *state.Store, spec job.Spec) []string {
	dirs := []string{
		store.Paths().Home,
		store.JobsFilePath(),
		store.JobDirForSpec(spec),
		filepath.Dir(store.RuntimeBinaryPath()),
		filepath.Dir(spec.PlistPath),
	}
	seen := map[string]struct{}{}
	var unique []string
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		unique = append(unique, dir)
	}
	return unique
}

func (a *App) reconcileDaemonRuntime(spec job.Spec) error {
	_ = spec
	return nil
}

func (a *App) removeDaemonSpecWithSudo(spec job.Spec) error {
	return a.priv.RemoveDaemonArtifactsWithSudo(
		[]string{spec.PlistPath},
		managedCleanupPaths(a.daemonStore, spec),
		[]string{a.daemonStore.JobDirForSpec(spec)},
	)
}

func preserveRuntimeFields(dst *job.Spec, src job.Spec) {
	dst.LastStartedAt = src.LastStartedAt
	dst.LastFinishedAt = src.LastFinishedAt
	dst.LastExitCode = src.LastExitCode
	dst.LastError = src.LastError
	dst.RunCount = src.RunCount
	dst.SuccessCount = src.SuccessCount
	dst.FailureCount = src.FailureCount
	dst.RecentRuns = src.RecentRuns
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

func describeExecutionRecord(record job.ExecutionRecord) string {
	status := "running"
	if record.FinishedAt != nil {
		status = "finished"
	}
	if record.ExitCode != nil {
		status = fmt.Sprintf("exit=%d", *record.ExitCode)
	}
	text := fmt.Sprintf("started=%s %s", formatTimestamp(record.StartedAt), status)
	if record.FinishedAt != nil {
		text += fmt.Sprintf(" finished=%s", formatTimestamp(*record.FinishedAt))
	}
	if record.Error != "" {
		text += fmt.Sprintf(" error=%q", record.Error)
	}
	return text
}

func formatTimestamp(ts time.Time) string {
	return ts.In(time.Local).Format("2006-01-02 15:04:05")
}

func mergeEnvironment(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	values := map[string]string{}
	for _, item := range base {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) != 2 {
			continue
		}
		values[parts[0]] = parts[1]
	}
	for key, value := range overrides {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func jsonEnvPreamble(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	env, err := readEnvJSON(data)
	if err != nil {
		return ""
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&sb, "export %s=%q\n", k, env[k])
	}
	return sb.String()
}

func readEnvJSON(data []byte) (map[string]string, error) {
	var env map[string]string
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	return env, nil
}

func writeEnvJSON(path string, env map[string]string) error {
	data, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return fmt.Errorf("encode env file: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create env file temp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write env file temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close env file temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("install env file: %w", err)
	}
	return nil
}

func parseKeyValue(arg string) (key, value string, err error) {
	idx := strings.IndexByte(arg, '=')
	if idx < 0 {
		return "", "", fmt.Errorf("invalid argument %q: expected KEY=VALUE", arg)
	}
	key = arg[:idx]
	if key == "" {
		return "", "", fmt.Errorf("invalid argument %q: key must not be empty", arg)
	}
	if !isValidEnvKey(key) {
		return "", "", fmt.Errorf("invalid key %q: must match [A-Za-z_][A-Za-z0-9_]*", key)
	}
	return key, arg[idx+1:], nil
}

func isValidEnvKey(key string) bool {
	for i, c := range key {
		if i == 0 {
			if !(c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) {
				return false
			}
		} else {
			if !(c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
				return false
			}
		}
	}
	return len(key) > 0
}

type multiValueFlag []string

func (m *multiValueFlag) String() string {
	return strings.Join(*m, ",")
}

func (m *multiValueFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}

func (m multiValueFlag) Map() map[string]string {
	if len(m) == 0 {
		return nil
	}
	values := map[string]string{}
	for _, item := range m {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) != 2 {
			continue
		}
		values[parts[0]] = parts[1]
	}
	return values
}

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

func (a *App) confirm(prompt string) (bool, error) {
	return a.confirmWithDefault(prompt, true)
}

func (a *App) confirmWithDefault(prompt string, defaultYes bool) (bool, error) {
	_, err := fmt.Fprint(a.stdout, prompt)
	if err != nil {
		return false, err
	}
	if a.promptReader == nil {
		a.promptReader = bufio.NewReader(a.stdin)
	}
	line, err := a.promptReader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	answer := strings.TrimSpace(strings.ToLower(line))
	if answer == "" {
		return defaultYes && err == nil, nil
	}
	return answer == "y" || answer == "yes", nil
}

func (a *App) printInstallSummary(result bootstrap.Result) error {
	_ = result
	return nil
}

func (a *App) printUninstallSummary(result bootstrap.UninstallResult, removedJobs int, needsNewsyslogWarning bool) error {
	_ = result
	_ = removedJobs
	if needsNewsyslogWarning {
		a.logger.Info("uninstall warning", "newsyslog_install_path", result.NewsyslogInstallPath)
	}
	return nil
}

func (a *App) printUninstallManual(result bootstrap.UninstallResult, sudoPlists []string) error {
	for _, plistPath := range sudoPlists {
		if _, err := fmt.Fprintf(a.stdout, "manual cleanup: sudo launchctl bootout system %s || true\n", plistPath); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(a.stdout, "manual cleanup: sudo rm -f %s\n", plistPath); err != nil {
			return err
		}
	}
	if result.NewsyslogInstallStatus != "removed" {
		if _, err := fmt.Fprintf(a.stdout, "manual cleanup: sudo rm -f %s\n", result.NewsyslogInstallPath); err != nil {
			return err
		}
	}
	return nil
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func appendIfMissing(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func managedCleanupPaths(store *state.Store, spec job.Spec) []string {
	paths := []string{
		store.MetadataPathForSpec(spec),
		store.MetadataPathForSpec(spec) + ".lock",
		spec.StdoutPath,
		spec.StderrPath,
	}
	var unique []string
	for _, path := range paths {
		if path == "" {
			continue
		}
		unique = appendIfMissing(unique, path)
	}
	return unique
}

func (a *App) printApplyPlan(specs []job.Spec, orphanedSpecs []managedSpec, prune bool) error {
	if _, err := fmt.Fprintln(a.stdout, "apply will:"); err != nil {
		return err
	}
	changes := 0
	for _, spec := range specs {
		action, err := a.describeApplyAction(spec)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(a.stdout, "- %s %s job: %s (%s)\n", action, spec.Target, spec.Name, describeTriggerOrSchedule(spec)); err != nil {
			return err
		}
		changes++
	}
	for _, managed := range orphanedSpecs {
		action := "prompt to prune"
		if prune {
			action = "prune"
		}
		if _, err := fmt.Fprintf(a.stdout, "- %s managed job: %s (%s)\n", action, managed.spec.Name, managed.spec.Target); err != nil {
			return err
		}
		changes++
	}
	if changes == 0 {
		if _, err := fmt.Fprintln(a.stdout, "- no job changes"); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(a.stdout, "dry run: no changes made")
	return err
}

func (a *App) describeApplyAction(spec job.Spec) (string, error) {
	if _, err := a.storeForTarget(spec.Target).Load(spec.Name); err == nil {
		return "update", nil
	} else if errors.Is(err, os.ErrNotExist) {
		return "create", nil
	} else {
		return "", err
	}
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

func (a *App) printRemovePlan(managed managedSpec) error {
	spec := managed.spec
	if _, err := fmt.Fprintln(a.stdout, "remove will:"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.stdout, "- boot out managed job: %s (%s)\n", spec.Name, spec.PlistPath); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.stdout, "- remove managed plist: %s\n", spec.PlistPath); err != nil {
		return err
	}
	for _, path := range managedCleanupPaths(managed.store, spec) {
		if _, err := fmt.Fprintf(a.stdout, "- remove managed state/log: %s\n", path); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(a.stdout, "- remove managed job directory: %s\n", managed.store.JobDirForSpec(spec)); err != nil {
		return err
	}
	if spec.Target == job.TargetDaemon {
		if _, err := fmt.Fprintln(a.stdout, "- may require sudo for daemon-owned files"); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(a.stdout, "dry run: no changes made")
	return err
}

func (a *App) printInstallPlan(opts bootstrap.Options) error {
	configPath, err := resolveCurrentConfigPath()
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(a.stdout, "install will:"); err != nil {
		return err
	}
	configAction, err := describeInstallAction(configPath, opts.Overwrite)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.stdout, "- %s starter config: %s\n", configAction, configPath); err != nil {
		return err
	}
	installMarkerPath := filepath.Join(a.store.Paths().Home, ".installed")
	installMarkerAction, err := describeInstallAction(installMarkerPath, true)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.stdout, "- %s install marker: %s\n", installMarkerAction, installMarkerPath); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.stdout, "- manage agent state under: %s\n", a.store.Paths().Home); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.stdout, "- manage daemon state under: %s\n", a.daemonStore.Paths().Home); err != nil {
		return err
	}
	if opts.SkipNewsyslog {
		if _, err := fmt.Fprintln(a.stdout, "- skip newsyslog generation and system install"); err != nil {
			return err
		}
	} else {
		generatedNewsyslogAction, err := describeInstallAction(a.boot.GeneratedNewsyslogPath(), opts.Overwrite)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(a.stdout, "- %s generated newsyslog config: %s\n", generatedNewsyslogAction, a.boot.GeneratedNewsyslogPath()); err != nil {
			return err
		}
		systemNewsyslogAction, err := describeInstallAction(a.boot.SystemNewsyslogPath(), true)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(a.stdout, "- %s system newsyslog config: %s\n", systemNewsyslogAction, a.boot.SystemNewsyslogPath()); err != nil {
			return err
		}
	}
	if opts.Overwrite {
		if _, err := fmt.Fprintln(a.stdout, "- overwrite generated files if they already exist"); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) printUninstallPlan(specs []managedSpec) error {
	if _, err := fmt.Fprintln(a.stdout, "uninstall will:"); err != nil {
		return err
	}
	for _, managed := range specs {
		spec := managed.spec
		if _, err := fmt.Fprintf(a.stdout, "- boot out managed job: %s (%s)\n", spec.Name, spec.PlistPath); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(a.stdout, "- remove managed plist: %s\n", spec.PlistPath); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(a.stdout, "- remove generated newsyslog config: %s\n", a.boot.GeneratedNewsyslogPath()); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.stdout, "- remove system newsyslog config: %s\n", a.boot.SystemNewsyslogPath()); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.stdout, "- remove managed agent state directory: %s\n", a.store.Paths().Home); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.stdout, "- remove managed daemon state directory: %s\n", a.daemonStore.Paths().Home); err != nil {
		return err
	}
	return nil
}

func resolveCurrentConfigPath() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve current working directory: %w", err)
	}
	return filepath.Join(wd, "summond.toml"), nil
}

func describeInstallAction(path string, overwrite bool) (string, error) {
	_, err := os.Stat(path)
	if err == nil {
		if overwrite {
			return "overwrite", nil
		}
		return "leave unchanged", nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return "create", nil
	}
	return "", err
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
