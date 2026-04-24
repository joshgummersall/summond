package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/standardlabs/summond/internal/bootstrap"
	"github.com/standardlabs/summond/internal/config"
	"github.com/standardlabs/summond/internal/job"
	"github.com/standardlabs/summond/internal/launchd"
	"github.com/standardlabs/summond/internal/state"
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
}

type privilegedOperator interface {
	InstallDaemonSpecWithSudo(dirs []string, runtimeSource string, runtimeDest string, plistSource string, plistDest string, metadataSource string, metadataDest string) error
	RemoveDaemonArtifactsWithSudo(plistPaths []string, metadataPaths []string, home string) error
	RemovePathWithSudo(path string) error
	BootoutDaemonWithSudo(plistPath string) error
}

type osPrivilegedOperator struct{}

func Run(args []string, stdout io.Writer) error {
	paths, err := state.DiscoverPathSet()
	if err != nil {
		return err
	}
	app := NewAppWithStores(
		os.Stdin,
		stdout,
		state.NewStore(paths.Agent),
		state.NewStore(paths.Daemon),
		launchd.LaunchCtl{},
		bootstrap.NewManagerWithDaemonHome(paths.Agent, paths.Daemon, bootstrap.OSInstaller{}),
	)
	app.stderr = os.Stderr
	return app.Run(args)
}

func NewApp(stdin io.Reader, stdout io.Writer, store *state.Store, runner launchd.Runner, boot *bootstrap.Manager) *App {
	daemonPaths := store.Paths()
	daemonPaths.Home = daemonPaths.Home + "-daemon"
	return NewAppWithStores(stdin, stdout, store, state.NewStore(daemonPaths), runner, boot)
}

func NewAppWithStores(stdin io.Reader, stdout io.Writer, store *state.Store, daemonStore *state.Store, runner launchd.Runner, boot *bootstrap.Manager) *App {
	return &App{stdin: stdin, stdout: stdout, stderr: io.Discard, store: store, daemonStore: daemonStore, runner: runner, boot: boot, priv: osPrivilegedOperator{}}
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

func (a *App) runInstall(args []string) error {
	a.logger.Debug("install start")
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {
		printCommandUsage(a.stdout, "install")
	}
	overwrite := fs.Bool("overwrite", false, "overwrite generated files")
	skipNewsyslog := fs.Bool("skip-newsyslog", false, "skip generating newsyslog config")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if len(fs.Args()) != 0 {
		return errors.New("install does not accept positional arguments")
	}
	opts := bootstrap.Options{
		SkipNewsyslog: *skipNewsyslog,
		Overwrite:     *overwrite,
	}
	if err := a.printInstallPlan(opts); err != nil {
		return err
	}
	approved, err := a.confirmWithDefault("Proceed with install? [y/N]: ", false)
	if err != nil {
		return err
	}
	if !approved {
		_, err = fmt.Fprintln(a.stdout, "install cancelled")
		return err
	}

	result, err := a.boot.Install(opts)
	if err != nil {
		a.logger.Debug("install failed", "error", err)
		var permissionErr *bootstrap.PermissionError
		if errors.As(err, &permissionErr) && !*skipNewsyslog {
			approved, promptErr := a.confirmWithDefault("newsyslog install requires sudo. Retry with sudo? [Y/n]: ", true)
			if promptErr != nil {
				return promptErr
			}
			if approved {
				if retryErr := a.boot.InstallNewsyslogWithSudo(&result); retryErr != nil {
					return retryErr
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
	a.logger.Info("install completed")
	return a.printInstallSummary(result)
}

func (a *App) runUninstall(args []string) error {
	a.logger.Debug("uninstall start")
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {
		printCommandUsage(a.stdout, "uninstall")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if len(fs.Args()) != 0 {
		return errors.New("uninstall does not accept positional arguments")
	}
	managedSpecs, err := a.listManagedJobs()
	if err != nil {
		return err
	}
	if err := a.printUninstallPlan(managedSpecs); err != nil {
		return err
	}
	approved, err := a.confirmWithDefault("Proceed with uninstall? [y/N]: ", false)
	if err != nil {
		return err
	}
	if !approved {
		_, err = fmt.Fprintln(a.stdout, "uninstall cancelled")
		return err
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
				daemonHome := ""
				if needsSudoDaemonHome {
					daemonHome = a.daemonStore.Paths().Home
				}
				if err := a.priv.RemoveDaemonArtifactsWithSudo(sudoPlists, sudoMetadata, daemonHome); err != nil {
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

func (a *App) runApply(args []string) error {
	a.logger.Debug("apply start")
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {
		printCommandUsage(a.stdout, "apply")
	}
	prune := fs.Bool("prune", false, "remove managed jobs missing from the config without prompting")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	filePath, err := optionalConfigPath(fs.Args(), "apply")
	if err != nil {
		return err
	}
	installed, err := a.boot.IsInstalled()
	if err != nil {
		return err
	}
	if !installed {
		return errors.New("apply requires install to be run first")
	}
	runtimeSource, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve current executable: %w", err)
	}
	specs, err := config.LoadFile(filePath)
	if err != nil {
		return err
	}
	a.logger.Debug("loaded config", "path", filePath, "jobs", len(specs))
	orphanedSpecs, err := a.orphanedManagedJobs(specs)
	if err != nil {
		return err
	}
	agentRuntimePath, err := a.store.PrepareRuntimeBinary(runtimeSource)
	if err != nil {
		return err
	}
	daemonRuntimePath := a.daemonStore.RuntimeBinaryPath()
	envFilePath := a.store.EnvFilePath()
	applied := 0
	var failures []string
	for _, spec := range specs {
		spec.EnvironmentFilePath = envFilePath
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
		pruned, err := a.pruneManagedSpecs(orphanedSpecs, *prune)
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

func (a *App) runAdd(args []string) error {
	a.logger.Debug("add start", "args", args)
	if isHelpArg(args) || len(args) == 0 {
		printCommandUsage(a.stdout, "add")
		return nil
	}
	switch args[0] {
	case "agent":
		return a.runAddTarget(job.TargetAgent, args[1:])
	case "daemon":
		return a.runAddTarget(job.TargetDaemon, args[1:])
	case "help":
		printCommandUsage(a.stdout, "add")
		return nil
	default:
		return fmt.Errorf("add requires a subcommand: agent or daemon")
	}
}

func (a *App) runAddTarget(target job.Target, args []string) error {
	a.logger.Debug("add target start", "target", target, "args", args)
	commandName := "add " + string(target)
	if isHelpArg(args) {
		printCommandUsage(a.stdout, commandName)
		return nil
	}
	if len(args) == 0 || args[0] == "--" || strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("%s requires a job name", commandName)
	}
	name := args[0]
	args = args[1:]
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {
		printCommandUsage(a.stdout, commandName)
	}
	schedule := fs.String("schedule", "", "schedule kind")
	workingDir := fs.String("working-dir", "", "working directory")
	var hour optionalIntFlag
	var minute optionalIntFlag
	var weekday optionalIntFlag
	var intervalMinutes optionalIntFlag
	fs.Var(&hour, "hour", "schedule hour")
	fs.Var(&minute, "minute", "schedule minute")
	fs.Var(&weekday, "weekday", "schedule weekday")
	fs.Var(&intervalMinutes, "interval-minutes", "interval schedule minutes")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	commandArgs := append([]string(nil), fs.Args()...)
	hasArgvCommand := len(commandArgs) > 0
	hasShellStdin := !hasArgvCommand && stdinHasData(a.stdin)
	commandModes := 0
	for _, active := range []bool{hasArgvCommand, hasShellStdin} {
		if active {
			commandModes++
		}
	}
	if commandModes == 0 {
		return errors.New("add requires a command after -- or shell script input on stdin")
	}
	if commandModes > 1 {
		return fmt.Errorf("%s accepts only one of command argv after -- or shell script input on stdin", commandName)
	}
	if *schedule == "" {
		return fmt.Errorf("%s requires --schedule", commandName)
	}
	installed, err := a.boot.IsInstalled()
	if err != nil {
		return err
	}
	if !installed {
		return fmt.Errorf("%s requires install to be run first", commandName)
	}
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve current working directory: %w", err)
	}
	if _, err := a.loadManagedSpec(name); err == nil {
		return fmt.Errorf("job %q already exists", name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	spec := job.Spec{
		Name:       name,
		Target:     target,
		WorkingDir: *workingDir,
		Schedule: job.Schedule{
			Kind: job.ScheduleKind(*schedule),
		},
	}
	switch {
	case hasArgvCommand:
		spec.Command = commandArgs[0]
		spec.Args = append([]string(nil), commandArgs[1:]...)
	case hasShellStdin:
		data, err := io.ReadAll(a.stdin)
		if err != nil {
			return fmt.Errorf("read shell command from stdin: %w", err)
		}
		spec.ShellCommand = strings.TrimRight(string(data), "\n")
		if strings.TrimSpace(spec.ShellCommand) == "" {
			return errors.New("shell stdin produced an empty command")
		}
	}
	if spec.WorkingDir == "" {
		spec.WorkingDir = wd
	}
	if hour.set {
		spec.Schedule.Hour = hour.value
		spec.Schedule.HourSet = true
	}
	if minute.set {
		spec.Schedule.Minute = minute.value
		spec.Schedule.MinuteSet = true
	}
	if weekday.set {
		spec.Schedule.Weekday = weekday.value
		spec.Schedule.WeekdaySet = true
	}
	if intervalMinutes.set {
		spec.Schedule.IntervalMinutes = intervalMinutes.value
		spec.Schedule.IntervalSet = true
	}
	if err := spec.Normalize(); err != nil {
		return err
	}
	runtimeSource, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve current executable: %w", err)
	}
	spec.EnvironmentFilePath = a.store.EnvFilePath()
	if target == job.TargetDaemon {
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

func (a *App) runRemove(args []string) error {
	a.logger.Debug("remove start", "args", args)
	if isHelpArg(args) {
		printCommandUsage(a.stdout, "remove")
		return nil
	}
	if len(args) != 1 {
		return errors.New("remove requires a job name")
	}
	managed, err := a.loadManagedSpec(args[0])
	if err != nil {
		return err
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

func (a *App) runList(args []string) error {
	a.logger.Debug("list start")
	if isHelpArg(args) {
		printCommandUsage(a.stdout, "list")
		return nil
	}
	if len(args) != 0 {
		return errors.New("list does not accept arguments")
	}
	managedSpecs, err := a.listManagedJobs()
	if err != nil {
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

func (a *App) runState(args []string) error {
	a.logger.Debug("state start", "args", args)
	if isHelpArg(args) {
		printCommandUsage(a.stdout, "state")
		return nil
	}
	if len(args) != 1 {
		return errors.New("state requires a job name")
	}
	managed, err := a.loadManagedSpec(args[0])
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

func (a *App) runPlist(args []string) error {
	a.logger.Debug("plist start", "args", args)
	if isHelpArg(args) {
		printCommandUsage(a.stdout, "plist")
		return nil
	}
	if len(args) != 1 {
		return errors.New("plist requires a job name")
	}
	managed, err := a.loadManagedSpec(args[0])
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

func (a *App) runExec(args []string) error {
	a.logger.Debug("exec start", "args", args)
	if isHelpArg(args) {
		printCommandUsage(a.stdout, "exec")
		return nil
	}
	if len(args) != 1 {
		return errors.New("exec requires a job name")
	}
	managed, err := a.loadManagedSpec(args[0])
	if err != nil {
		return err
	}
	spec := managed.spec
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

func (a *App) runEnv(args []string) error {
	if isHelpArg(args) {
		printCommandUsage(a.stdout, "env")
		return nil
	}
	if len(args) != 0 {
		return errors.New("env does not accept positional arguments")
	}
	path := a.store.EnvFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create env file directory: %w", err)
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(path, []byte(bootstrap.RenderEnvFile()), 0o644); err != nil {
			return fmt.Errorf("write env file: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("stat env file: %w", err)
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		return errors.New("env requires $EDITOR or $VISUAL to be set")
	}
	cmd := exec.Command("/bin/sh", "-lc", `exec ${EDITOR:-${VISUAL:-}} "$1"`, "sh", path)
	cmd.Stdin = a.stdin
	cmd.Stdout = a.stdout
	cmd.Stderr = a.stderr
	cmd.Env = mergeEnvironment(os.Environ(), map[string]string{
		"EDITOR": editor,
		"VISUAL": os.Getenv("VISUAL"),
	})
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return ExitError{Code: exitErr.ExitCode()}
		}
		return err
	}
	return nil
}

func (a *App) runCd(args []string) error {
	if isHelpArg(args) {
		printCommandUsage(a.stdout, "cd")
		return nil
	}
	if len(args) != 1 {
		return errors.New("cd requires a job name")
	}
	managed, err := a.loadManagedSpec(args[0])
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

func (a *App) runLogs(args []string) error {
	a.logger.Debug("logs start", "args", args)
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {
		printCommandUsage(a.stdout, "logs")
	}
	lineCount := fs.Int("n", 40, "number of lines to print")
	follow := fs.Bool("follow", false, "follow appended data")
	fs.BoolVar(follow, "f", false, "follow appended data")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("logs requires a job name")
	}
	if *lineCount < 0 {
		return errors.New("logs requires -n >= 0")
	}
	managed, err := a.loadManagedSpec(fs.Args()[0])
	if err != nil {
		return err
	}
	spec := managed.spec
	targets := []logTailTarget{}
	if spec.StdoutPath != "" {
		targets = append(targets, logTailTarget{path: spec.StdoutPath, output: a.stdout, name: "stdout"})
	}
	if spec.StderrPath != "" {
		targets = append(targets, logTailTarget{path: spec.StderrPath, output: a.stderr, name: "stderr"})
	}
	if len(targets) == 0 {
		return errors.New("no log path configured")
	}
	if !*follow {
		for _, target := range targets {
			if err := a.runTail(target, *lineCount, false); err != nil {
				return err
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
			errCh <- a.runTailContext(ctx, target, *lineCount, true)
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

func isHelpArg(args []string) bool {
	return len(args) == 1 && (args[0] == "-h" || args[0] == "--help")
}

func (a *App) requireSingleSpec(args []string, name string) (managedSpec, error) {
	if len(args) != 1 {
		return managedSpec{}, fmt.Errorf("%s requires a job name", name)
	}
	return a.loadManagedSpec(args[0])
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

func printUsage(stdout io.Writer) {
	fmt.Fprintln(stdout, "summond")
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "Usage:")
	fmt.Fprintln(stdout, "  summond [-v|-vv] <command> [arguments]")
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "Commands:")
	fmt.Fprintln(stdout, "  install                    Scaffold config and newsyslog setup")
	fmt.Fprintln(stdout, "  uninstall                  Remove Summond-managed jobs and setup")
	fmt.Fprintln(stdout, "  apply [file]               Apply jobs from a TOML file")
	fmt.Fprintln(stdout, "  add <target> ...           Add and install a managed job")
	fmt.Fprintln(stdout, "  remove <name>              Remove a managed job and all state")
	fmt.Fprintln(stdout, "  list                       List managed jobs")
	fmt.Fprintln(stdout, "  state <name>               Print the job state.json file")
	fmt.Fprintln(stdout, "  plist <name>               Print the job plist file")
	fmt.Fprintln(stdout, "  logs [flags] <name>        Print job logs")
	fmt.Fprintln(stdout, "  exec <name>                Run a managed job immediately")
	fmt.Fprintln(stdout, "  env                        Edit the shared job environment file")
	fmt.Fprintln(stdout, "  cd <name>                  Open a shell in the job's state directory")
	fmt.Fprintln(stdout, "  version                    Print the CLI version")
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "Run 'summond <command> --help' for command-specific usage.")
}

func printCommandUsage(stdout io.Writer, command string) {
	switch command {
	case "install":
		fmt.Fprintln(stdout, "Usage:")
		fmt.Fprintln(stdout, "  summond install [flags]")
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Flags:")
		fmt.Fprintln(stdout, "  --overwrite                Overwrite generated files")
		fmt.Fprintln(stdout, "  --skip-newsyslog           Skip generating and installing newsyslog config")
	case "uninstall":
		fmt.Fprintln(stdout, "Usage:")
		fmt.Fprintln(stdout, "  summond uninstall [flags]")
	case "apply":
		fmt.Fprintln(stdout, "Usage:")
		fmt.Fprintln(stdout, "  summond apply [flags] [file]")
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Apply jobs from a TOML config file.")
		fmt.Fprintln(stdout, "If [file] is omitted, reads ./summond.toml.")
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Flags:")
		fmt.Fprintln(stdout, "  --prune                    Remove managed jobs missing from the config without prompting")
	case "add":
		fmt.Fprintln(stdout, "Usage:")
		fmt.Fprintln(stdout, "  summond add <target> ...")
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Targets:")
		fmt.Fprintln(stdout, "  agent                      Add a LaunchAgent job")
		fmt.Fprintln(stdout, "  daemon                     Add a LaunchDaemon job")
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Run 'summond add <target> --help' for target-specific usage.")
	case "add agent":
		fmt.Fprintln(stdout, "Usage:")
		fmt.Fprintln(stdout, "  summond add agent <name> [flags]")
		fmt.Fprintln(stdout, "  summond add agent <name> [flags] -- <command> [args]")
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Add and install an agent job without editing summond.toml.")
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Flags:")
		fmt.Fprintln(stdout, "  --schedule <kind>          daily, hourly, weekly, login, boot, interval")
		fmt.Fprintln(stdout, "  --hour <hour>              Hour for daily/weekly schedules")
		fmt.Fprintln(stdout, "  --minute <minute>          Minute for hourly/daily/weekly schedules")
		fmt.Fprintln(stdout, "  --weekday <weekday>        Weekday for weekly schedules")
		fmt.Fprintln(stdout, "  --interval-minutes <n>     Interval minutes for interval schedules")
		fmt.Fprintln(stdout, "  --working-dir <path>       Working directory")
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Examples:")
		fmt.Fprintln(stdout, "  summond add agent my-job -- /bin/echo hello --flag")
		fmt.Fprintln(stdout, "  summond add agent my-script <<'EOF'")
		fmt.Fprintln(stdout, "  echo hi")
		fmt.Fprintln(stdout, "  EOF")
	case "add daemon":
		fmt.Fprintln(stdout, "Usage:")
		fmt.Fprintln(stdout, "  summond add daemon <name> [flags]")
		fmt.Fprintln(stdout, "  summond add daemon <name> [flags] -- <command> [args]")
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Add and install a daemon job without editing summond.toml.")
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Flags:")
		fmt.Fprintln(stdout, "  --schedule <kind>          daily, hourly, weekly, boot, interval")
		fmt.Fprintln(stdout, "  --hour <hour>              Hour for daily/weekly schedules")
		fmt.Fprintln(stdout, "  --minute <minute>          Minute for hourly/daily/weekly schedules")
		fmt.Fprintln(stdout, "  --weekday <weekday>        Weekday for weekly schedules")
		fmt.Fprintln(stdout, "  --interval-minutes <n>     Interval minutes for interval schedules")
		fmt.Fprintln(stdout, "  --working-dir <path>       Working directory")
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Examples:")
		fmt.Fprintln(stdout, "  summond add daemon my-daemon --schedule boot -- /usr/local/bin/task")
		fmt.Fprintln(stdout, "  summond add daemon my-script --schedule daily <<'EOF'")
		fmt.Fprintln(stdout, "  echo hi")
		fmt.Fprintln(stdout, "  EOF")
	case "remove":
		fmt.Fprintln(stdout, "Usage:")
		fmt.Fprintln(stdout, "  summond remove <name>")
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Remove the managed job, its plist, logs, and persisted state.")
	case "list":
		fmt.Fprintln(stdout, "Usage:")
		fmt.Fprintln(stdout, "  summond list")
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "List all managed jobs with target, schedule, and status.")
	case "state":
		fmt.Fprintln(stdout, "Usage:")
		fmt.Fprintln(stdout, "  summond state <name>")
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Print the managed job's persisted state.json file.")
	case "plist":
		fmt.Fprintln(stdout, "Usage:")
		fmt.Fprintln(stdout, "  summond plist <name>")
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Print the managed job's installed plist file.")
	case "logs":
		fmt.Fprintln(stdout, "Usage:")
		fmt.Fprintln(stdout, "  summond logs [flags] <name>")
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Flags:")
		fmt.Fprintln(stdout, "  -n <lines>                 Number of lines to print (default 40)")
		fmt.Fprintln(stdout, "  -f, --follow               Follow appended log output")
	case "exec":
		fmt.Fprintln(stdout, "Usage:")
		fmt.Fprintln(stdout, "  summond exec <name>")
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Run a managed job immediately and record its execution result.")
	case "env":
		fmt.Fprintln(stdout, "Usage:")
		fmt.Fprintln(stdout, "  summond env")
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Open the shared shell env file in $EDITOR or $VISUAL.")
	case "cd":
		fmt.Fprintln(stdout, "Usage:")
		fmt.Fprintln(stdout, "  summond cd <name>")
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Open a subshell in the job's state directory (interactive), or")
		fmt.Fprintln(stdout, "print a cd command suitable for eval (non-interactive).")
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Examples:")
		fmt.Fprintln(stdout, "  summond cd myjob                  # drops into subshell")
		fmt.Fprintln(stdout, "  eval $(summond cd myjob)          # cd in current shell")
	default:
		printUsage(stdout)
	}
}

func parseGlobalFlags(args []string) (int, []string, error) {
	verbosity := 0
	for len(args) > 0 {
		switch args[0] {
		case "-v":
			verbosity++
			args = args[1:]
		case "-vv":
			verbosity += 2
			args = args[1:]
		case "--verbose":
			verbosity++
			args = args[1:]
		default:
			return verbosity, args, nil
		}
	}
	return verbosity, args, nil
}

func optionalConfigPath(args []string, command string) (string, error) {
	filePath := "summond.toml"
	switch len(args) {
	case 0:
		return filePath, nil
	case 1:
		return args[0], nil
	default:
		return "", fmt.Errorf("%s accepts at most one config path", command)
	}
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
	if spec.ShellCommand != "" {
		cmd = exec.Command("/bin/bash", "-lc", shellPreamble+shellSourcePreamble(envFilePath)+spec.ShellCommand)
	} else {
		args := []string{"-lc", shellPreamble + shellSourcePreamble(envFilePath) + `exec "$@"`, "bash", spec.Command}
		args = append(args, spec.Args...)
		cmd = exec.Command("/bin/bash", args...)
	}
	cmd.Dir = spec.WorkingDir
	cmd.Env = mergeEnvironment(os.Environ(), spec.Environment)
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
	jobDir, _ := store.JobDir(spec.Name)
	dirs := []string{
		store.Paths().Home,
		store.JobsFilePath(),
		jobDir,
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
		"",
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

func shellSourcePreamble(path string) string {
	if path == "" {
		return ""
	}
	return fmt.Sprintf("if [ -f %q ]; then . %q; fi\n", path, path)
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

type optionalIntFlag struct {
	value int
	set   bool
}

func (f *optionalIntFlag) String() string {
	if f == nil || !f.set {
		return ""
	}
	return strconv.Itoa(f.value)
}

func (f *optionalIntFlag) Set(value string) error {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return err
	}
	f.value = parsed
	f.set = true
	return nil
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

func (osPrivilegedOperator) RemoveDaemonArtifactsWithSudo(plistPaths []string, metadataPaths []string, home string) error {
	script := `
plist_count="$1"
metadata_count="$2"
shift 2
i=0
while [ "$i" -lt "$plist_count" ]; do
  launchctl bootout system "$1" >/dev/null 2>&1 || true
  rm -rf "$1"
  shift
  i=$((i + 1))
done
i=0
while [ "$i" -lt "$metadata_count" ]; do
  rm -rf "$1"
  shift
  i=$((i + 1))
done
if [ "$#" -gt 0 ] && [ -n "$1" ]; then
  rm -rf "$1"
fi
`
	args := []string{fmt.Sprintf("%d", len(plistPaths)), fmt.Sprintf("%d", len(metadataPaths))}
	args = append(args, plistPaths...)
	args = append(args, metadataPaths...)
	if home != "" {
		args = append(args, home)
	}
	return runSudoScript(script, args...)
}

func (osPrivilegedOperator) RemovePathWithSudo(path string) error {
	return runSudoScript(`rm -rf "$1"`, path)
}

func (osPrivilegedOperator) BootoutDaemonWithSudo(plistPath string) error {
	return runSudoScript(`launchctl bootout system "$1"`, plistPath)
}
