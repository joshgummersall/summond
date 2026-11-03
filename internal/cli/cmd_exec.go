package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/joshgummersall/summond/internal/job"
	"github.com/joshgummersall/summond/internal/state"
	"github.com/joshgummersall/summond/internal/xpcevent"
	"github.com/spf13/cobra"
)

func (a *App) environmentFilePath(spec job.Spec) string {
	if spec.EnvironmentFilePath != "" {
		return spec.EnvironmentFilePath
	}
	if spec.Target == job.TargetDaemon {
		return a.daemonStore.EnvFilePath()
	}
	return a.store.EnvFilePath()
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
		fmt.Fprintf(&sb, "export %s=%s\n", k, shellQuote(env[k]))
	}
	return sb.String()
}

// shellQuote wraps s in POSIX single quotes so it is safe to use in a shell
// command. Single quotes inside the value are escaped as '\”.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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

func (a *App) newExecCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "exec <name>",
		Short: "Run a managed job immediately",
		Long: `Run a managed job immediately in the foreground and record its execution result.

The job runs with the same command, working directory, and environment as it would on
its normal schedule. stdout and stderr are written to the terminal and appended to the
job's log files, so 'summond logs' shows manual runs too. The execution is
recorded in the job's state (last run time, exit code, run count).

Exits with the job's exit code. A non-zero exit means the job itself failed, not summond.`,
		Example: `  summond exec my-job`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			a.logger.Debug("exec start", "name", name)
			managed, err := a.loadManagedSpec(name)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) && a.geteuid != nil && a.geteuid() == 0 && os.Getenv("SUDO_USER") != "" {
					return fmt.Errorf("read job metadata: %w (running with sudo uses root-managed jobs; if %s is an agent job, re-run without sudo: summond exec %s)", os.ErrNotExist, name, name)
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
			// A job launched by launchd for an IOKit event must consume the
			// event, or launchd relaunches it every ThrottleInterval forever.
			// Skip for manual runs (parent is a shell, not launchd).
			if spec.Trigger == job.TriggerUSBAttach && os.Getppid() == 1 {
				delivered := xpcevent.ConsumeStream("com.apple.iokit.matching", 3*time.Second)
				a.logger.Debug("consumed iokit matching event", "delivered", delivered)
			}
			if !spec.Concurrency {
				unlock, acquired, err := managed.store.TryLockExecution(spec.Name)
				if err != nil {
					return err
				}
				if !acquired {
					fmt.Fprintf(a.stderr, "summond: %s is already running, skipping this run (concurrency=false)\n", spec.Name)
					return nil
				}
				defer unlock()
			}

			exitCode, runErr, err := a.runAndRecord(spec, managed.store)
			if err != nil {
				return err
			}
			if runErr != nil {
				return ExitError{Code: exitCode, Message: runErr.Error()}
			}
			return nil
		},
	}
}

// runAndRecord runs spec with retries and records the run in store. runErr is
// the job's own failure; err is a failure to record state.
//
// Termination signals are trapped while the job runs, so a stop from launchd
// (restart, kill, logout) still records the run as finished instead of leaving
// it "running" forever. Signals are forwarded to the job and stop retries.
func (a *App) runAndRecord(spec job.Spec, store *state.Store) (exitCode int, runErr error, err error) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sigs)

	changedPaths, newFingerprints := watchChanges(spec, store)
	for _, path := range []string{spec.StdoutPath, spec.StderrPath} {
		if err := rotateLog(path, logRotateMaxBytes); err != nil {
			a.logger.Debug("rotate job log", "path", path, "error", err)
		}
	}
	startedAt := time.Now()
	if err := store.RecordExecutionStart(spec.Name, startedAt); err != nil {
		return 0, nil, err
	}

	maxAttempts := 1 + spec.RetryAttempts
	delay := time.Duration(spec.RetryDelaySeconds) * time.Second
	var received os.Signal
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case received = <-sigs:
			case <-time.After(delay):
			}
			if received != nil {
				break
			}
			delay *= 2
			if spec.RetryMaxDelaySeconds > 0 {
				if cap := time.Duration(spec.RetryMaxDelaySeconds) * time.Second; delay > cap {
					delay = cap
				}
			}
		}
		exitCode, received, runErr = a.executeSpec(spec, changedPaths, attempt, sigs)
		if runErr == nil || received != nil {
			break
		}
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			break
		}
	}

	finishedAt := time.Now()
	record := job.ExecutionRecord{StartedAt: startedAt, FinishedAt: &finishedAt, ExitCode: &exitCode}
	if runErr != nil {
		record.Error = runErr.Error()
	}
	if err := store.RecordExecutionFinish(spec.Name, record); err != nil {
		return exitCode, runErr, err
	}
	if runErr == nil && len(newFingerprints) > 0 {
		_ = store.UpdateWatchFingerprints(spec.Name, newFingerprints)
	}
	return exitCode, runErr, nil
}

// executeSpec runs one attempt of the job. Signals that arrive on sigs while
// the job runs are forwarded to it; the last one is returned as received so
// the caller can stop retrying.
func (a *App) executeSpec(spec job.Spec, changedPaths []string, attempt int, sigs <-chan os.Signal) (exitCode int, received os.Signal, err error) {
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
		"SUMMOND_JOB_NAME":      spec.Name,
		"SUMMOND_JOB_LABEL":     spec.Label,
		"SUMMOND_STATE_DIR":     a.storeForTarget(spec.Target).JobDirForSpec(spec),
		"SUMMOND_CHANGED_PATHS": strings.Join(changedPaths, ":"),
		"SUMMOND_ATTEMPT":       fmt.Sprintf("%d", attempt),
	})
	stdout, closeStdout := a.teeToLog(a.stdout, spec.StdoutPath)
	defer closeStdout()
	stderr, closeStderr := a.teeToLog(a.stderr, spec.StderrPath)
	defer closeStderr()
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = a.stdin
	if err := cmd.Start(); err != nil {
		return 1, nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	forwarded := false
	for {
		select {
		case sig := <-sigs:
			received = sig
			forwardSignal(cmd.Process, sig, !forwarded)
			forwarded = true
		case err := <-done:
			return exitCodeOf(err), received, err
		}
	}
}

// forwardSignal passes sig on to the job. bash defers a trapped SIGTERM until
// its foreground child exits, so signaling only bash would leave a long command
// (such as brew) running until launchd escalates to SIGKILL and the run is never
// recorded. When summond leads its process group, as it does under launchd, the
// first SIGTERM goes to the whole group so every process in the job sees it.
// Our own copy of that signal comes back on sigs, so this happens once; later
// signals go to the direct child only. SIGINT and SIGHUP from a terminal
// already reach the whole foreground group, so they are not re-sent to it.
func forwardSignal(p *os.Process, sig os.Signal, first bool) {
	if first && sig == syscall.SIGTERM && syscall.Getpgrp() == os.Getpid() {
		_ = syscall.Kill(-os.Getpid(), syscall.SIGTERM)
		return
	}
	_ = p.Signal(sig)
}

// exitCodeOf maps a cmd.Wait error to a shell-style exit code: 0 on success,
// 128+N when the process died from signal N, 1 for non-exit errors.
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return 1
	}
	if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return exitErr.ExitCode()
}

// logRotateMaxBytes is the size above which rotateLog rotates a job log.
var logRotateMaxBytes int64 = 5 << 20

// rotateLog copies the log at path to path+".1", replacing any older copy, and
// truncates path to zero when it is larger than max. It copies and truncates
// instead of renaming so that a file launchd already opened (O_APPEND, via the
// plist) keeps writing to path, and teeToLog still sees it as the same file.
func rotateLog(path string, max int64) error {
	if path == "" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.Size() <= max {
		return nil
	}
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".1.tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	_, copyErr := io.Copy(tmp, src)
	closeErr := tmp.Close()
	if err := errors.Join(copyErr, closeErr, os.Chmod(tmpPath, info.Mode().Perm())); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path+".1"); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return os.Truncate(path, 0)
}

// teeToLog also writes w's output to the job log at path, so manual runs land
// in the same log files as launchd runs. It returns w unchanged when w already
// is the log file (launchd redirected stdout/stderr there via the plist).
func (a *App) teeToLog(w io.Writer, path string) (io.Writer, func()) {
	noop := func() {}
	if path == "" {
		return w, noop
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		a.logger.Debug("open job log", "path", path, "error", err)
		return w, noop
	}
	if wf, ok := w.(*os.File); ok {
		wInfo, wErr := wf.Stat()
		fInfo, fErr := f.Stat()
		if wErr == nil && fErr == nil && os.SameFile(wInfo, fInfo) {
			f.Close()
			return w, noop
		}
	}
	return io.MultiWriter(w, f), func() { f.Close() }
}

func pathFingerprint(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size())
}

func watchChanges(spec job.Spec, store *state.Store) (changedPaths []string, newFingerprints map[string]string) {
	if len(spec.WatchPaths) == 0 {
		return nil, nil
	}
	newFingerprints = make(map[string]string, len(spec.WatchPaths))
	for _, p := range spec.WatchPaths {
		newFingerprints[p] = pathFingerprint(p)
	}
	existing, err := store.Load(spec.Name)
	if err != nil || existing.WatchFingerprints == nil {
		return append([]string(nil), spec.WatchPaths...), newFingerprints
	}
	for _, p := range spec.WatchPaths {
		if newFingerprints[p] != existing.WatchFingerprints[p] {
			changedPaths = append(changedPaths, p)
		}
	}
	return changedPaths, newFingerprints
}
