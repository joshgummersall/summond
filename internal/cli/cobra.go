package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/standardlabs/summond/internal/job"
)

func (a *App) newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "summond",
		Short:         "Manage friendly launchd jobs",
		SilenceUsage:  true,
		SilenceErrors: true,
		Long:          "summond manages friendly launchd jobs with native LaunchAgents and LaunchDaemons.",
	}
	verbosity := root.PersistentFlags().CountP("verbose", "v", "increase verbosity")
	root.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		a.configureLogger(*verbosity)
	}
	root.CompletionOptions.DisableDefaultCmd = true

	root.AddCommand(
		a.newInstallCommand(),
		a.newUninstallCommand(),
		a.newApplyCommand(),
		a.newAddCommand(),
		a.newRemoveCommand(),
		a.newListCommand(),
		a.newStateCommand(),
		a.newPlistCommand(),
		a.newLogsCommand(),
		a.newExecCommand(),
		a.newEnvCommand(),
		a.newCDCommand(),
		a.newVersionCommand(),
	)
	return root
}

func (a *App) newInstallCommand() *cobra.Command {
	var overwrite bool
	var skipNewsyslog bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Scaffold config and newsyslog setup",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runInstall(installOptions{
				overwrite:     overwrite,
				skipNewsyslog: skipNewsyslog,
			})
		},
	}
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "overwrite generated files")
	cmd.Flags().BoolVar(&skipNewsyslog, "skip-newsyslog", false, "skip generating and installing newsyslog config")
	return cmd
}

func (a *App) newUninstallCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove Summond-managed jobs and setup",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runUninstall()
		},
	}
}

func (a *App) newApplyCommand() *cobra.Command {
	var prune bool
	cmd := &cobra.Command{
		Use:   "apply [file]",
		Short: "Apply jobs from a TOML file",
		Long:  "Apply jobs from a TOML config file.\nIf [file] is omitted, reads ./summond.toml.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath := "summond.toml"
			if len(args) == 1 {
				filePath = args[0]
			}
			return a.runApply(applyOptions{
				filePath: filePath,
				prune:    prune,
			})
		},
	}
	cmd.Flags().BoolVar(&prune, "prune", false, "remove managed jobs missing from the config without prompting")
	return cmd
}

func (a *App) newAddCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <target> ...",
		Short: "Add and install a managed job",
		Long:  "Add and install a managed job.\n\nRun 'summond add <target> --help' for target-specific usage.",
	}
	cmd.AddCommand(
		a.newAddTargetCommand("agent"),
		a.newAddTargetCommand("daemon"),
	)
	return cmd
}

func (a *App) newAddTargetCommand(target string) *cobra.Command {
	var schedule string
	var workingDir string
	var hour int
	var minute int
	var weekday int
	var intervalMinutes int
	targetLabel := "LaunchAgent"
	exampleSchedule := "login"
	exampleBinary := "/bin/echo hello --flag"
	stdinName := "my-script"
	if target == "daemon" {
		targetLabel = "LaunchDaemon"
		exampleSchedule = "boot"
		exampleBinary = "/usr/local/bin/task"
		stdinName = "my-daemon-script"
	}
	cmd := &cobra.Command{
		Use:   fmt.Sprintf("%s <name> [flags] -- <command> [args]", target),
		Short: fmt.Sprintf("Add a %s job", targetLabel),
		Long:  fmt.Sprintf("Add and install a %s job without editing summond.toml.", targetLabel),
		Args:  cobra.MinimumNArgs(1),
		Example: fmt.Sprintf("summond add %s my-job --schedule %s -- %s\nsummond add %s %s --schedule daily <<'EOF'\necho hi\nEOF",
			target, exampleSchedule, exampleBinary, target, stdinName),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := addOptions{
				name:            args[0],
				schedule:        schedule,
				workingDir:      workingDir,
				hour:            hour,
				hourSet:         cmd.Flags().Changed("hour"),
				minute:          minute,
				minuteSet:       cmd.Flags().Changed("minute"),
				weekday:         weekday,
				weekdaySet:      cmd.Flags().Changed("weekday"),
				intervalMinutes: intervalMinutes,
				intervalSet:     cmd.Flags().Changed("interval-minutes"),
			}
			if target == "daemon" {
				opts.target = job.TargetDaemon
			} else {
				opts.target = job.TargetAgent
			}
			if len(args) > 1 {
				opts.commandArgs = append([]string(nil), args[1:]...)
			} else if stdinHasData(a.stdin) {
				data, err := io.ReadAll(a.stdin)
				if err != nil {
					return fmt.Errorf("read shell command from stdin: %w", err)
				}
				opts.stdinScript = strings.TrimRight(string(data), "\n")
			}
			return a.runAddTarget(opts)
		},
	}
	cmd.Flags().StringVar(&schedule, "schedule", "", "schedule kind")
	cmd.Flags().StringVar(&workingDir, "working-dir", "", "working directory")
	cmd.Flags().IntVar(&hour, "hour", 0, "schedule hour")
	cmd.Flags().IntVar(&minute, "minute", 0, "schedule minute")
	cmd.Flags().IntVar(&weekday, "weekday", 0, "schedule weekday")
	cmd.Flags().IntVar(&intervalMinutes, "interval-minutes", 0, "interval schedule minutes")
	return cmd
}

func (a *App) newRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a managed job and all state",
		Long:  "Remove the managed job, its plist, logs, and persisted state.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runRemove(removeOptions{name: args[0]})
		},
	}
}

func (a *App) newListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List managed jobs",
		Long:  "List all managed jobs with target, schedule, and status.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runList()
		},
	}
}

func (a *App) newStateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "state <name>",
		Short: "Print the job state.json file",
		Long:  "Print the managed job's persisted state.json file.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runState(namedJobOptions{name: args[0]})
		},
	}
}

func (a *App) newPlistCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "plist <name>",
		Short: "Print the job plist file",
		Long:  "Print the managed job's installed plist file.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runPlist(namedJobOptions{name: args[0]})
		},
	}
}

func (a *App) newLogsCommand() *cobra.Command {
	var lineCount int
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs <name>",
		Short: "Print job logs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runLogs(logsOptions{name: args[0], lineCount: lineCount, follow: follow})
		},
	}
	cmd.Flags().IntVarP(&lineCount, "lines", "n", 40, "number of lines to print")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow appended data")
	return cmd
}

func (a *App) newExecCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "exec <name>",
		Short: "Run a managed job immediately",
		Long:  "Run a managed job immediately and record its execution result.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runExec(namedJobOptions{name: args[0]})
		},
	}
}

func (a *App) newEnvCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "env",
		Short: "Edit the shared job environment file",
		Long:  "Open the shared shell env file in $EDITOR or $VISUAL.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runEnv()
		},
	}
}

func (a *App) newCDCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "cd <name>",
		Short: "Open a shell in the job state directory",
		Long:  "Open a subshell in the job's state directory (interactive), or print a cd command suitable for eval (non-interactive).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runCd(namedJobOptions{name: args[0]})
		},
	}
}

func (a *App) newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the CLI version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Fprintf(a.stdout, "summond %s\n", version)
			return err
		},
	}
}
