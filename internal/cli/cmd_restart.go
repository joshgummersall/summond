package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/joshgummersall/summond/internal/job"
	"github.com/spf13/cobra"
)

func (a *App) newRestartCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "restart <name>",
		Short: "Restart a job through launchd",
		Long: `Restart a job through launchd with "launchctl kickstart -k".

If the job is running, launchd stops it and starts a new instance; otherwise
launchd starts it. The interrupted run is recorded as killed. Use this for
login and boot jobs, which launchd starts only once at load time.`,
		Example: `  summond restart my-login-job`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			managed, err := a.loadManagedSpec(name)
			if err != nil {
				return err
			}
			spec := managed.spec

			printText, printErr := a.runner.Print(spec)
			if printErr != nil {
				if isMissingServiceError(printErr) {
					return fmt.Errorf("%s is not loaded in launchd; run summond apply", spec.Name)
				}
				return fmt.Errorf("launchctl print: %w", printErr)
			}

			if spec.LastStartedAt != nil && spec.LastFinishedAt == nil && strings.Contains(printText, "state = running") {
				now := time.Now()
				record := job.ExecutionRecord{StartedAt: *spec.LastStartedAt, FinishedAt: &now, Error: "killed"}
				if err := managed.store.RecordExecutionFinish(spec.Name, record); err != nil {
					return fmt.Errorf("record interrupted run: %w", err)
				}
			}

			if err := a.runner.Kickstart(spec); err != nil {
				return fmt.Errorf("restart job: %w", err)
			}
			fmt.Fprintf(a.stdout, "restarted %s\n", spec.Name)
			return nil
		},
	}
}
