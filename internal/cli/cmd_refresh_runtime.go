package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// newRefreshRuntimeCommand replaces this scope's runtime copy of summond with
// the running binary. The cask runs it after install and upgrade so jobs pick
// up the new version without a manual apply. It is a no-op for a scope that
// has no state yet, so installing the cask never creates state.
func (a *App) newRefreshRuntimeCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "refresh-runtime",
		Short:  "Replace this scope's runtime copy of summond with this binary",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			home := a.store.Paths().Home
			if _, err := os.Stat(home); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					a.logger.Info("no state for this scope; nothing to refresh", "home", home)
					return nil
				}
				return err
			}
			source, err := os.Executable()
			if err != nil {
				return fmt.Errorf("resolve current executable: %w", err)
			}
			path, err := a.store.PrepareRuntimeBinary(source)
			if err != nil {
				return err
			}
			a.logger.Info("refreshed runtime binary", "path", path)
			return nil
		},
	}
}
