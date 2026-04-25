package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

func (a *App) newCDCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "cd <name>",
		Short: "Open a shell in the job state directory",
		Long: `Open a subshell in the job's state directory, or print a cd command for eval.

When stdout is a terminal (interactive), spawns a new shell ($SHELL or /bin/zsh) with
its working directory set to the job's state directory. Exit the shell to return.

When stdout is not a terminal (e.g. inside $(...) or eval), prints a cd command instead:

  eval $(summond cd my-job)

The job state directory contains the job's metadata JSON, log symlinks, and runtime files.`,
		Example: `  summond cd my-job
  eval $(summond cd my-job)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			managed, err := a.loadManagedSpec(args[0])
			if err != nil {
				return err
			}

			dir, err := managed.store.JobDir(managed.spec.Name)
			if err != nil {
				return err
			}

			// Detect whether stdout is a TTY.
			// If non-TTY (e.g. eval $(summond cd <name>)), print a cd command for the shell to eval.
			// If TTY (interactive), spawn a subshell in the job directory.
			info, err := os.Stdout.Stat()
			if err != nil {
				return fmt.Errorf("stat stdout: %w", err)
			}
			isTTY := (info.Mode() & os.ModeCharDevice) != 0
			if !isTTY {
				_, err := fmt.Fprintf(a.stdout, "cd %s\n", dir)
				return err
			}

			shell := os.Getenv("SHELL")
			if shell == "" {
				shell = "/bin/zsh"
			}

			c := exec.Command(shell)
			c.Dir = dir
			c.Stdin = a.stdin
			c.Stdout = a.stdout
			c.Stderr = a.stderr
			if err := c.Run(); err != nil {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					return ExitError{Code: exitErr.ExitCode()}
				}
				return err
			}

			return nil
		},
	}
}
