package cli

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/joshgummersall/summond/internal/bootstrap"
	"github.com/joshgummersall/summond/internal/config"
	"github.com/joshgummersall/summond/internal/job"
	"github.com/joshgummersall/summond/internal/launchd"
	"github.com/joshgummersall/summond/internal/state"
)

const version = "0.4.0"

type App struct {
	stdin  io.Reader
	stdout io.Writer
	store  *state.Store
	runner launchd.Runner
	boot   *bootstrap.Manager
	priv   privilegedOperator
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
		store:  state.NewStore(paths),
		runner: launchd.LaunchCtl{},
		boot:   bootstrap.NewManager(paths, bootstrap.OSInstaller{}),
		priv:   osPrivilegedOperator{},
	}
	return app.Run(args)
}

func NewApp(stdin io.Reader, stdout io.Writer, store *state.Store, runner launchd.Runner, boot *bootstrap.Manager) *App {
	return &App{stdin: stdin, stdout: stdout, store: store, runner: runner, boot: boot, priv: osPrivilegedOperator{}}
}

func (a *App) Run(args []string) error {
	if len(args) == 0 {
		printUsage(a.stdout)
		return nil
	}

	switch args[0] {
	case "install":
		return a.runInstall(args[1:])
	case "uninstall":
		return a.runUninstall(args[1:])
	case "add":
		return a.runAdd(args[1:])
	case "apply":
		return a.runApply(args[1:])
	case "list":
		return a.runList(args[1:])
	case "inspect":
		return a.runInspect(args[1:])
	case "enable":
		return a.runEnableDisable(args[1:], true)
	case "disable":
		return a.runEnableDisable(args[1:], false)
	case "start":
		return a.runStart(args[1:])
	case "stop":
		return a.runStop(args[1:])
	case "restart":
		return a.runRestart(args[1:])
	case "remove":
		return a.runRemove(args[1:])
	case "logs":
		return a.runLogs(args[1:])
	case "version":
		_, err := fmt.Fprintf(a.stdout, "summond %s\n", version)
		return err
	case "help", "-h", "--help":
		printUsage(a.stdout)
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func (a *App) runInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config-path", "", "starter config path")
	force := fs.Bool("force", false, "overwrite generated files")
	skipNewsyslog := fs.Bool("skip-newsyslog", false, "skip generating newsyslog config")
	installNewsyslog := fs.Bool("install-newsyslog", true, "install newsyslog config")
	noPrompt := fs.Bool("no-prompt", false, "disable sudo retry prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		return errors.New("install does not accept positional arguments")
	}

	result, err := a.boot.Install(bootstrap.Options{
		ConfigPath:       *configPath,
		InstallNewsyslog: *installNewsyslog,
		SkipNewsyslog:    *skipNewsyslog,
		Force:            *force,
	})
	if err != nil {
		var permissionErr *bootstrap.PermissionError
		if errors.As(err, &permissionErr) && !*noPrompt {
			approved, promptErr := a.confirm("newsyslog install requires sudo. Retry with sudo? [Y/n]: ")
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
	return a.printInstallSummary(result)
}

func (a *App) runUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config-path", "", "starter config path")
	yes := fs.Bool("yes", false, "skip confirmation")
	noPrompt := fs.Bool("no-prompt", false, "disable sudo retry prompt")
	if err := fs.Parse(args); err != nil {
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

	uninstallResult, uninstallErr := a.boot.Uninstall(*configPath)
	needsSudoNewsyslog := false
	if uninstallErr != nil {
		var permissionErr *bootstrap.PermissionError
		if errors.As(uninstallErr, &permissionErr) {
			needsSudoNewsyslog = true
		} else {
			return uninstallErr
		}
	}

	if err := os.RemoveAll(a.store.Paths().Home); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if (len(sudoPlists) > 0 || needsSudoNewsyslog) && !*noPrompt {
		approved, err := a.confirm("some system-owned files require sudo to remove. Retry with sudo? [Y/n]: ")
		if err != nil {
			return err
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
			}
		}
	}

	if err := a.printUninstallSummary(uninstallResult, removedJobs, len(sudoPlists) > 0 || needsSudoNewsyslog); err != nil {
		return err
	}
	if (len(sudoPlists) > 0 || needsSudoNewsyslog) && *noPrompt {
		return a.printUninstallManual(uninstallResult, sudoPlists)
	}
	if (len(sudoPlists) > 0 || needsSudoNewsyslog) && !uninstallResult.UsedSudo && len(sudoPlists) > 0 {
		return a.printUninstallManual(uninstallResult, sudoPlists)
	}
	if needsSudoNewsyslog && !uninstallResult.UsedSudo {
		return a.printUninstallManual(uninstallResult, sudoPlists)
	}
	return nil
}

func (a *App) runAdd(args []string) error {
	if len(args) == 0 {
		return errors.New("add requires a job name")
	}
	name := ""
	if !strings.HasPrefix(args[0], "-") {
		name = args[0]
		args = args[1:]
	}

	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	target := fs.String("target", string(job.TargetAgent), "agent or daemon")
	schedule := fs.String("schedule", "", "hourly, daily, weekly, login, boot, interval, calendar")
	command := fs.String("command", "", "absolute executable path")
	shellCommand := fs.String("shell", "", "shell command")
	workingDir := fs.String("working-dir", "", "working directory")
	minute := fs.Int("minute", 0, "calendar minute")
	hour := fs.Int("hour", 0, "calendar hour")
	weekday := fs.Int("weekday", 0, "launchd weekday 1-7")
	day := fs.Int("day", 0, "calendar day")
	month := fs.Int("month", 0, "calendar month")
	intervalMinutes := fs.Int("interval-minutes", 0, "interval minutes")
	enabled := fs.Bool("enabled", true, "enable the job after install")
	stdoutPath := fs.String("stdout-path", "", "stdout log path")
	stderrPath := fs.String("stderr-path", "", "stderr log path")
	envPairs := multiValueFlag{}
	fs.Var(&envPairs, "env", "KEY=VALUE environment variable")

	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if name == "" {
		if len(rest) == 0 {
			return errors.New("add requires a job name")
		}
		name = rest[0]
		rest = rest[1:]
	}
	if name == "" {
		return errors.New("add requires a job name")
	}
	spec := job.Spec{
		Name:         name,
		Target:       job.Target(*target),
		Command:      *command,
		ShellCommand: *shellCommand,
		WorkingDir:   *workingDir,
		Schedule: job.Schedule{
			Kind:            job.ScheduleKind(*schedule),
			IntervalMinutes: *intervalMinutes,
			Minute:          *minute,
			Hour:            *hour,
			Weekday:         *weekday,
			Day:             *day,
			Month:           *month,
		},
		Enabled:     *enabled,
		StdoutPath:  *stdoutPath,
		StderrPath:  *stderrPath,
		Environment: envPairs.Map(),
	}
	if len(rest) > 0 {
		spec.Args = rest
	}

	installed, err := a.store.Install(spec)
	if err != nil {
		return err
	}
	if installed.Enabled {
		if err := a.runner.Bootstrap(installed); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(a.stdout, "installed %s (%s)\n", installed.Name, installed.Label)
	return err
}

func (a *App) runApply(args []string) error {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	filePath := fs.String("f", "", "TOML config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *filePath == "" {
		return errors.New("apply requires -f <config>")
	}
	specs, err := config.LoadFile(*filePath)
	if err != nil {
		return err
	}
	var runtimeWarnings []string
	for _, spec := range specs {
		installed, err := a.store.Install(spec)
		if err != nil {
			return err
		}
		if installed.Enabled {
			if err := a.runner.Bootout(installed); err != nil {
				runtimeWarnings = append(runtimeWarnings, fmt.Sprintf("%s: bootout before bootstrap failed: %v", installed.Name, err))
			}
			if err := a.runner.Bootstrap(installed); err != nil {
				runtimeWarnings = append(runtimeWarnings, fmt.Sprintf("%s: bootstrap failed: %v", installed.Name, err))
				continue
			}
			if err := a.verifyLoadedJob(installed); err != nil {
				runtimeWarnings = append(runtimeWarnings, fmt.Sprintf("%s: loaded job verification failed: %v", installed.Name, err))
			}
		} else {
			if err := a.runner.Bootout(installed); err != nil {
				runtimeWarnings = append(runtimeWarnings, fmt.Sprintf("%s: bootout failed: %v", installed.Name, err))
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
	return nil
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
	if spec.ShellCommand != "" {
		expected = append(expected, spec.ShellCommand)
	} else {
		expected = append(expected, spec.Command)
		expected = append(expected, spec.Args...)
	}
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
	for _, spec := range specs {
		_, err := fmt.Fprintf(a.stdout, "%s\t%s\t%s\tenabled=%t\n", spec.Name, spec.Target, describeTriggerOrSchedule(spec), spec.Enabled)
		if err != nil {
			return err
		}
	}
	return nil
}

func (a *App) runInspect(args []string) error {
	if len(args) != 1 {
		return errors.New("inspect requires a job name")
	}
	spec, err := a.store.Load(args[0])
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(a.stdout, "name: %s\nlabel: %s\ntarget: %s\ntrigger: %s\nenabled: %t\nplist: %s\nstdout: %s\nstderr: %s\n",
		spec.Name, spec.Label, spec.Target, describeTriggerOrSchedule(spec), spec.Enabled, spec.PlistPath, spec.StdoutPath, spec.StderrPath)
	if err != nil {
		return err
	}
	if len(spec.WatchPaths) > 0 {
		if _, err := fmt.Fprintf(a.stdout, "watch_paths: %s\n", strings.Join(spec.WatchPaths, ", ")); err != nil {
			return err
		}
	}
	if spec.Command != "" {
		_, err = fmt.Fprintf(a.stdout, "command: %s %s\n", spec.Command, strings.Join(spec.Args, " "))
		return err
	}
	_, err = fmt.Fprintf(a.stdout, "shell: %s\n", spec.ShellCommand)
	return err
}

func (a *App) runEnableDisable(args []string, enabled bool) error {
	if len(args) != 1 {
		return errors.New("enable/disable requires a job name")
	}
	spec, err := a.store.UpdateEnabled(args[0], enabled)
	if err != nil {
		return err
	}
	if enabled {
		if err := a.runner.Bootstrap(spec); err != nil {
			return err
		}
		_, err = fmt.Fprintf(a.stdout, "enabled %s\n", spec.Name)
		return err
	}
	if err := a.runner.Bootout(spec); err != nil {
		return err
	}
	_, err = fmt.Fprintf(a.stdout, "disabled %s\n", spec.Name)
	return err
}

func (a *App) runStart(args []string) error {
	spec, err := a.requireSingleSpec(args, "start")
	if err != nil {
		return err
	}
	if err := a.runner.Kickstart(spec); err != nil {
		return err
	}
	_, err = fmt.Fprintf(a.stdout, "started %s\n", spec.Name)
	return err
}

func (a *App) runStop(args []string) error {
	spec, err := a.requireSingleSpec(args, "stop")
	if err != nil {
		return err
	}
	if err := a.runner.Stop(spec); err != nil {
		return err
	}
	_, err = fmt.Fprintf(a.stdout, "stopped %s\n", spec.Name)
	return err
}

func (a *App) runRestart(args []string) error {
	spec, err := a.requireSingleSpec(args, "restart")
	if err != nil {
		return err
	}
	if err := a.runner.Kickstart(spec); err != nil {
		return err
	}
	_, err = fmt.Fprintf(a.stdout, "restarted %s\n", spec.Name)
	return err
}

func (a *App) runRemove(args []string) error {
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
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	follow := fs.Bool("follow", false, "follow the log file")
	stream := fs.String("stream", "stdout", "stdout or stderr")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("logs requires a job name")
	}
	spec, err := a.store.Load(fs.Args()[0])
	if err != nil {
		return err
	}
	path := spec.StdoutPath
	if *stream == "stderr" {
		path = spec.StderrPath
	}
	if path == "" {
		return errors.New("no log path configured")
	}
	_, err = fmt.Fprintf(a.stdout, "%s\n", path)
	if err != nil || !*follow {
		return err
	}
	cmd := exec.Command("tail", "-n", "40", "-f", path)
	cmd.Stdout = a.stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
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
	fmt.Fprintln(stdout, "  summond <command> [arguments]")
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "Commands:")
	fmt.Fprintln(stdout, "  install                    Scaffold config and newsyslog setup")
	fmt.Fprintln(stdout, "  uninstall                  Remove Summond-managed jobs and setup")
	fmt.Fprintln(stdout, "  add <name> [args...]       Create or update a managed job")
	fmt.Fprintln(stdout, "  apply -f <file>            Apply jobs from a TOML file")
	fmt.Fprintln(stdout, "  list                       List managed jobs")
	fmt.Fprintln(stdout, "  inspect <name>             Show job details")
	fmt.Fprintln(stdout, "  enable <name>              Enable and load a job")
	fmt.Fprintln(stdout, "  disable <name>             Disable and unload a job")
	fmt.Fprintln(stdout, "  start <name>               Kickstart a job")
	fmt.Fprintln(stdout, "  stop <name>                Stop a running job")
	fmt.Fprintln(stdout, "  restart <name>             Restart a job")
	fmt.Fprintln(stdout, "  remove <name>              Remove a managed job")
	fmt.Fprintln(stdout, "  logs [--follow] <name>     Show log path or follow logs")
	fmt.Fprintln(stdout, "  version                    Print the CLI version")
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

func ExampleConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "summond", "summond.toml")
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
	lines := []string{
		fmt.Sprintf("config: %s (%s)", result.ConfigPath, result.ConfigStatus),
		fmt.Sprintf("newsyslog source: %s (%s)", result.NewsyslogGeneratedPath, result.NewsyslogGenerateStatus),
	}
	if result.NewsyslogGenerateStatus != "skipped" {
		status := "not installed"
		if result.NewsyslogInstalled {
			status = "installed"
			if result.UsedSudo {
				status += " via sudo"
			}
		}
		lines = append(lines, fmt.Sprintf("newsyslog install: %s -> %s (%s)", result.NewsyslogGeneratedPath, result.NewsyslogInstallPath, status))
	}
	lines = append(lines, fmt.Sprintf("next: edit %s and run summond apply -f %s", result.ConfigPath, result.ConfigPath))
	for _, line := range lines {
		if _, err := fmt.Fprintln(a.stdout, line); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) printUninstallSummary(result bootstrap.UninstallResult, removedJobs int, hadPrivileged bool) error {
	lines := []string{
		fmt.Sprintf("jobs removed: %d", removedJobs),
		fmt.Sprintf("config: %s (%s)", result.ConfigPath, result.ConfigStatus),
		fmt.Sprintf("newsyslog source: %s (%s)", result.NewsyslogGeneratedPath, result.NewsyslogGenerateStatus),
	}
	status := result.NewsyslogInstallStatus
	if status == "" {
		if hadPrivileged {
			status = "pending sudo removal"
		} else {
			status = "absent"
		}
	}
	if result.UsedSudo && status == "removed" {
		status += " via sudo"
	}
	lines = append(lines, fmt.Sprintf("newsyslog install: %s (%s)", result.NewsyslogInstallPath, status))
	lines = append(lines, fmt.Sprintf("state home: %s (removed)", a.store.Paths().Home))
	for _, line := range lines {
		if _, err := fmt.Fprintln(a.stdout, line); err != nil {
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
