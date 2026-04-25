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

Daemon jobs are owned by root and may prompt for sudo to remove system-owned files.
Use 'summond list' to see job names.`,
		Example: `  summond remove my-job
  summond remove --dry-run my-job`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			a.logger.Debug("remove start", "name", name)
			managed, err := a.loadManagedSpec(name)
			if err != nil {
				return err
			}
			if dryRun {
				return a.printRemovePlan(managed)
			}
			if err := a.removeManagedSpec(managed); err != nil {
				return err
			}
			_, err = fmt.Fprintf(a.stdout, "removed %s\n", managed.spec.Name)
			return err

		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what remove would do without changing job state")
	return cmd
}

func (a *App) removeManagedSpec(managed managedSpec) error {
	spec := managed.spec
	needsSudo := false
	if err := a.runner.Bootout(spec); err != nil && spec.Target == job.TargetDaemon {
		needsSudo = true
	}
	if _, err := managed.store.Remove(spec.Name); err != nil {
		if spec.Target == job.TargetDaemon && errors.Is(err, os.ErrPermission) {
			needsSudo = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if needsSudo {
		approved, promptErr := a.confirmWithDefault("removing daemon jobs requires sudo. Retry with sudo? [Y/n]: ", true)
		if promptErr != nil {
			return promptErr
		}
		if approved {
			return a.removeDaemonSpecWithSudo(spec)
		}
	}
	return nil
}

func (a *App) removeDaemonSpecWithSudo(spec job.Spec) error {
	return a.priv.RemoveDaemonArtifactsWithSudo(
		[]string{spec.PlistPath},
		managedCleanupPaths(a.daemonStore, spec),
		[]string{a.daemonStore.JobDirForSpec(spec)},
	)
}

func (a *App) printRemovePlan(managed managedSpec) error {
	spec := managed.spec
	if _, err := fmt.Fprintln(a.stdout, "remove will:"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.stdout, "- boot out managed job: %s (%s)\n", spec.Name, spec.PlistPath); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.stdout, "- remove managed plist: %s\n", spec.PlistPath); err != nil {
		return err
	}
	for _, path := range managedCleanupPaths(managed.store, spec) {
		if _, err := fmt.Fprintf(a.stdout, "- remove managed state/log: %s\n", path); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(a.stdout, "- remove managed job directory: %s\n", managed.store.JobDirForSpec(spec)); err != nil {
		return err
	}
	if spec.Target == job.TargetDaemon {
		if _, err := fmt.Fprintln(a.stdout, "- may require sudo for daemon-owned files"); err != nil {
			return err
		}
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
