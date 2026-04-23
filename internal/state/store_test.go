package state

import (
	"os"
	"os/user"
	"path/filepath"
	"testing"
	"time"

	"github.com/standardlabs/summond/internal/job"
)

func TestDiscoverPathSetUsesFixedPlatformPaths(t *testing.T) {
	current, err := user.Current()
	if err != nil {
		t.Fatalf("Current() error = %v", err)
	}

	paths, err := DiscoverPathSet()
	if err != nil {
		t.Fatalf("DiscoverPathSet() error = %v", err)
	}

	if paths.Agent.Home != filepath.Join(current.HomeDir, "Library", "Application Support", "summond") {
		t.Fatalf("Agent.Home = %q", paths.Agent.Home)
	}
	if paths.Agent.AgentsDir != filepath.Join(current.HomeDir, "Library", "LaunchAgents") {
		t.Fatalf("Agent.AgentsDir = %q", paths.Agent.AgentsDir)
	}
	if paths.Daemon.Home != "/Library/Application Support/summond" {
		t.Fatalf("Daemon.Home = %q", paths.Daemon.Home)
	}
	if paths.Daemon.DaemonsDir != "/Library/LaunchDaemons" {
		t.Fatalf("Daemon.DaemonsDir = %q", paths.Daemon.DaemonsDir)
	}
	if paths.Agent.NewsyslogDir != "/etc/newsyslog.d" || paths.Daemon.NewsyslogDir != "/etc/newsyslog.d" {
		t.Fatalf("unexpected NewsyslogDir values: %#v", paths)
	}
}

func TestInstallCreatesManagedLogFiles(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(Paths{
		Home:       filepath.Join(dir, "state"),
		AgentsDir:  filepath.Join(dir, "LaunchAgents"),
		DaemonsDir: filepath.Join(dir, "LaunchDaemons"),
	})

	spec, err := store.Install(job.Spec{
		Name:    "hello-world",
		Target:  job.TargetAgent,
		Command: "/bin/echo",
		Schedule: job.Schedule{
			Kind: job.ScheduleLogin,
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}

	for _, path := range []string{spec.StdoutPath, spec.StderrPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat(%q) error = %v", path, err)
		}
		if info.Size() != 0 {
			t.Fatalf("log file %q size = %d, want 0", path, info.Size())
		}
	}
	if spec.Checksum == "" {
		t.Fatal("expected checksum")
	}
}

func TestInstallAlwaysUsesManagedLogPaths(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(Paths{
		Home:       filepath.Join(dir, "state"),
		AgentsDir:  filepath.Join(dir, "LaunchAgents"),
		DaemonsDir: filepath.Join(dir, "LaunchDaemons"),
	})
	stdoutPath := filepath.Join(dir, "custom", "logs", "stdout.log")
	stderrPath := filepath.Join(dir, "custom", "logs", "stderr.log")

	spec, err := store.Install(job.Spec{
		Name:       "custom-logs",
		Target:     job.TargetAgent,
		Command:    "/bin/echo",
		StdoutPath: stdoutPath,
		StderrPath: stderrPath,
		Schedule: job.Schedule{
			Kind: job.ScheduleLogin,
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}

	if spec.StdoutPath == stdoutPath || spec.StderrPath == stderrPath {
		t.Fatalf("expected managed log paths, got stdout=%q stderr=%q", spec.StdoutPath, spec.StderrPath)
	}
	for _, path := range []string{spec.StdoutPath, spec.StderrPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("Stat(%q) error = %v", path, err)
		}
	}
}

func TestInstallPreservesRuntimeState(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(Paths{
		Home:       filepath.Join(dir, "state"),
		AgentsDir:  filepath.Join(dir, "LaunchAgents"),
		DaemonsDir: filepath.Join(dir, "LaunchDaemons"),
	})

	spec, err := store.Install(job.Spec{
		Name:    "hello-world",
		Target:  job.TargetAgent,
		Command: "/bin/echo",
		Schedule: job.Schedule{
			Kind: job.ScheduleLogin,
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}

	started := time.Now().Add(-time.Minute).UTC()
	finished := time.Now().UTC()
	exitCode := 0
	if err := store.RecordExecutionStart(spec.Name, started); err != nil {
		t.Fatalf("RecordExecutionStart() error = %v", err)
	}
	if err := store.RecordExecutionFinish(spec.Name, job.ExecutionRecord{
		StartedAt:  started,
		FinishedAt: &finished,
		ExitCode:   &exitCode,
	}); err != nil {
		t.Fatalf("RecordExecutionFinish() error = %v", err)
	}

	reinstalled, err := store.Install(job.Spec{
		Name:    "hello-world",
		Target:  job.TargetAgent,
		Command: "/bin/echo",
		Args:    []string{"hello"},
		Schedule: job.Schedule{
			Kind: job.ScheduleLogin,
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if reinstalled.RunCount != 1 || reinstalled.SuccessCount != 1 || reinstalled.FailureCount != 0 {
		t.Fatalf("unexpected runtime counters: %+v", reinstalled)
	}
	if len(reinstalled.RecentRuns) != 1 {
		t.Fatalf("RecentRuns len = %d", len(reinstalled.RecentRuns))
	}
}

func TestRecordExecutionUpdatesMetadata(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(Paths{
		Home:       filepath.Join(dir, "state"),
		AgentsDir:  filepath.Join(dir, "LaunchAgents"),
		DaemonsDir: filepath.Join(dir, "LaunchDaemons"),
	})

	spec, err := store.Install(job.Spec{
		Name:    "hello-world",
		Target:  job.TargetAgent,
		Command: "/bin/echo",
		Schedule: job.Schedule{
			Kind: job.ScheduleLogin,
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}

	started := time.Now().Add(-time.Second).UTC()
	if err := store.RecordExecutionStart(spec.Name, started); err != nil {
		t.Fatalf("RecordExecutionStart() error = %v", err)
	}
	loaded, err := store.Load(spec.Name)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.LastStartedAt == nil || !loaded.LastStartedAt.Equal(started) {
		t.Fatalf("LastStartedAt = %v, want %v", loaded.LastStartedAt, started)
	}
	if loaded.LastFinishedAt != nil {
		t.Fatalf("LastFinishedAt = %v, want nil", loaded.LastFinishedAt)
	}

	finished := time.Now().UTC()
	exitCode := 125
	if err := store.RecordExecutionFinish(spec.Name, job.ExecutionRecord{
		StartedAt:  started,
		FinishedAt: &finished,
		ExitCode:   &exitCode,
		Error:      "exit status 125",
	}); err != nil {
		t.Fatalf("RecordExecutionFinish() error = %v", err)
	}
	loaded, err = store.Load(spec.Name)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.LastExitCode == nil || *loaded.LastExitCode != exitCode {
		t.Fatalf("LastExitCode = %v, want %d", loaded.LastExitCode, exitCode)
	}
	if loaded.RunCount != 1 || loaded.FailureCount != 1 || loaded.SuccessCount != 0 {
		t.Fatalf("unexpected counters: %+v", loaded)
	}
	if len(loaded.RecentRuns) != 1 || loaded.RecentRuns[0].Error != "exit status 125" {
		t.Fatalf("unexpected recent runs: %+v", loaded.RecentRuns)
	}
}
