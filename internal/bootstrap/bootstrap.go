package bootstrap

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/joshgummersall/summond/internal/state"
)

const newsyslogFilename = "com.joshgummersall.summond.conf"
const installMarkerFilename = ".installed"

type Options struct {
	SkipNewsyslog bool
	Overwrite     bool
}

type Result struct {
	ConfigPath              string
	ConfigStatus            string
	EnvPath                 string
	EnvStatus               string
	NewsyslogGeneratedPath  string
	NewsyslogGenerateStatus string
	NewsyslogInstallPath    string
	NewsyslogInstalled      bool
	DaemonStateInitialized  bool
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
	MkdirAllWithSudo(path string) error
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
	paths      state.Paths
	daemonHome string
	installer  Installer
}

func NewManager(paths state.Paths, installer Installer) *Manager {
	return &Manager{paths: paths, daemonHome: paths.Home, installer: installer}
}

func NewManagerWithDaemonHome(paths state.Paths, daemonPaths state.Paths, installer Installer) *Manager {
	return &Manager{paths: paths, daemonHome: daemonPaths.Home, installer: installer}
}

func (m *Manager) Install(opts Options) (Result, error) {
	configPath, err := currentConfigPath()
	if err != nil {
		return Result{}, err
	}
	result := Result{
		ConfigPath:             configPath,
		EnvPath:                m.EnvFilePath(),
		NewsyslogGeneratedPath: m.GeneratedNewsyslogPath(),
		NewsyslogInstallPath:   m.SystemNewsyslogPath(),
	}

	configStatus, err := writeFile(configPath, []byte(renderConfig()), opts.Overwrite)
	if err != nil {
		return result, err
	}
	result.ConfigStatus = configStatus
	envStatus, err := writeFile(result.EnvPath, []byte(RenderEnvFile()), opts.Overwrite)
	if err != nil {
		return result, err
	}
	result.EnvStatus = envStatus

	// Ensure bin/ directory exists in the agent state dir.
	if err := os.MkdirAll(filepath.Join(m.paths.Home, "bin"), 0o755); err != nil {
		return result, err
	}

	// Write daemon env.json and bin/ if the daemon state dir differs from the agent dir.
	// If the daemon home is not writable (e.g. /Library/... requires sudo), set a flag
	// so the caller can retry with sudo via InitDaemonStateWithSudo.
	if m.daemonHome != "" && m.daemonHome != m.paths.Home {
		daemonEnvPath := filepath.Join(m.daemonHome, "env.json")
		binPath := filepath.Join(m.daemonHome, "bin")
		mkdirErr := os.MkdirAll(binPath, 0o755)
		_, envErr := writeFile(daemonEnvPath, []byte(RenderEnvFile()), opts.Overwrite)
		if mkdirErr != nil || envErr != nil {
			permDenied := errors.Is(mkdirErr, os.ErrPermission) || errors.Is(envErr, os.ErrPermission)
			if !permDenied {
				if mkdirErr != nil {
					return result, mkdirErr
				}
				return result, envErr
			}
			// Permission denied — caller can retry with InitDaemonStateWithSudo.
		} else {
			result.DaemonStateInitialized = true
		}
	} else {
		result.DaemonStateInitialized = true
	}

	if opts.SkipNewsyslog {
		if err := m.writeInstallMarker(); err != nil {
			return result, err
		}
		result.NewsyslogGenerateStatus = "skipped"
		return result, nil
	}

	newsyslogStatus, err := writeFile(result.NewsyslogGeneratedPath, []byte(renderNewsyslog(m.paths.Home, m.daemonHome)), opts.Overwrite)
	if err != nil {
		return result, err
	}
	result.NewsyslogGenerateStatus = newsyslogStatus
	if err := m.writeInstallMarker(); err != nil {
		return result, err
	}

	if err := m.installer.Install(result.NewsyslogGeneratedPath, result.NewsyslogInstallPath); err != nil {
		return result, err
	}
	result.NewsyslogInstalled = true
	return result, nil
}

func (m *Manager) Init(opts Options) (Result, error) {
	return m.Install(opts)
}

func (m *Manager) InitDaemonStateWithSudo(result *Result) error {
	if m.daemonHome == "" || m.daemonHome == m.paths.Home {
		result.DaemonStateInitialized = true
		return nil
	}
	if err := m.installer.MkdirAllWithSudo(filepath.Join(m.daemonHome, "bin")); err != nil {
		return err
	}
	daemonEnvPath := filepath.Join(m.daemonHome, "env.json")
	if _, statErr := os.Stat(daemonEnvPath); statErr == nil {
		result.DaemonStateInitialized = true
		result.UsedSudo = true
		return nil
	}
	tmpFile, err := os.CreateTemp("", "summond-env-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)
	if _, err := tmpFile.WriteString(RenderEnvFile()); err != nil {
		tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if err := m.installer.InstallWithSudo(tmpPath, daemonEnvPath); err != nil {
		return err
	}
	result.DaemonStateInitialized = true
	result.UsedSudo = true
	return nil
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
	result := UninstallResult{
		NewsyslogGeneratedPath: m.GeneratedNewsyslogPath(),
		NewsyslogInstallPath:   m.SystemNewsyslogPath(),
	}

	status, err := removePath(result.NewsyslogGeneratedPath)
	if err != nil {
		return result, err
	}
	result.NewsyslogGenerateStatus = status
	if err := removeIfExists(m.installMarkerPath()); err != nil {
		return result, err
	}

	if err := m.installer.Remove(result.NewsyslogInstallPath); err != nil {
		return result, err
	}
	result.NewsyslogInstallStatus = "removed"
	return result, nil
}

func (m *Manager) IsInstalled() (bool, error) {
	_, err := os.Stat(m.installMarkerPath())
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func (m *Manager) UninstallNewsyslogWithSudo(result *UninstallResult) error {
	if err := m.installer.RemoveWithSudo(result.NewsyslogInstallPath); err != nil {
		return err
	}
	result.NewsyslogInstallStatus = "removed"
	result.UsedSudo = true
	return nil
}

func (m *Manager) stateDirs() []string {
	dirs := []string{m.paths.Home}
	if m.daemonHome != "" && m.daemonHome != m.paths.Home {
		dirs = append(dirs, m.daemonHome)
	}
	return dirs
}

func (m *Manager) GeneratedNewsyslogPath() string {
	return filepath.Join(m.paths.Home, newsyslogFilename)
}

func (m *Manager) EnvFilePath() string {
	return filepath.Join(m.paths.Home, "env.json")
}

func (m *Manager) SystemNewsyslogPath() string {
	return filepath.Join(m.paths.NewsyslogDir, newsyslogFilename)
}

func (m *Manager) installMarkerPath() string {
	return filepath.Join(m.paths.Home, installMarkerFilename)
}

func (m *Manager) writeInstallMarker() error {
	if err := os.MkdirAll(m.paths.Home, 0o755); err != nil {
		return err
	}
	return os.WriteFile(m.installMarkerPath(), []byte("installed\n"), 0o644)
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

func (OSInstaller) MkdirAllWithSudo(path string) error {
	var stderr bytes.Buffer
	cmd := exec.Command("sudo", "install", "-d", "-m", "0755", path)
	cmd.Stderr = &stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("%w: %s", err, stderr.String())
		}
		return err
	}
	return nil
}

func (OSInstaller) InstallWithSudo(src, dst string) error {
	var stderr bytes.Buffer
	cmd := exec.Command("sudo", "install", "-m", "0644", src, dst)
	cmd.Stderr = &stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("%w: %s", err, stderr.String())
		}
		return err
	}
	return nil
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
	var stderr bytes.Buffer
	cmd := exec.Command("sudo", "rm", "-f", path)
	cmd.Stderr = &stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("%w: %s", err, stderr.String())
		}
		return err
	}
	return nil
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

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
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
# schedule = "hourly"
# minute = 15

# Daily example
# [jobs.backup]
# shell_command = "cd ~/src/my-project && git pull --ff-only"
# schedule = "daily"
# hour = 3
# minute = 30

# Login example
# [jobs.startup]
# command = "/usr/bin/open"
# args = ["-a", "Messages"]
# schedule = "login"

# Interval example
# [jobs.sync]
# command = "/usr/bin/env"
# args = ["bash", "-c", "echo syncing"]
# schedule = "interval"
# interval_minutes = 30

# Daemon note
# target defaults to "agent"; use target = "daemon" with schedule = "boot" for a system LaunchDaemon.
# Managed stdout/stderr log paths are assigned automatically.
`) + "\n"
}

func RenderEnvFile() string {
	return "{\n  \"PATH\": \"/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin\"\n}\n"
}

func renderNewsyslog(agentHome string, daemonHome string) string {
	patterns := []string{
		filepath.Join(agentHome, "logs", "*.log"),
	}
	if daemonHome != "" && daemonHome != agentHome {
		patterns = append(patterns, filepath.Join(daemonHome, "logs", "*.log"))
	}
	lines := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		lines = append(lines, fmt.Sprintf("%s  644  7  *  @T00  Z", pattern))
	}
	return strings.Join(lines, "\n") + "\n"
}
