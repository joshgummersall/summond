package cli

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func (a *App) newListCommand() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List managed jobs",
		Long: `List all managed jobs with their target, schedule, and last run status.

Output columns:
  NAME      job name (or group.name if the job belongs to a group)
  TARGET    agent or daemon
  SCHEDULE  schedule kind and parameters, or trigger type
  STATUS    never / running / ok <timestamp> / exit <code> <timestamp>

Use --json to output a JSON array of job objects instead of the table.`,
		Example: `  summond list
  summond list --json
  summond list --json | jq '.[].name'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a.logger.Debug("list start")
			specs, err := a.listManagedJobs()
			if err != nil {
				return err
			}

			if jsonOutput {
				data, err := json.MarshalIndent(specs, "", "  ")
				if err != nil {
					return fmt.Errorf("marshal jobs: %w", err)
				}
				_, err = fmt.Fprintln(a.stdout, string(data))
				return err
			}

			if len(specs) == 0 {
				_, err = fmt.Fprintln(a.stdout, "no managed jobs")
				return err
			}
			writer := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
			if _, err := fmt.Fprintln(writer, "NAME\tTARGET\tSCHEDULE\tSTATUS"); err != nil {
				return err
			}
			for _, spec := range specs {
				_, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", spec.Name, spec.Target, describeTriggerOrSchedule(spec), describeLastRunStatus(spec))
				if err != nil {
					return err
				}
			}
			return writer.Flush()

		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output jobs as a JSON array")
	return cmd
}
