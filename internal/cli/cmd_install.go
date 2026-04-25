package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/joshgummersall/summond/internal/bootstrap"
	"github.com/spf13/cobra"
)

func (a *App) printInstallSummary(result bootstrap.Result) error {
	_ = result
	return nil
}

func (a *App) printInstallPlan(opts bootstrap.Options) error {
	configPath, err := resolveCurrentConfigPath()
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(a.stdout, "install will:"); err != nil {
		return err
	}
	configAction, err := describeInstallAction(configPath, opts.Overwrite)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.stdout, "- %s starter config: %s\n", configAction, configPath); err != nil {
		return err
	}
	installMarkerPath := filepath.Join(a.store.Paths().Home, ".installed")
	installMarkerAction, err := describeInstallAction(installMarkerPath, true)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.stdout, "- %s install marker: %s\n", installMarkerAction, installMarkerPath); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.stdout, "- manage agent state under: %s\n", a.store.Paths().Home); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.stdout, "- manage daemon state under: %s\n", a.daemonStore.Paths().Home); err != nil {
		return err
	}
	if opts.SkipNewsyslog {
		if _, err := fmt.Fprintln(a.stdout, "- skip newsyslog generation and system install"); err != nil {
			return err
		}
	} else {
		generatedNewsyslogAction, err := describeInstallAction(a.boot.GeneratedNewsyslogPath(), opts.Overwrite)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(a.stdout, "- %s generated newsyslog config: %s\n", generatedNewsyslogAction, a.boot.GeneratedNewsyslogPath()); err != nil {
			return err
		}
		systemNewsyslogAction, err := describeInstallAction(a.boot.SystemNewsyslogPath(), true)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(a.stdout, "- %s system newsyslog config: %s\n", systemNewsyslogAction, a.boot.SystemNewsyslogPath()); err != nil {
			return err
		}
	}
	if opts.Overwrite {
		if _, err := fmt.Fprintln(a.stdout, "- overwrite generated files if they already exist"); err != nil {
			return err
		}
	}
	return nil
}

func resolveCurrentConfigPath() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve current working directory: %w", err)
	}
	return filepath.Join(wd, "summond.toml"), nil
}

func describeInstallAction(path string, overwrite bool) (string, error) {
	_, err := os.Stat(path)
	if err == nil {
		if overwrite {
			return "overwrite", nil
		}
		return "leave unchanged", nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return "create", nil
	}
	return "", err
}

func (a *App) newInstallCommand() *cobra.Command {
	var overwrite bool
	var skipNewsyslog bool
	var yes bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Scaffold config and newsyslog setup",
		Long: `Set up summond in the current directory and configure system log rotation.

Install creates the following files and directories:
  - summond.toml      starter config in the current directory (skipped if it already exists, unless --overwrite)
  - ~/.summond/       agent job state directory
  - /var/db/summond/  daemon job state directory
  - newsyslog config  log rotation config installed to /etc/newsyslog.d/ (requires sudo)

Run 'summond apply' after install to load jobs from summond.toml.
Run 'summond uninstall' to remove all managed jobs and state.`,
		Example: `  summond install
  summond install --overwrite
  summond install --skip-newsyslog`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a.logger.Debug("install start")
			installOpts := bootstrap.Options{
				SkipNewsyslog: skipNewsyslog,
				Overwrite:     overwrite,
			}
			if err := a.printInstallPlan(installOpts); err != nil {
				return err
			}
			if !yes {
				approved, err := a.confirmWithDefault("Proceed with install? [y/N]: ", false)
				if err != nil {
					return err
				}
				if !approved {
					_, err = fmt.Fprintln(a.stdout, "install cancelled")
					return err
				}
			}

			result, err := a.boot.Install(installOpts)
			if err != nil {
				a.logger.Debug("install failed", "error", err)
				var permissionErr *bootstrap.PermissionError
				if errors.As(err, &permissionErr) && !skipNewsyslog {
					approved, promptErr := a.confirmWithDefault("system setup requires sudo (newsyslog + daemon state). Retry with sudo? [Y/n]: ", true)
					if promptErr != nil {
						return promptErr
					}
					if approved {
						if retryErr := a.boot.InstallNewsyslogWithSudo(&result); retryErr != nil {
							return retryErr
						}
						if !result.DaemonStateInitialized {
							_ = a.boot.InitDaemonStateWithSudo(&result)
						}
						return a.printInstallSummary(result)
					}
					if summaryErr := a.printInstallSummary(result); summaryErr != nil {
						return summaryErr
					}
					_, summaryErr := fmt.Fprintf(a.stdout, "install manually with: sudo install -m 0644 %s %s\n", result.NewsyslogGeneratedPath, result.NewsyslogInstallPath)
					return summaryErr
				}
				if errors.As(err, &permissionErr) {
					if summaryErr := a.printInstallSummary(result); summaryErr != nil {
						return summaryErr
					}
					_, summaryErr := fmt.Fprintf(a.stdout, "install manually with: sudo install -m 0644 %s %s\n", result.NewsyslogGeneratedPath, result.NewsyslogInstallPath)
					return summaryErr
				}
				return err
			}
			// If daemon state wasn't initialized (requires sudo for /Library paths), prompt and retry.
			if !result.DaemonStateInitialized {
				approved, promptErr := a.confirmWithDefault("daemon state directory requires sudo. Initialize now? [Y/n]: ", true)
				if promptErr != nil {
					return promptErr
				}
				if approved {
					if initErr := a.boot.InitDaemonStateWithSudo(&result); initErr != nil {
						a.logger.Debug("daemon state init failed", "error", initErr)
					}
				}
			}
			a.logger.Info("install completed")
			return a.printInstallSummary(result)

		},
	}
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "overwrite generated files if they already exist (summond.toml, newsyslog config)")
	cmd.Flags().BoolVar(&skipNewsyslog, "skip-newsyslog", false, "skip generating and installing the newsyslog log rotation config")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation prompt")
	return cmd
}
