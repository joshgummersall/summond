package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func (a *App) newPlistCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "plist <name>",
		Short: "Print the job plist file",
		Long: `Print the managed job's installed launchd plist file.

Useful for verifying the generated plist or passing it to launchctl directly.
Daemon plists are installed to /Library/LaunchDaemons/; agent plists to ~/Library/LaunchAgents/.`,
		Example: `  summond plist my-job`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			a.logger.Debug("plist start", "name", name)
			managed, err := a.loadManagedSpec(name)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(managed.spec.PlistPath)
			if err != nil {
				return fmt.Errorf("read job plist: %w", err)
			}
			_, err = a.stdout.Write(data)
			return err
		},
	}
}
