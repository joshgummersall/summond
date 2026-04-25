package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"
)

func (a *App) newEnvCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Manage the shared job environment",
		Long: `Manage environment variables shared across all managed jobs.

Variables are stored in a JSON file and injected into every job at runtime before the
job command runs. Use 'set', 'get', and 'list' to manipulate the environment non-interactively.

By default, operates on the agent env file. Use --daemon to target the daemon env file
(stored in /Library/Application Support/summond/env.json).`,
		Example: `  summond env set API_KEY=abc123
  summond env get API_KEY
  summond env list
  summond env --daemon set SVC_TOKEN=xyz`,
	}
	cmd.AddCommand(
		a.newEnvSetCommand(),
		a.newEnvGetCommand(),
		a.newEnvRemoveCommand(),
		a.newEnvListCommand(),
	)
	return cmd
}

func (a *App) newEnvSetCommand() *cobra.Command {
	var daemon bool
	cmd := &cobra.Command{
		Use:   "set KEY=VALUE",
		Short: "Set an environment variable",
		Long: `Add or update an environment variable in the shared env file.

If KEY already exists its value is updated in place. Otherwise a new entry is appended.
Keys must match [A-Za-z_][A-Za-z0-9_]*.`,
		Example: `  summond env set API_KEY=abc123
  summond env set PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin
  summond env set --daemon SVC_TOKEN=xyz`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value, err := parseKeyValue(args[0])
			if err != nil {
				return err
			}

			path, err := a.ensureEnvFile(a.envStore(daemon))
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read env file: %w", err)
			}
			env, err := readEnvJSON(data)
			if err != nil {
				return fmt.Errorf("parse env file: %w", err)
			}
			_, existed := env[key]
			env[key] = value
			data, err = json.MarshalIndent(env, "", "  ")
			if err != nil {
				return fmt.Errorf("encode env file: %w", err)
			}
			if err := a.writeEnvFileContents(path, append(data, '\n')); err != nil {
				return fmt.Errorf("write env file: %w", err)
			}
			if existed {
				_, err = fmt.Fprintf(a.stdout, "updated %s\n", key)
			} else {
				_, err = fmt.Fprintf(a.stdout, "set %s\n", key)
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&daemon, "daemon", false, "operate on the daemon env file instead of the agent env file")
	return cmd
}

func (a *App) newEnvGetCommand() *cobra.Command {
	var daemon bool
	cmd := &cobra.Command{
		Use:     "get KEY",
		Short:   "Print the value of an environment variable",
		Example: `  summond env get API_KEY`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]

			path, err := a.ensureEnvFile(a.envStore(daemon))
			if err != nil {
				return err
			}

			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read env file: %w", err)
			}

			env, err := readEnvJSON(data)
			if err != nil {
				return fmt.Errorf("parse env file: %w", err)
			}

			value, ok := env[key]
			if !ok {
				return fmt.Errorf("key %q not found in env file", key)
			}

			_, err = fmt.Fprintf(a.stdout, "%s\n", value)
			return err
		},
	}
	cmd.Flags().BoolVar(&daemon, "daemon", false, "operate on the daemon env file instead of the agent env file")
	return cmd
}

func (a *App) newEnvRemoveCommand() *cobra.Command {
	var daemon bool
	cmd := &cobra.Command{
		Use:   "remove KEY",
		Short: "Remove an environment variable",
		Long: `Remove an environment variable from the shared env file.

Returns an error if the key does not exist.`,
		Example: `  summond env remove API_KEY
  summond env remove --daemon SVC_TOKEN`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			path, err := a.ensureEnvFile(a.envStore(daemon))
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read env file: %w", err)
			}
			env, err := readEnvJSON(data)
			if err != nil {
				return fmt.Errorf("parse env file: %w", err)
			}
			if _, ok := env[key]; !ok {
				return fmt.Errorf("key %q not found in env file", key)
			}
			delete(env, key)
			data, err = json.MarshalIndent(env, "", "  ")
			if err != nil {
				return fmt.Errorf("encode env file: %w", err)
			}
			if err := a.writeEnvFileContents(path, append(data, '\n')); err != nil {
				return fmt.Errorf("write env file: %w", err)
			}
			_, err = fmt.Fprintf(a.stdout, "removed %s\n", key)
			return err
		},
	}
	cmd.Flags().BoolVar(&daemon, "daemon", false, "operate on the daemon env file instead of the agent env file")
	return cmd
}

func (a *App) newEnvListCommand() *cobra.Command {
	var daemon bool
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List all environment variables",
		Example: `  summond env list`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := a.ensureEnvFile(a.envStore(daemon))
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read env file: %w", err)
			}
			env, err := readEnvJSON(data)
			if err != nil {
				return fmt.Errorf("parse env file: %w", err)
			}
			keys := make([]string, 0, len(env))
			for k := range env {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if _, err := fmt.Fprintf(a.stdout, "%s=%s\n", k, env[k]); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&daemon, "daemon", false, "operate on the daemon env file instead of the agent env file")
	return cmd
}
