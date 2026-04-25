package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/joshgummersall/summond/internal/job"
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
// command. Single quotes inside the value are escaped as '\''.
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
its normal schedule. stdout and stderr are written to the terminal. The execution is
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

		},
	}
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
