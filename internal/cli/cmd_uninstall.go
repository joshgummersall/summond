package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/joshgummersall/summond/internal/bootstrap"
	"github.com/joshgummersall/summond/internal/job"
	"github.com/spf13/cobra"
)

func (a *App) printUninstallPlan(specs []job.Spec) error {
	if _, err := fmt.Fprintln(a.stdout, "uninstall will:"); err != nil {
		return err
	}
	for _, spec := range specs {
		if _, err := fmt.Fprintf(a.stdout, "- boot out managed job: %s (%s)\n", spec.Name, spec.PlistPath); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(a.stdout, "- remove managed plist: %s\n", spec.PlistPath); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(a.stdout, "- remove generated newsyslog config: %s\n", a.boot.GeneratedNewsyslogPath()); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.stdout, "- remove system newsyslog config: %s\n", a.boot.SystemNewsyslogPath()); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.stdout, "- remove managed %s state directory: %s\n", a.scopeTarget(), a.store.Paths().Home); err != nil {
		return err
	}
	return nil
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (a *App) newUninstallCommand() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove all Summond-managed jobs and setup in this scope",
		Long: `Remove all Summond-managed jobs, plists, logs, the state directory, and the newsyslog config.

summond uninstall removes agent jobs and agent state. sudo summond uninstall removes
daemon jobs and daemon state. Each run boots its jobs out of launchd and deletes their
plists, log files, and the summond state directory for that scope.

This is a destructive operation. Run 'summond list' first to see what will be removed.`,
		Example: `  summond uninstall
  summond uninstall --yes`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a.logger.Debug("uninstall start")
			specs, err := a.listManagedJobs()
			if err != nil {
				return err
			}
			if err := a.printUninstallPlan(specs); err != nil {
				return err
			}
			if !yes {
				approved, err := a.confirmWithDefault("Proceed with uninstall? [y/N]: ", false)
				if err != nil {
					return err
				}
				if !approved {
					_, err = fmt.Fprintln(a.stdout, "uninstall cancelled")
					return err
				}
			}
			a.logger.Debug("loaded managed jobs", "count", len(specs))
			for _, spec := range specs {
				if err := a.removeManagedSpec(spec); err != nil {
					return err
				}
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

			if needsSudoNewsyslog {
				approved, err := a.confirmWithDefault("removing the newsyslog config requires sudo. Retry with sudo? [Y/n]: ", true)
				if err != nil {
					return err
				}
				if approved {
					if err := a.boot.UninstallNewsyslogWithSudo(&uninstallResult); err != nil {
						return err
					}
				} else {
					if err := a.printUninstallManual(uninstallResult); err != nil {
						return err
					}
				}
			}
			a.logger.Info("uninstall completed", "removed_jobs", len(specs))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation prompt")
	return cmd
}

func (a *App) printUninstallManual(result bootstrap.UninstallResult) error {
	_, err := fmt.Fprintf(a.stdout, "manual cleanup: sudo rm -f %s\n", result.NewsyslogInstallPath)
	return err
}
