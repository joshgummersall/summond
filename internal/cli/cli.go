package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
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

const version = "0.4.0"
const shellPreamble = "set -euo pipefail\n"

type ExitError struct {
	Code int
}

func (e ExitError) Error() string {
	return ""
}

type App struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	store  *state.Store
	runner launchd.Runner
	boot   *bootstrap.Manager
	priv   privilegedOperator
	logger *slog.Logger
}

type privilegedOperator interface {
	RemoveFileWithSudo(path string) error
	BootoutDaemonWithSudo(plistPath string) error
}

type osPrivilegedOperator struct{}

func Run(args []string, stdout io.Writer) error {
	paths, err := state.DiscoverPaths()
	if err != nil {
		return err
	}
	app := &App{
		stdin:  os.Stdin,
		stdout: stdout,
		stderr: os.Stderr,
		store:  state.NewStore(paths),
		runner: launchd.LaunchCtl{},
		boot:   bootstrap.NewManager(paths, bootstrap.OSInstaller{}),
		priv:   osPrivilegedOperator{},
	}
	return app.Run(args)
}

func NewApp(stdin io.Reader, stdout io.Writer, store *state.Store, runner launchd.Runner, boot *bootstrap.Manager) *App {
	return &App{stdin: stdin, stdout: stdout, stderr: io.Discard, store: store, runner: runner, boot: boot, priv: osPrivilegedOperator{}}
}

func (a *App) Run(args []string) error {
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
	case "remove":
		return a.runRemove(remaining[1:])
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
	yes := fs.Bool("yes", false, "automatically accept prompts")
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

	result, err := a.boot.Install(bootstrap.Options{
		SkipNewsyslog: *skipNewsyslog,
		Overwrite:     *overwrite,
	})
	if err != nil {
		a.logger.Debug("install failed", "error", err)
		var permissionErr *bootstrap.PermissionError
		if errors.As(err, &permissionErr) && !*skipNewsyslog {
			approved := *yes
			if !approved {
				var promptErr error
				approved, promptErr = a.confirm("newsyslog install requires sudo. Retry with sudo? [Y/n]: ")
				if promptErr != nil {
					return promptErr
				}
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
	yes := fs.Bool("yes", false, "skip confirmation")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if len(fs.Args()) != 0 {
		return errors.New("uninstall does not accept positional arguments")
	}
	if !*yes {
		approved, err := a.confirm("uninstall will remove Summond-managed jobs, logs, plists, and config. Continue? [y/N]: ")
		if err != nil {
			return err
		}
		if !approved {
			_, err = fmt.Fprintln(a.stdout, "uninstall cancelled")
			return err
		}
	}

	specs, err := a.store.List()
	if err != nil {
		return err
	}
	a.logger.Debug("loaded managed jobs", "count", len(specs))
	var sudoPlists []string
	removedJobs := 0
	for _, spec := range specs {
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

	if len(sudoPlists) > 0 || needsSudoNewsyslog {
		approved := *yes
		if !approved {
			var err error
			approved, err = a.confirm("some system-owned files require sudo to remove. Retry with sudo? [Y/n]: ")
			if err != nil {
				return err
			}
		}
		if approved {
			for _, plistPath := range sudoPlists {
				_ = a.priv.BootoutDaemonWithSudo(plistPath)
				if err := a.priv.RemoveFileWithSudo(plistPath); err != nil {
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
	if len(sudoPlists) > 0 && !uninstallResult.UsedSudo {
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
	runtimePath, err := a.store.PrepareRuntimeBinary(runtimeSource)
	if err != nil {
		return err
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
	for _, spec := range specs {
		spec.RuntimeBinaryPath = runtimePath
		a.logger.Debug("reconciling job", "name", spec.Name, "trigger", spec.Trigger, "schedule", spec.Schedule.Kind)
		installed, err := a.store.Install(spec)
		if err != nil {
			return err
		}
		if installed.Enabled {
			if loaded, err := a.isLoaded(installed); err != nil {
				runtimeWarnings = append(runtimeWarnings, fmt.Sprintf("%s: load-state check failed: %v", installed.Name, err))
				a.logger.Debug("load-state check failed", "name", installed.Name, "error", err)
			} else if loaded {
				a.logger.Debug("job already loaded, bootout before bootstrap", "name", installed.Name)
				if err := a.runner.Bootout(installed); err != nil {
					runtimeWarnings = append(runtimeWarnings, fmt.Sprintf("%s: bootout before bootstrap failed: %v", installed.Name, err))
					a.logger.Debug("bootout before bootstrap failed", "name", installed.Name, "error", err)
				}
			}
			if err := a.runner.Bootstrap(installed); err != nil {
				runtimeWarnings = append(runtimeWarnings, fmt.Sprintf("%s: bootstrap failed: %v", installed.Name, err))
				a.logger.Debug("bootstrap failed", "name", installed.Name, "error", err)
				continue
			}
			if err := a.verifyLoadedJob(installed); err != nil {
				runtimeWarnings = append(runtimeWarnings, fmt.Sprintf("%s: loaded job verification failed: %v", installed.Name, err))
				a.logger.Debug("loaded job verification failed", "name", installed.Name, "error", err)
			}
		} else {
			a.logger.Debug("job disabled, bootout", "name", installed.Name)
			if err := a.runner.Bootout(installed); err != nil {
				runtimeWarnings = append(runtimeWarnings, fmt.Sprintf("%s: bootout failed: %v", installed.Name, err))
				a.logger.Debug("bootout failed", "name", installed.Name, "error", err)
			}
		}
	}
	if _, err := fmt.Fprintf(a.stdout, "applied %d job(s)\n", len(specs)); err != nil {
		return err
	}
	for _, warning := range runtimeWarnings {
		if _, err := fmt.Fprintf(a.stdout, "warning: %s\n", warning); err != nil {
			return err
		}
	}
	if len(orphanedSpecs) > 0 {
		names := make([]string, 0, len(orphanedSpecs))
		for _, spec := range orphanedSpecs {
			names = append(names, spec.Name)
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
	for _, spec := range pruneSpecs {
		if _, err := fmt.Fprintf(a.stdout, "- %s\n", spec.Name); err != nil {
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

	for _, spec := range pruneSpecs {
		_ = a.runner.Bootout(spec)
		if _, err := a.store.Remove(spec.Name); err != nil {
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
	specs, err := a.store.List()
	if err != nil {
		return err
	}
	if len(specs) == 0 {
		_, err = fmt.Fprintln(a.stdout, "no managed jobs")
		return err
	}
	writer := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "NAME\tTARGET\tSCHEDULE\tENABLED\tSTATUS"); err != nil {
		return err
	}
	for _, spec := range specs {
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
	spec, err := a.store.Load(args[0])
	if err != nil {
		return err
	}
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
	spec, err := a.store.Load(args[0])
	if err != nil {
		return err
	}
	startedAt := time.Now()
	if err := a.store.RecordExecutionStart(spec.Name, startedAt); err != nil {
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
	if err := a.store.RecordExecutionFinish(spec.Name, record); err != nil {
		return err
	}
	if runErr != nil {
		return ExitError{Code: exitCode}
	}
	return nil
}

func (a *App) runRemove(args []string) error {
	a.logger.Debug("remove start", "args", args)
	if isHelpArg(args) {
		printCommandUsage(a.stdout, "remove")
		return nil
	}
	spec, err := a.requireSingleSpec(args, "remove")
	if err != nil {
		return err
	}
	_ = a.runner.Bootout(spec)
	if _, err := a.store.Remove(spec.Name); err != nil {
		return err
	}
	_, err = fmt.Fprintf(a.stdout, "removed %s\n", spec.Name)
	return err
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
	spec, err := a.store.Load(fs.Args()[0])
	if err != nil {
		return err
	}
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

func (a *App) requireSingleSpec(args []string, name string) (job.Spec, error) {
	if len(args) != 1 {
		return job.Spec{}, fmt.Errorf("%s requires a job name", name)
	}
	return a.store.Load(args[0])
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
	fmt.Fprintln(stdout, "  remove <name>              Remove a managed job")
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
		fmt.Fprintln(stdout, "  --yes                      Automatically accept prompts")
		fmt.Fprintln(stdout, "  --overwrite                Overwrite generated files")
		fmt.Fprintln(stdout, "  --skip-newsyslog           Skip generating and installing newsyslog config")
	case "uninstall":
		fmt.Fprintln(stdout, "Usage:")
		fmt.Fprintln(stdout, "  summond uninstall [flags]")
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Flags:")
		fmt.Fprintln(stdout, "  --yes                      Automatically accept prompts")
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
	case "remove":
		fmt.Fprintln(stdout, "Usage:")
		fmt.Fprintln(stdout, "  summond remove <name>")
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Boot out and remove a managed job's plist and metadata.")
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

func (a *App) orphanedManagedJobs(desiredSpecs []job.Spec) ([]job.Spec, error) {
	desired := make(map[string]struct{}, len(desiredSpecs))
	for _, spec := range desiredSpecs {
		desired[spec.Name] = struct{}{}
	}
	existingSpecs, err := a.store.List()
	if err != nil {
		return nil, err
	}
	var orphaned []job.Spec
	for _, spec := range existingSpecs {
		if _, ok := desired[spec.Name]; !ok {
			orphaned = append(orphaned, spec)
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
	_, err := fmt.Fprint(a.stdout, prompt)
	if err != nil {
		return false, err
	}
	reader := bufio.NewReader(a.stdin)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	answer := strings.TrimSpace(strings.ToLower(line))
	return answer == "" || answer == "y" || answer == "yes", nil
}

func (a *App) printInstallSummary(result bootstrap.Result) error {
	_ = result
	return nil
}

func (a *App) printUninstallSummary(result bootstrap.UninstallResult, removedJobs int, needsNewsyslogWarning bool) error {
	_ = result
	_ = removedJobs
	if needsNewsyslogWarning {
		if _, err := fmt.Fprintf(a.stdout, "warning: could not remove system newsyslog config %s\n", result.NewsyslogInstallPath); err != nil {
			return err
		}
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

func (osPrivilegedOperator) RemoveFileWithSudo(path string) error {
	cmd := exec.Command("sudo", "rm", "-f", path)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func (osPrivilegedOperator) BootoutDaemonWithSudo(plistPath string) error {
	cmd := exec.Command("sudo", "launchctl", "bootout", "system", plistPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}
