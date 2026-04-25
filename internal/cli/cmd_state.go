package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func (a *App) newStateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "state <name>",
		Short: "Print the job state JSON",
		Long: `Print the managed job's persisted state as JSON.

The state file contains the job spec, schedule, runtime paths, and execution history
(last run time, exit code, recent runs). Useful for inspecting job configuration or
scripting based on run history.`,
		Example: `  summond state my-job
  summond state my-job | jq '.last_exit_code'`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			a.logger.Debug("state start", "name", name)
			managed, err := a.loadManagedSpec(name)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(managed.store.MetadataPathForSpec(managed.spec))
			if err != nil {
				return fmt.Errorf("read job metadata: %w", err)
			}
			_, err = a.stdout.Write(data)
			return err

		},
	}
}
