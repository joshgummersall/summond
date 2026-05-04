package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/joshgummersall/summond/internal/job"
	"github.com/spf13/cobra"
)

func (a *App) newKillCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "kill <name>",
		Short: "Stop a running job and reconcile its state",
		Long: `Stop a running job and reconcile its recorded state.

If the job process is still alive (managed by launchd), it is sent SIGTERM and
its state is marked as killed. If the job is recorded as running but the process
is already gone, the state is reconciled: if launchd reports a clean exit code
the run is recorded as successful; otherwise it is marked as failed.`,
		Example: `  summond kill my-job`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			managed, err := a.loadManagedSpec(name)
			if err != nil {
				return err
			}
			spec := managed.spec

			if spec.LastStartedAt == nil || spec.LastFinishedAt != nil {
				fmt.Fprintf(a.stdout, "%s is not running\n", spec.Name)
				return nil
			}

			now := time.Now()
			startedAt := *spec.LastStartedAt
			record := job.ExecutionRecord{StartedAt: startedAt, FinishedAt: &now}

			printText, printErr := a.runner.Print(spec)
			processRunning := printErr == nil && strings.Contains(printText, "state = running")

			if processRunning {
				if err := a.runner.Stop(spec); err != nil && !isMissingServiceError(err) {
					return fmt.Errorf("stop job: %w", err)
				}
				record.Error = "killed"
				fmt.Fprintf(a.stdout, "killed %s\n", spec.Name)
			} else {
				exitCode := parsePrintExitCode(printText)
				record.ExitCode = exitCode
				if exitCode != nil && *exitCode == 0 {
					fmt.Fprintf(a.stdout, "reconciled %s (exit 0)\n", spec.Name)
				} else if exitCode != nil {
					fmt.Fprintf(a.stdout, "reconciled %s (exit %d)\n", spec.Name, *exitCode)
				} else {
					fmt.Fprintf(a.stdout, "reconciled %s\n", spec.Name)
				}
			}

			if err := managed.store.RecordExecutionFinish(spec.Name, record); err != nil {
				return fmt.Errorf("reconcile state: %w", err)
			}
			return nil
		},
	}
}

// parsePrintExitCode extracts "last exit code = N" from launchctl print output.
func parsePrintExitCode(text string) *int {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "last exit code = ") {
			val := strings.TrimPrefix(line, "last exit code = ")
			if n, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
				return &n
			}
		}
	}
	return nil
}
