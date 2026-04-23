package bootstrap

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/joshgummersall/summond/internal/state"
)

const newsyslogFilename = "com.joshgummersall.summond.conf"

type Options struct {
	SkipNewsyslog    bool
	Force            bool
}

type Result struct {
	ConfigPath              string
	ConfigStatus            string
	NewsyslogGeneratedPath  string
	NewsyslogGenerateStatus string
	NewsyslogInstallPath    string
	NewsyslogInstalled      bool
	UsedSudo                bool
}

type UninstallResult struct {
	ConfigPath              string
	ConfigStatus            string
	NewsyslogGeneratedPath  string
	NewsyslogGenerateStatus string
	NewsyslogInstallPath    string
	NewsyslogInstallStatus  string
	UsedSudo                bool
}

type Installer interface {
	Install(src, dst string) error
	InstallWithSudo(src, dst string) error
	Remove(path string) error
	RemoveWithSudo(path string) error
}

type PermissionError struct {
	Err error
}

func (e *PermissionError) Error() string {
	if e == nil || e.Err == nil {
		return "permission denied"
	}
	return e.Err.Error()
}

func (e *PermissionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type Manager struct {
	paths     state.Paths
	installer Installer
}

func NewManager(paths state.Paths, installer Installer) *Manager {
	return &Manager{paths: paths, installer: installer}
}

func (m *Manager) Install(opts Options) (Result, error) {
	configPath, err := currentConfigPath()
	if err != nil {
		return Result{}, err
	}
	result := Result{
		ConfigPath:             configPath,
		NewsyslogGeneratedPath: m.GeneratedNewsyslogPath(),
		NewsyslogInstallPath:   m.SystemNewsyslogPath(),
	}

	configStatus, err := writeFile(configPath, []byte(renderConfig()), opts.Force)
	if err != nil {
		return result, err
	}
	result.ConfigStatus = configStatus

	if opts.SkipNewsyslog {
		result.NewsyslogGenerateStatus = "skipped"
		return result, nil
	}

	newsyslogStatus, err := writeFile(result.NewsyslogGeneratedPath, []byte(renderNewsyslog(m.paths.Home)), opts.Force)
	if err != nil {
		return result, err
	}
	result.NewsyslogGenerateStatus = newsyslogStatus

	if err := m.installer.Install(result.NewsyslogGeneratedPath, result.NewsyslogInstallPath); err != nil {
		return result, err
	}
	result.NewsyslogInstalled = true
	return result, nil
}

func (m *Manager) Init(opts Options) (Result, error) {
	return m.Install(opts)
}

func (m *Manager) InstallNewsyslogWithSudo(result *Result) error {
	if err := m.installer.InstallWithSudo(result.NewsyslogGeneratedPath, result.NewsyslogInstallPath); err != nil {
		return err
	}
	result.NewsyslogInstalled = true
	result.UsedSudo = true
	return nil
}

func (m *Manager) Uninstall() (UninstallResult, error) {
	configPath, err := currentConfigPath()
	if err != nil {
		return UninstallResult{}, err
	}
	result := UninstallResult{
		ConfigPath:             configPath,
		NewsyslogGeneratedPath: m.GeneratedNewsyslogPath(),
		NewsyslogInstallPath:   m.SystemNewsyslogPath(),
	}

	status, err := removePath(configPath)
	if err != nil {
		return result, err
	}
	result.ConfigStatus = status

	status, err = removePath(result.NewsyslogGeneratedPath)
	if err != nil {
		return result, err
	}
	result.NewsyslogGenerateStatus = status

	if err := m.installer.Remove(result.NewsyslogInstallPath); err != nil {
		return result, err
	}
	result.NewsyslogInstallStatus = "removed"
	return result, nil
}

func (m *Manager) UninstallNewsyslogWithSudo(result *UninstallResult) error {
	if err := m.installer.RemoveWithSudo(result.NewsyslogInstallPath); err != nil {
		return err
	}
	result.NewsyslogInstallStatus = "removed"
	result.UsedSudo = true
	return nil
}

func (m *Manager) GeneratedNewsyslogPath() string {
	return filepath.Join(m.paths.ConfigDir, newsyslogFilename)
}

func (m *Manager) SystemNewsyslogPath() string {
	return filepath.Join(m.paths.NewsyslogDir, newsyslogFilename)
}

func currentConfigPath() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve current working directory: %w", err)
	}
	return filepath.Join(wd, "summond.toml"), nil
}

type OSInstaller struct{}

func (OSInstaller) Install(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return &PermissionError{Err: err}
		}
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return &PermissionError{Err: err}
		}
		return err
	}
	return nil
}

func (OSInstaller) InstallWithSudo(src, dst string) error {
	cmd := exec.Command("sudo", "install", "-m", "0644", src, dst)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func (OSInstaller) Remove(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		if errors.Is(err, os.ErrPermission) {
			return &PermissionError{Err: err}
		}
		return err
	}
	return nil
}

func (OSInstaller) RemoveWithSudo(path string) error {
	cmd := exec.Command("sudo", "rm", "-f", path)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func writeFile(path string, content []byte, force bool) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err == nil {
		if !force {
			return "exists", nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return "", err
	}
	if force {
		return "overwritten", nil
	}
	return "created", nil
}

func removePath(path string) (string, error) {
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "absent", nil
		}
		return "", err
	}
	return "removed", nil
}

func renderConfig() string {
	return strings.TrimSpace(`
# Summond starter config
# Edit this file, then run: summond apply summond.toml

# Hourly example
# [jobs.cleanup]
# command = "/bin/echo"
# args = ["cleanup"]
# target = "agent"
# schedule = "hourly"
# minute = 15
# enabled = true

# Daily example
# [jobs.backup]
# shell_command = "cd ~/src/my-project && git pull --ff-only"
# target = "agent"
# schedule = "daily"
# hour = 3
# minute = 30
# enabled = true

# Login example
# [jobs.startup]
# command = "/usr/bin/open"
# args = ["-a", "Messages"]
# target = "agent"
# schedule = "login"
# enabled = true

# Interval example
# [jobs.sync]
# command = "/usr/bin/env"
# args = ["bash", "-lc", "echo syncing"]
# target = "agent"
# schedule = "interval"
# interval_minutes = 30
# enabled = true

# Daemon note
# Use target = "daemon" with schedule = "boot" for a system LaunchDaemon.
# Managed stdout/stderr log paths are assigned automatically unless overridden.
`) + "\n"
}

func renderNewsyslog(stateHome string) string {
	pattern := filepath.Join(stateHome, "logs", "*.log")
	return fmt.Sprintf("%s  644  7  *  @T00  Z\n", pattern)
}
