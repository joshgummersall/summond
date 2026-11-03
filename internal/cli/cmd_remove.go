package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/joshgummersall/summond/internal/job"
	"github.com/joshgummersall/summond/internal/state"
	"github.com/spf13/cobra"
)

func (a *App) newRemoveCommand() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a managed job and all state",
		Long: `Remove a managed job, booting it out of launchd and deleting its plist, logs, and persisted state.

summond removes agent jobs; sudo summond removes daemon jobs.
Use 'summond list' to see job names.`,
		Example: `  summond remove my-job
  summond remove --dry-run my-job`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			a.logger.Debug("remove start", "name", name)
			spec, err := a.loadManagedSpec(name)
			if err != nil {
				return err
			}
			if dryRun {
				return a.printRemovePlan(spec)
			}
			if err := a.removeManagedSpec(spec); err != nil {
				return err
			}
			_, err = fmt.Fprintf(a.stdout, "removed %s\n", spec.Name)
			return err

		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what remove would do without changing job state")
	return cmd
}

// removeManagedSpec boots spec out of launchd and deletes its plist and
// state. A bootout failure is ignored because the job may not be loaded.
func (a *App) removeManagedSpec(spec job.Spec) error {
	_ = a.runner.Bootout(spec)
	if _, err := a.store.Remove(spec.Name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (a *App) printRemovePlan(spec job.Spec) error {
	if _, err := fmt.Fprintln(a.stdout, "remove will:"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.stdout, "- boot out managed job: %s (%s)\n", spec.Name, spec.PlistPath); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.stdout, "- remove managed plist: %s\n", spec.PlistPath); err != nil {
		return err
	}
	for _, path := range managedCleanupPaths(a.store, spec) {
		if _, err := fmt.Fprintf(a.stdout, "- remove managed state/log: %s\n", path); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(a.stdout, "- remove managed job directory: %s\n", a.store.JobDirForSpec(spec)); err != nil {
		return err
	}
	_, err := fmt.Fprintln(a.stdout, "dry run: no changes made")
	return err
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
