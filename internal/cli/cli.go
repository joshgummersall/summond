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
	Code int
}

func (e ExitError) Error() string {
	return ""
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
	InstallDaemonSpecWithSudo(dirs []string, runtimeSource string, runtimeDest string, plistSource string, plistDest string, metadataSource string, metadataDest string, enabled bool) error
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
	verbosity, remaining, err := parseGlobalFlags(args)
	if err != nil {
		return err
	}
	a.configureLogger(verbosity)
	if len(remaining) == 0 {
		printUsage(a.stdout)
		return nil
	}
	a.logger.Debug("running command", "args", remaining)

	switch remaining[0] {
	case "install":
		return a.runInstall(remaining[1:])
	case "uninstall":
		return a.runUninstall(remaining[1:])
	case "apply":
		return a.runApply(remaining[1:])
	case "prune":
		return a.runPrune(remaining[1:])
	case "list":
		return a.runList(remaining[1:])
	case "inspect":
		return a.runInspect(remaining[1:])
	case "logs":
		return a.runLogs(remaining[1:])
	case "exec":
		return a.runExec(remaining[1:])
	case "version":
		_, err := fmt.Fprintf(a.stdout, "summond %s\n", version)
		return err
	case "help", "-h", "--help":
		printUsage(a.stdout)
		return nil
	default:
		return fmt.Errorf("unknown command %q", remaining[0])
	}
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
	var runtimeWarnings []string
	agentRuntimePath, err := a.store.PrepareRuntimeBinary(runtimeSource)
	if err != nil {
		return err
	}
	daemonRuntimePath := a.daemonStore.RuntimeBinaryPath()
	for _, spec := range specs {
		if spec.Target == job.TargetDaemon {
			spec.RuntimeBinaryPath = daemonRuntimePath
			installedSpec, applyErr := a.applyDaemonSpec(spec, runtimeSource)
			if applyErr != nil {
				return applyErr
			}
			if warning := a.reconcileDaemonRuntime(installedSpec); warning != "" {
				runtimeWarnings = append(runtimeWarnings, warning)
			}
			continue
		}
		spec.RuntimeBinaryPath = agentRuntimePath
		installedSpec, warning, applyErr := a.applySpec(a.store, spec)
		if applyErr != nil {
			return applyErr
		}
		if warning != "" {
			runtimeWarnings = append(runtimeWarnings, warning)
		}
		_ = installedSpec
	}
	if _, err := fmt.Fprintf(a.stdout, "applied %d job(s)\n", len(specs)); err != nil {
		return err
	}
	for _, warning := range runtimeWarnings {
		a.logger.Info("apply warning", "warning", warning)
	}
	if len(orphanedSpecs) > 0 {
		names := make([]string, 0, len(orphanedSpecs))
		for _, managed := range orphanedSpecs {
			names = append(names, managed.spec.Name)
		}
		if _, err := fmt.Fprintf(a.stdout, "warning: orphaned managed jobs not present in %s: %s\n", filePath, strings.Join(names, ", ")); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(a.stdout, "warning: run 'summond prune' to remove them"); err != nil {
			return err
		}
	}
	a.logger.Info("apply completed", "jobs", len(specs), "warnings", len(runtimeWarnings))
	return nil
}

func (a *App) runPrune(args []string) error {
	a.logger.Debug("prune start")
	fs := flag.NewFlagSet("prune", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {
		printCommandUsage(a.stdout, "prune")
	}
	yes := fs.Bool("yes", false, "skip confirmation")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	filePath, err := optionalConfigPath(fs.Args(), "prune")
	if err != nil {
		return err
	}
	installed, err := a.boot.IsInstalled()
	if err != nil {
		return err
	}
	if !installed {
		return errors.New("prune requires install to be run first")
	}

	desiredSpecs, err := config.LoadFile(filePath)
	if err != nil {
		return err
	}
	pruneSpecs, err := a.orphanedManagedJobs(desiredSpecs)
	if err != nil {
		return err
	}
	if len(pruneSpecs) == 0 {
		_, err := fmt.Fprintln(a.stdout, "no jobs to prune")
		return err
	}

	if _, err := fmt.Fprintln(a.stdout, "jobs to prune:"); err != nil {
		return err
	}
	for _, managed := range pruneSpecs {
		if _, err := fmt.Fprintf(a.stdout, "- %s\n", managed.spec.Name); err != nil {
			return err
		}
	}

	if !*yes {
		approved, err := a.confirm("prune will remove these Summond-managed jobs. Continue? [y/N]: ")
		if err != nil {
			return err
		}
		if !approved {
			_, err = fmt.Fprintln(a.stdout, "prune cancelled")
			return err
		}
	}

	for _, managed := range pruneSpecs {
		spec := managed.spec
		_ = a.runner.Bootout(spec)
		if _, err := managed.store.Remove(spec.Name); err != nil {
			if spec.Target == job.TargetDaemon && errors.Is(err, os.ErrPermission) {
				approved, promptErr := a.confirmWithDefault("removing daemon jobs requires sudo. Retry with sudo? [Y/n]: ", true)
				if promptErr != nil {
					return promptErr
				}
				if approved {
					if err := a.removeDaemonSpecWithSudo(spec); err != nil {
						return err
					}
					continue
				}
			}
			return err
		}
	}
	_, err = fmt.Fprintf(a.stdout, "pruned %d job(s)\n", len(pruneSpecs))
	return err
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
	if _, err := fmt.Fprintln(writer, "NAME\tTARGET\tSCHEDULE\tENABLED\tSTATUS"); err != nil {
		return err
	}
	for _, managed := range managedSpecs {
		spec := managed.spec
		_, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%t\t%s\n", spec.Name, spec.Target, describeTriggerOrSchedule(spec), spec.Enabled, describeLastRunStatus(spec))
		if err != nil {
			return err
		}
	}
	return writer.Flush()
}

func (a *App) runInspect(args []string) error {
	a.logger.Debug("inspect start", "args", args)
	if isHelpArg(args) {
		printCommandUsage(a.stdout, "inspect")
		return nil
	}
	if len(args) != 1 {
		return errors.New("inspect requires a job name")
	}
	managed, err := a.loadManagedSpec(args[0])
	if err != nil {
		return err
	}
	spec := managed.spec
	_, err = fmt.Fprintf(a.stdout, "name: %s\nlabel: %s\ntarget: %s\ntrigger: %s\nenabled: %t\nplist: %s\nstdout: %s\nstderr: %s\nruntime: %s\nstatus: %s\nruns: %d (success=%d failure=%d)\n",
		spec.Name, spec.Label, spec.Target, describeTriggerOrSchedule(spec), spec.Enabled, spec.PlistPath, spec.StdoutPath, spec.StderrPath, spec.RuntimeBinaryPath, describeLastRunStatus(spec), spec.RunCount, spec.SuccessCount, spec.FailureCount)
	if err != nil {
		return err
	}
	if spec.LastError != "" {
		if _, err := fmt.Fprintf(a.stdout, "last_error: %s\n", spec.LastError); err != nil {
			return err
		}
	}
	if len(spec.WatchPaths) > 0 {
		if _, err := fmt.Fprintf(a.stdout, "watch_paths: %s\n", strings.Join(spec.WatchPaths, ", ")); err != nil {
			return err
		}
	}
	if len(spec.RecentRuns) > 0 {
		if _, err := fmt.Fprintln(a.stdout, "recent_runs:"); err != nil {
			return err
		}
		for _, run := range spec.RecentRuns {
			if _, err := fmt.Fprintf(a.stdout, "  %s\n", describeExecutionRecord(run)); err != nil {
				return err
			}
		}
	}
	if spec.Command != "" {
		_, err = fmt.Fprintf(a.stdout, "command: %s %s\n", spec.Command, strings.Join(spec.Args, " "))
		return err
	}
	shellDisplay := strings.TrimRight(spec.ShellCommand, "\r\n")
	_, err = fmt.Fprintf(a.stdout, "shell:\n  %s\n", strings.ReplaceAll(shellDisplay, "\n", "\n  "))
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
		return ExitError{Code: exitCode}
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
	fmt.Fprintln(stdout, "  prune [flags] [file]       Remove managed jobs missing from a TOML file")
	fmt.Fprintln(stdout, "  list                       List managed jobs")
	fmt.Fprintln(stdout, "  inspect <name>             Show job details")
	fmt.Fprintln(stdout, "  logs [flags] <name>        Print job logs")
	fmt.Fprintln(stdout, "  exec <name>                Run a managed job immediately")
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
		fmt.Fprintln(stdout, "  summond apply [file]")
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Apply jobs from a TOML config file.")
		fmt.Fprintln(stdout, "If [file] is omitted, reads ./summond.toml.")
	case "prune":
		fmt.Fprintln(stdout, "Usage:")
		fmt.Fprintln(stdout, "  summond prune [flags] [file]")
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Remove managed jobs that are missing from a TOML config file.")
		fmt.Fprintln(stdout, "If [file] is omitted, reads ./summond.toml.")
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Flags:")
		fmt.Fprintln(stdout, "  --yes                      Automatically accept prompts")
	case "list":
		fmt.Fprintln(stdout, "Usage:")
		fmt.Fprintln(stdout, "  summond list")
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "List all managed jobs with target, schedule, enabled state, and status.")
	case "inspect":
		fmt.Fprintln(stdout, "Usage:")
		fmt.Fprintln(stdout, "  summond inspect <name>")
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Show the stored configuration and recent run state for a managed job.")
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
	if spec.ShellCommand != "" {
		cmd = exec.Command("/bin/bash", "-lc", shellPreamble+spec.ShellCommand)
	} else {
		cmd = exec.Command(spec.Command, spec.Args...)
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

func (a *App) applySpec(store *state.Store, spec job.Spec) (job.Spec, string, error) {
	a.logger.Debug("reconciling job", "name", spec.Name, "target", spec.Target, "trigger", spec.Trigger, "schedule", spec.Schedule.Kind)
	installed, err := store.Install(spec)
	if err != nil {
		return job.Spec{}, "", err
	}
	if installed.Enabled {
		if loaded, err := a.isLoaded(installed); err != nil {
			a.logger.Debug("load-state check failed", "name", installed.Name, "error", err)
			return installed, fmt.Sprintf("%s: load-state check failed: %v", installed.Name, err), nil
		} else if loaded {
			a.logger.Debug("job already loaded, bootout before bootstrap", "name", installed.Name)
			if err := a.runner.Bootout(installed); err != nil {
				a.logger.Debug("bootout before bootstrap failed", "name", installed.Name, "error", err)
				return installed, fmt.Sprintf("%s: bootout before bootstrap failed: %v", installed.Name, err), nil
			}
		}
		if err := a.runner.Bootstrap(installed); err != nil {
			a.logger.Debug("bootstrap failed", "name", installed.Name, "error", err)
			return installed, fmt.Sprintf("%s: bootstrap failed: %v", installed.Name, err), nil
		}
		if err := a.verifyLoadedJob(installed); err != nil {
			a.logger.Debug("loaded job verification failed", "name", installed.Name, "error", err)
			return installed, fmt.Sprintf("%s: loaded job verification failed: %v", installed.Name, err), nil
		}
		return installed, "", nil
	}
	a.logger.Debug("job disabled, bootout", "name", installed.Name)
	if err := a.runner.Bootout(installed); err != nil {
		a.logger.Debug("bootout failed", "name", installed.Name, "error", err)
		return installed, fmt.Sprintf("%s: bootout failed: %v", installed.Name, err), nil
	}
	return installed, "", nil
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
		installed.Enabled,
	); err != nil {
		return job.Spec{}, err
	}
	return installed, nil
}

func daemonRequiredDirs(store *state.Store, spec job.Spec) []string {
	dirs := []string{
		store.Paths().Home,
		store.JobsFilePath(),
		store.LogsDir(),
		filepath.Dir(store.RuntimeBinaryPath()),
		filepath.Dir(spec.PlistPath),
	}
	for _, path := range []string{spec.StdoutPath, spec.StderrPath} {
		if path != "" {
			dirs = append(dirs, filepath.Dir(path))
		}
	}
	seen := map[string]struct{}{}
	var unique []string
	for _, dir := range dirs {
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		unique = append(unique, dir)
	}
	return unique
}

func (a *App) reconcileDaemonRuntime(spec job.Spec) string {
	_ = spec
	return ""
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

func (osPrivilegedOperator) InstallDaemonSpecWithSudo(dirs []string, runtimeSource string, runtimeDest string, plistSource string, plistDest string, metadataSource string, metadataDest string, enabled bool) error {
	script := `
dir_count="$1"
enabled="$2"
shift 2
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
if [ "$enabled" = "true" ]; then
  launchctl bootstrap system "$plist_dest"
fi
`
	args := []string{fmt.Sprintf("%d", len(dirs)), fmt.Sprintf("%t", enabled)}
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
