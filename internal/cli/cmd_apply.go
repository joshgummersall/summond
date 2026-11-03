package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/joshgummersall/summond/internal/config"
	"github.com/joshgummersall/summond/internal/job"
	"github.com/spf13/cobra"
)

func (a *App) newApplyCommand() *cobra.Command {
	var prune bool
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "apply [file]",
		Short: "Apply jobs from a TOML file",
		Long: `Apply jobs from a TOML config file, creating or updating launchd jobs as needed.

If [file] is omitted, reads ./summond.toml.

The top-level 'target' key sets the target of every job in the file: "agent" (the
default) or "daemon". Apply agent configs with 'summond apply' and daemon configs with
'sudo summond apply'; a config for the other scope is an error.

Jobs are identified by their name within a group. The group defaults to a hash of the
config file path, or the 'group' key at the top of the config file. Apply only manages
jobs in groups present in the config — jobs in other groups are never pruned.

If the config file removes a job that summond previously managed (within the same group),
apply will prompt to prune it. Use --prune to remove orphaned jobs without prompting.

Exits with code 1 if any job fails to apply, even if other jobs succeeded.`,
		Example: `  summond apply
  summond apply path/to/jobs.toml
  summond apply --prune
  summond apply --dry-run`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath := "summond.toml"
			if len(args) == 1 {
				filePath = args[0]
			}

			a.logger.Debug("apply start")
			installed, err := a.boot.IsInstalled()
			if err != nil {
				return err
			}
			if !installed {
				return errors.New("apply requires install to be run first")
			}
			file, err := config.Load(filePath)
			if err != nil {
				return err
			}
			if file.Target != a.scopeTarget() {
				return scopeMismatchError(file.Target, "apply "+filePath)
			}
			specs := file.Specs
			a.logger.Debug("loaded config", "path", filePath, "jobs", len(specs))
			orphanedSpecs, err := a.orphanedManagedJobs(specs)
			if err != nil {
				return err
			}
			if dryRun {
				return a.printApplyPlan(specs, orphanedSpecs, prune)
			}
			runtimeSource, err := os.Executable()
			if err != nil {
				return fmt.Errorf("resolve current executable: %w", err)
			}
			runtimePath, err := a.store.PrepareRuntimeBinary(runtimeSource)
			if err != nil {
				return err
			}
			applied := 0
			var failures []string
			for _, spec := range specs {
				spec.EnvironmentFilePath = a.store.EnvFilePath()
				spec.RuntimeBinaryPath = runtimePath
				if _, err := a.applySpec(spec); err != nil {
					failures = append(failures, fmt.Sprintf("%s: %v", spec.Name, err))
					continue
				}
				applied++
			}
			if _, err := fmt.Fprintf(a.stdout, "applied %d job(s)\n", applied); err != nil {
				return err
			}
			if len(failures) > 0 {
				if _, err := fmt.Fprintf(a.stdout, "failed %d job(s):\n", len(failures)); err != nil {
					return err
				}
				for _, failure := range failures {
					if _, err := fmt.Fprintf(a.stdout, "- %s\n", failure); err != nil {
						return err
					}
				}
				return ExitError{Code: 1}
			}
			if len(orphanedSpecs) > 0 {
				pruned, err := a.pruneManagedSpecs(orphanedSpecs, prune)
				if err != nil {
					return err
				}
				if pruned > 0 {
					if _, err := fmt.Fprintf(a.stdout, "pruned %d job(s)\n", pruned); err != nil {
						return err
					}
				}
			}
			a.logger.Info("apply completed", "jobs", applied)
			return nil

		},
	}
	cmd.Flags().BoolVar(&prune, "prune", false, "remove orphaned managed jobs (within the same group) without prompting")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what apply would do without changing job state")
	return cmd
}

func (a *App) pruneManagedSpecs(pruneSpecs []job.Spec, autoApprove bool) (int, error) {
	if len(pruneSpecs) == 0 {
		return 0, nil
	}
	if _, err := fmt.Fprintln(a.stdout, "jobs to prune:"); err != nil {
		return 0, err
	}
	for _, spec := range pruneSpecs {
		if _, err := fmt.Fprintf(a.stdout, "- %s\n", spec.Name); err != nil {
			return 0, err
		}
	}

	if !autoApprove {
		approved, err := a.confirm("apply will remove these Summond-managed jobs. Continue? [y/N]: ")
		if err != nil {
			return 0, err
		}
		if !approved {
			_, err = fmt.Fprintln(a.stdout, "prune cancelled")
			return 0, err
		}
	}

	for _, spec := range pruneSpecs {
		if err := a.removeManagedSpec(spec); err != nil {
			return 0, err
		}
	}
	return len(pruneSpecs), nil
}

func (a *App) orphanedManagedJobs(desiredSpecs []job.Spec) ([]job.Spec, error) {
	desired := make(map[string]struct{}, len(desiredSpecs))
	groupFilter := make(map[string]struct{}, len(desiredSpecs))
	for _, spec := range desiredSpecs {
		desired[spec.ManagedKey()] = struct{}{}
		groupFilter[spec.Group] = struct{}{}
	}
	existingSpecs, err := a.listManagedJobs()
	if err != nil {
		return nil, err
	}
	var orphaned []job.Spec
	for _, spec := range existingSpecs {
		if _, ok := groupFilter[spec.Group]; !ok {
			continue
		}
		if _, ok := desired[spec.ManagedKey()]; !ok {
			orphaned = append(orphaned, spec)
		}
	}
	return orphaned, nil
}

func (a *App) applySpec(spec job.Spec) (job.Spec, error) {
	a.logger.Debug("reconciling job", "name", spec.Name, "target", spec.Target, "trigger", spec.Trigger, "schedule", spec.Schedule.Kind)
	previous, hadPrevious, previousLoaded, err := a.captureApplyState(spec.Name)
	if err != nil {
		return job.Spec{}, err
	}
	installed, err := a.store.Install(spec)
	if err != nil {
		return job.Spec{}, err
	}
	if loaded, err := a.isLoaded(installed); err != nil {
		a.logger.Debug("load-state check failed", "name", installed.Name, "error", err)
		return installed, a.rollbackApplyFailure(installed, previous, hadPrevious, previousLoaded, false, fmt.Errorf("load-state check failed: %w", err))
	} else if loaded {
		a.logger.Debug("job already loaded, bootout before bootstrap", "name", installed.Name)
		if err := a.runner.Bootout(installed); err != nil {
			a.logger.Debug("bootout before bootstrap failed", "name", installed.Name, "error", err)
			return installed, a.rollbackApplyFailure(installed, previous, hadPrevious, previousLoaded, false, fmt.Errorf("bootout before bootstrap failed: %w", err))
		}
	}
	if err := a.runner.Bootstrap(installed); err != nil {
		a.logger.Debug("bootstrap failed", "name", installed.Name, "error", err)
		return installed, a.rollbackApplyFailure(installed, previous, hadPrevious, previousLoaded, false, fmt.Errorf("bootstrap failed: %w", err))
	}
	if err := a.verifyLoadedJob(installed); err != nil {
		a.logger.Debug("loaded job verification failed", "name", installed.Name, "error", err)
		return installed, a.rollbackApplyFailure(installed, previous, hadPrevious, previousLoaded, true, fmt.Errorf("loaded job verification failed: %w", err))
	}
	return installed, nil
}

func (a *App) captureApplyState(name string) (job.Spec, bool, bool, error) {
	previous, err := a.store.Load(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return job.Spec{}, false, false, nil
		}
		return job.Spec{}, false, false, err
	}
	loaded, err := a.isLoaded(previous)
	if err != nil {
		return job.Spec{}, false, false, fmt.Errorf("load-state check failed before update: %w", err)
	}
	return previous, true, loaded, nil
}

func (a *App) rollbackApplyFailure(installed job.Spec, previous job.Spec, hadPrevious bool, previousLoaded bool, unloadInstalled bool, applyErr error) error {
	if rollbackErr := a.rollbackApplyState(installed, previous, hadPrevious, previousLoaded, unloadInstalled); rollbackErr != nil {
		return fmt.Errorf("%v (rollback failed: %w)", applyErr, rollbackErr)
	}
	return applyErr
}

func (a *App) rollbackApplyState(installed job.Spec, previous job.Spec, hadPrevious bool, previousLoaded bool, unloadInstalled bool) error {
	if unloadInstalled {
		if err := a.runner.Bootout(installed); err != nil && !isMissingServiceError(err) {
			return fmt.Errorf("bootout failed job: %w", err)
		}
	}
	if hadPrevious {
		restored, err := a.store.Install(previous)
		if err != nil {
			return fmt.Errorf("restore previous managed state: %w", err)
		}
		if previousLoaded {
			if err := a.runner.Bootstrap(restored); err != nil {
				return fmt.Errorf("restore previous launchd job: %w", err)
			}
		}
		return nil
	}
	if _, err := a.store.Remove(installed.Name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove failed managed state: %w", err)
	}
	return nil
}

func (a *App) printApplyPlan(specs []job.Spec, orphanedSpecs []job.Spec, prune bool) error {
	if _, err := fmt.Fprintln(a.stdout, "apply will:"); err != nil {
		return err
	}
	changes := 0
	for _, spec := range specs {
		action, err := a.describeApplyAction(spec)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(a.stdout, "- %s %s job: %s (%s)\n", action, spec.Target, spec.Name, describeTriggerOrSchedule(spec)); err != nil {
			return err
		}
		changes++
	}
	for _, spec := range orphanedSpecs {
		action := "prompt to prune"
		if prune {
			action = "prune"
		}
		if _, err := fmt.Fprintf(a.stdout, "- %s managed job: %s (%s)\n", action, spec.Name, spec.Target); err != nil {
			return err
		}
		changes++
	}
	if changes == 0 {
		if _, err := fmt.Fprintln(a.stdout, "- no job changes"); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(a.stdout, "dry run: no changes made")
	return err
}

func (a *App) describeApplyAction(spec job.Spec) (string, error) {
	if _, err := a.store.Load(spec.Name); err == nil {
		return "update", nil
	} else if errors.Is(err, os.ErrNotExist) {
		return "create", nil
	} else {
		return "", err
	}
}
