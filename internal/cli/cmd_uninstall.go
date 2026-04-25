package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/joshgummersall/summond/internal/bootstrap"
	"github.com/joshgummersall/summond/internal/job"
	"github.com/spf13/cobra"
)

func (a *App) printUninstallSummary(result bootstrap.UninstallResult, removedJobs int, needsNewsyslogWarning bool) error {
	_ = result
	_ = removedJobs
	if needsNewsyslogWarning {
		a.logger.Info("uninstall warning", "newsyslog_install_path", result.NewsyslogInstallPath)
	}
	return nil
}

func (a *App) printUninstallPlan(specs []managedSpec) error {
	if _, err := fmt.Fprintln(a.stdout, "uninstall will:"); err != nil {
		return err
	}
	for _, managed := range specs {
		spec := managed.spec
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
	if _, err := fmt.Fprintf(a.stdout, "- remove managed agent state directory: %s\n", a.store.Paths().Home); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.stdout, "- remove managed daemon state directory: %s\n", a.daemonStore.Paths().Home); err != nil {
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
		Short: "Remove all Summond-managed jobs and setup",
		Long: `Remove all Summond-managed jobs, plists, logs, state directories, and the newsyslog config.

This removes every job managed by summond (both agents and daemons), boots them out of
launchd, deletes their plists and log files, and removes the summond state directories.
Daemon jobs and system-owned files may require sudo.

This is a destructive operation. Run 'summond list' first to see what will be removed.`,
		Example: `  summond uninstall
  summond uninstall --yes`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a.logger.Debug("uninstall start")
			managedSpecs, err := a.listManagedJobs()
			if err != nil {
				return err
			}
			if err := a.printUninstallPlan(managedSpecs); err != nil {
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
			a.logger.Debug("loaded managed jobs", "count", len(managedSpecs))
			var sudoPlists []string
			var sudoMetadata []string
			removedJobs := 0
			for _, managed := range managedSpecs {
				spec := managed.spec
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
				if _, err := managed.store.Remove(spec.Name); err != nil {
					if spec.Target == job.TargetDaemon && errors.Is(err, os.ErrPermission) {
						for _, path := range managedCleanupPaths(managed.store, spec) {
							sudoMetadata = appendIfMissing(sudoMetadata, path)
						}
					} else if !errors.Is(err, os.ErrNotExist) {
						return err
					}
				}
				removedJobs++
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
			needsSudoDaemonHome := false
			if err := os.RemoveAll(a.daemonStore.Paths().Home); err != nil && !errors.Is(err, os.ErrNotExist) {
				if errors.Is(err, os.ErrPermission) {
					needsSudoDaemonHome = true
				} else {
					return err
				}
			}

			if len(sudoPlists) > 0 || len(sudoMetadata) > 0 || needsSudoNewsyslog || needsSudoDaemonHome {
				approved, err := a.confirmWithDefault("some system-owned files require sudo to remove. Retry with sudo? [Y/n]: ", true)
				if err != nil {
					return err
				}
				if approved {
					if len(sudoPlists) > 0 || len(sudoMetadata) > 0 || needsSudoDaemonHome {
						var extraPaths []string
						if needsSudoDaemonHome {
							extraPaths = append(extraPaths, a.daemonStore.Paths().Home)
						}
						if err := a.priv.RemoveDaemonArtifactsWithSudo(sudoPlists, sudoMetadata, extraPaths); err != nil {
							return err
						}
					}
					if needsSudoNewsyslog {
						if err := a.boot.UninstallNewsyslogWithSudo(&uninstallResult); err != nil {
							return err
						}
						needsSudoNewsyslog = false
					}
				}
			}

			if err := a.printUninstallSummary(uninstallResult, removedJobs, needsSudoNewsyslog); err != nil {
				return err
			}
			if (len(sudoPlists) > 0 || len(sudoMetadata) > 0 || needsSudoDaemonHome) && !uninstallResult.UsedSudo {
				return a.printUninstallManual(uninstallResult, sudoPlists)
			}
			a.logger.Info("uninstall completed", "removed_jobs", removedJobs)
			return nil

		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation prompt")
	return cmd
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
