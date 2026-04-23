package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "summond.toml")
	data := []byte(`
[jobs.backup]
command = "/bin/echo"
args = ["backup", "now"]
target = "agent"
schedule = "weekly"
weekday = 2
hour = 1
minute = 30
enabled = true

[jobs.backup.env]
MODE = "fast"
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	specs, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("len(specs) = %d", len(specs))
	}
	if got, want := specs[0].Environment["MODE"], "fast"; got != want {
		t.Fatalf("MODE = %q, want %q", got, want)
	}
	if got, want := specs[0].WorkingDir, dir; got != want {
		t.Fatalf("WorkingDir = %q, want %q", got, want)
	}
}

func TestLoadFileResolvesRelativeWatchPaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "configs", "summond.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	data := []byte(`
[jobs.watcher]
command = "/bin/echo"
args = ["watch"]
target = "agent"
trigger = "on_change"
watch_paths = ["../data/input.txt", "/tmp/absolute.txt"]
enabled = true
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	specs, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	got := specs[0].WatchPaths
	want0 := filepath.Clean(filepath.Join(filepath.Dir(path), "../data/input.txt"))
	if got[0] != want0 {
		t.Fatalf("WatchPaths[0] = %q, want %q", got[0], want0)
	}
	if got[1] != "/tmp/absolute.txt" {
		t.Fatalf("WatchPaths[1] = %q", got[1])
	}
	if got, want := specs[0].WorkingDir, filepath.Dir(path); got != want {
		t.Fatalf("WorkingDir = %q, want %q", got, want)
	}
}

func TestLoadFileRespectsExplicitWorkingDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "configs", "summond.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	data := []byte(`
[jobs.worker]
command = "/bin/echo"
args = ["work"]
target = "agent"
schedule = "hourly"
minute = 5
working_dir = "/tmp/custom-working-dir"
enabled = true
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	specs, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if got, want := specs[0].WorkingDir, "/tmp/custom-working-dir"; got != want {
		t.Fatalf("WorkingDir = %q, want %q", got, want)
	}
}

func TestLoadFileParsesMultilineShellCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "summond.toml")
	data := []byte(`
[jobs.brew-maintenance]
shell_command = """
brew update
brew upgrade
brew cleanup
"""
target = "agent"
schedule = "weekly"
enabled = true
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	specs, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	got := specs[0].ShellCommand
	want := "brew update\nbrew upgrade\nbrew cleanup\n"
	if got != want {
		t.Fatalf("ShellCommand = %q, want %q", got, want)
	}
}

func TestLoadFileResolvesCommandFromJobPath(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	commandPath := filepath.Join(binDir, "chezmoi")
	if err := os.WriteFile(commandPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	path := filepath.Join(dir, "summond.toml")
	data := []byte(`
[jobs.chezmoi-update]
command = "chezmoi"
args = ["update"]
target = "agent"
schedule = "daily"
enabled = true

[jobs.chezmoi-update.env]
PATH = "` + binDir + `"
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	specs, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if got, want := specs[0].Command, commandPath; got != want {
		t.Fatalf("Command = %q, want %q", got, want)
	}
}
