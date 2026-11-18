package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/joshgummersall/summond/internal/job"
	"github.com/spf13/cobra"
)

// launchd starts jobs with a minimal PATH, so each scope has a default that a
// new env file is seeded with and that a job gets when no env source sets
// PATH. Agent jobs also get the Homebrew directories. Daemon jobs run as root
// and get only directories that root owns, because a user who can write a
// PATH directory can plant a command there that the job then runs as root.
const (
	agentDefaultPath  = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
	daemonDefaultPath = "/usr/bin:/bin:/usr/sbin:/sbin"
)

// defaultPath returns the default PATH for this process's scope.
func (a *App) defaultPath() string {
	if a.scopeTarget() == job.TargetDaemon {
		return daemonDefaultPath
	}
	return agentDefaultPath
}

// ensureEnvFile creates the scope's env file with the default contents if it
// does not exist yet, and returns its path.
func (a *App) ensureEnvFile() (string, error) {
	path := a.store.EnvFilePath()
	if _, err := os.Stat(path); err == nil {
		return path, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat env file: %w", err)
	}
	contents := "{\n  \"PATH\": \"" + a.defaultPath() + "\"\n}\n"
	if err := a.writeEnvFileContents(path, []byte(contents)); err != nil {
		return "", fmt.Errorf("write env file: %w", err)
	}
	return path, nil
}

// writeEnvFileContents replaces the env file atomically. The file holds
// secrets, so it is always written with mode 0600.
func (a *App) writeEnvFileContents(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func readEnvJSON(data []byte) (map[string]string, error) {
	var env map[string]string
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	return env, nil
}

// parseSetArg parses the argument to env set, KEY=VALUE or KEY. hasValue
// reports whether the argument carried a value, which may be empty.
func parseSetArg(arg string) (key, value string, hasValue bool, err error) {
	key, value, hasValue = strings.Cut(arg, "=")
	if key == "" {
		return "", "", false, fmt.Errorf("invalid argument %q: key must not be empty", arg)
	}
	if !isValidEnvKey(key) {
		return "", "", false, fmt.Errorf("invalid key %q: must match [A-Za-z_][A-Za-z0-9_]*", key)
	}
	return key, value, hasValue, nil
}

// readEnvValue reads the value for key from stdin, so that a secret stays
// out of shell history and ps. A terminal gets a prompt on stderr with echo
// off. Other input is read to EOF and loses one trailing "\n" or "\r\n". An
// empty value is an error; KEY= on the command line stores one on purpose.
func (a *App) readEnvValue(key string) (string, error) {
	var value string
	if f, ok := a.stdin.(*os.File); ok && term.IsTerminal(f.Fd()) {
		if _, err := fmt.Fprintf(a.stderr, "Value for %s (input hidden): ", key); err != nil {
			return "", err
		}
		data, err := term.ReadPassword(f.Fd())
		fmt.Fprintln(a.stderr)
		if err != nil {
			return "", fmt.Errorf("read value for %s: %w", key, err)
		}
		value = string(data)
	} else {
		data, err := io.ReadAll(a.stdin)
		if err != nil {
			return "", fmt.Errorf("read value for %s from stdin: %w", key, err)
		}
		value = string(data)
		if trimmed, ok := strings.CutSuffix(value, "\n"); ok {
			value = strings.TrimSuffix(trimmed, "\r")
		}
	}
	if value == "" {
		return "", fmt.Errorf("empty value for %s: use %s= to store an empty value", key, key)
	}
	return value, nil
}

func isValidEnvKey(key string) bool {
	for i, c := range key {
		if i == 0 {
			if !(c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) {
				return false
			}
		} else {
			if !(c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
				return false
			}
		}
	}
	return len(key) > 0
}

func (a *App) confirmWithDefault(prompt string, defaultYes bool) (bool, error) {
	_, err := fmt.Fprint(a.stdout, prompt)
	if err != nil {
		return false, err
	}
	if a.promptReader == nil {
		a.promptReader = bufio.NewReader(a.stdin)
	}
	line, err := a.promptReader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	answer := strings.TrimSpace(strings.ToLower(line))
	if answer == "" {
		return defaultYes && err == nil, nil
	}
	return answer == "y" || answer == "yes", nil
}

func (a *App) confirm(prompt string) (bool, error) {
	return a.confirmWithDefault(prompt, true)
}

func (a *App) newEnvCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Manage the shared job environment",
		Long: `Manage environment variables shared across all managed jobs.

Variables are stored in a JSON file and injected into every job at runtime before the
job command runs. Use 'set', 'get', and 'list' to manipulate the environment non-interactively.

summond uses the agent env file in ~/Library/Application Support/summond/env.json.
sudo summond uses the daemon env file in /Library/Application Support/summond/env.json.`,
		Example: `  summond env set API_KEY
  summond env get API_KEY
  summond env list
  sudo summond env set SVC_TOKEN`,
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
	cmd := &cobra.Command{
		Use:   "set KEY[=VALUE]",
		Short: "Set an environment variable",
		Long: `Add or update an environment variable in the shared env file.

If KEY already exists its value is updated in place. Otherwise a new entry is appended.
Keys must match [A-Za-z_][A-Za-z0-9_]*.

Use KEY without =VALUE for secrets, so the value stays out of shell history and ps.
On a terminal, summond prompts for the value and does not echo it. Otherwise it reads
the value from stdin and removes one trailing newline. An empty value from stdin is an
error; use KEY= to store an empty value.`,
		Example: `  summond env set API_KEY                      # prompt with echo off
  pbpaste | summond env set API_KEY            # value from stdin
  sudo summond env set SVC_TOKEN               # daemon scope, prompt with echo off
  summond env set LOG_LEVEL=debug              # not secret`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value, hasValue, err := parseSetArg(args[0])
			if err != nil {
				return err
			}
			if !hasValue {
				if value, err = a.readEnvValue(key); err != nil {
					return err
				}
			}

			path, err := a.ensureEnvFile()
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
	return cmd
}

func (a *App) newEnvGetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "get KEY",
		Short:   "Print the value of an environment variable",
		Example: `  summond env get API_KEY`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]

			path, err := a.ensureEnvFile()
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
	return cmd
}

func (a *App) newEnvRemoveCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove KEY",
		Short: "Remove an environment variable",
		Long: `Remove an environment variable from the shared env file.

Returns an error if the key does not exist.`,
		Example: `  summond env remove API_KEY
  sudo summond env remove SVC_TOKEN`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			path, err := a.ensureEnvFile()
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
	return cmd
}

func (a *App) newEnvListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List all environment variables",
		Example: `  summond env list`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := a.ensureEnvFile()
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
	return cmd
}
