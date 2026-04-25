package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"

	"github.com/spf13/cobra"
)

func (a *App) newLogsCommand() *cobra.Command {
	var lineCount int
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs <name>",
		Short: "Print job logs",
		Long: `Print logs from the last execution of a managed job.

By default, only output from the most recent execution is shown. Use -n to show
the last N lines of the full log file instead. Use -f to stream new output as it
is appended, similar to 'tail -f'. Press Ctrl-C to stop following.

Log files are managed by newsyslog and rotated automatically. If the job has never
run, the log files may not exist yet.`,
		Example: `  summond logs my-job
  summond logs my-job -n 100
  summond logs my-job -f`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			lastRun := !cmd.Flags().Changed("lines")

			a.logger.Debug("logs start", "name", name, "follow", follow, "lines", lineCount, "lastRun", lastRun)
			if lineCount < 0 {
				return errors.New("logs requires -n >= 0")
			}
			managed, err := a.loadManagedSpec(name)
			if err != nil {
				return err
			}
			spec := managed.spec
			type logTarget struct {
				logTailTarget
				offset int64
			}
			var targets []logTarget
			if spec.StdoutPath != "" {
				targets = append(targets, logTarget{logTailTarget: logTailTarget{path: spec.StdoutPath, output: a.stdout, name: "stdout"}, offset: spec.LastStdoutOffset})
			}
			if spec.StderrPath != "" {
				targets = append(targets, logTarget{logTailTarget: logTailTarget{path: spec.StderrPath, output: a.stderr, name: "stderr"}, offset: spec.LastStderrOffset})
			}
			if len(targets) == 0 {
				return errors.New("no log path configured")
			}
			if !follow {
				for _, target := range targets {
					if lastRun {
						if err := a.runTailFromOffset(target.logTailTarget, target.offset); err != nil {
							return err
						}
					} else {
						if err := a.runTail(target.logTailTarget, lineCount, false); err != nil {
							return err
						}
					}
				}
				return nil
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			errCh := make(chan error, len(targets))
			for _, target := range targets {
				target := target
				go func() {
					if lastRun {
						errCh <- a.runTailFromOffsetContext(ctx, target.logTailTarget, target.offset, true)
					} else {
						errCh <- a.runTailContext(ctx, target.logTailTarget, lineCount, true)
					}
				}()
			}
			var firstErr error
			for range targets {
				if err := <-errCh; err != nil && firstErr == nil && !errors.Is(err, context.Canceled) {
					firstErr = err
					cancel()
				}
			}
			return firstErr

		},
	}
	cmd.Flags().IntVarP(&lineCount, "lines", "n", 40, "number of lines to print (disables last-run mode)")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "stream appended output (like tail -f); press Ctrl-C to stop")
	return cmd
}

func logsTailArgs(lineCount int, follow bool, path string) []string {
	args := []string{"-n", fmt.Sprintf("%d", lineCount)}
	if follow {
		args = append(args, "-f")
	}
	return append(args, path)
}

type logTailTarget struct {
	path   string
	output io.Writer
	name   string
}

func (a *App) runTail(target logTailTarget, lineCount int, follow bool) error {
	return a.runTailContext(context.Background(), target, lineCount, follow)
}

func (a *App) runTailContext(ctx context.Context, target logTailTarget, lineCount int, follow bool) error {
	tailArgs := logsTailArgs(lineCount, follow, target.path)
	a.logger.Debug("reading log", "path", target.path, "stream", target.name, "follow", follow, "lines", lineCount)
	cmd := exec.CommandContext(ctx, "tail", tailArgs...)
	cmd.Stdout = target.output
	cmd.Stderr = a.stderr
	return cmd.Run()
}

func (a *App) runTailFromOffset(target logTailTarget, offset int64) error {
	return a.runTailFromOffsetContext(context.Background(), target, offset, false)
}

func (a *App) runTailFromOffsetContext(ctx context.Context, target logTailTarget, offset int64, follow bool) error {
	// tail -c +N prints from byte N (1-based), so offset+1 skips the first `offset` bytes.
	args := []string{"-c", fmt.Sprintf("+%d", offset+1)}
	if follow {
		args = append(args, "-f")
	}
	args = append(args, target.path)
	a.logger.Debug("reading log from offset", "path", target.path, "stream", target.name, "offset", offset, "follow", follow)
	cmd := exec.CommandContext(ctx, "tail", args...)
	cmd.Stdout = target.output
	cmd.Stderr = a.stderr
	return cmd.Run()
}
