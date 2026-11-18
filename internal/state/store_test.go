package state

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/joshgummersall/summond/internal/job"
)

func TestDiscoverPathsUsesUserHomeForNonRoot(t *testing.T) {
	current, err := user.Current()
	if err != nil {
		t.Fatalf("Current() error = %v", err)
	}
	uid, err := strconv.Atoi(current.Uid)
	if err != nil {
		t.Fatalf("Atoi(%q) error = %v", current.Uid, err)
	}
	if uid == 0 {
		t.Skip("test needs a non-root user")
	}

	paths, err := DiscoverPaths(uid)
	if err != nil {
		t.Fatalf("DiscoverPaths() error = %v", err)
	}
	if paths.Home != filepath.Join(current.HomeDir, "Library", "Application Support", "summond") {
		t.Fatalf("Home = %q", paths.Home)
	}
	if paths.LaunchDir != filepath.Join(current.HomeDir, "Library", "LaunchAgents") {
		t.Fatalf("LaunchDir = %q", paths.LaunchDir)
	}
}

func TestDiscoverPathsUsesSystemPathsForRoot(t *testing.T) {
	t.Setenv("SUDO_USER", "someone")
	paths, err := DiscoverPaths(0)
	if err != nil {
		t.Fatalf("DiscoverPaths() error = %v", err)
	}
	if paths.Home != "/Library/Application Support/summond" {
		t.Fatalf("Home = %q", paths.Home)
	}
	if paths.LaunchDir != "/Library/LaunchDaemons" {
		t.Fatalf("LaunchDir = %q", paths.LaunchDir)
	}
}

func TestInstallCreatesManagedLogFiles(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(Paths{
		Home:      filepath.Join(dir, "state"),
		LaunchDir: filepath.Join(dir, "LaunchAgents"),
	})

	spec, err := store.Install(job.Spec{
		Name:    "hello-world",
		Target:  job.TargetAgent,
		Command: "/bin/echo",
		Schedule: job.Schedule{
			Kind: job.ScheduleLogin,
		},
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
		Home:      filepath.Join(dir, "state"),
		LaunchDir: filepath.Join(dir, "LaunchAgents"),
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
		Home:      filepath.Join(dir, "state"),
		LaunchDir: filepath.Join(dir, "LaunchAgents"),
	})

	spec, err := store.Install(job.Spec{
		Name:    "hello-world",
		Target:  job.TargetAgent,
		Command: "/bin/echo",
		Schedule: job.Schedule{
			Kind: job.ScheduleLogin,
		},
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
		Home:      filepath.Join(dir, "state"),
		LaunchDir: filepath.Join(dir, "LaunchAgents"),
	})

	spec, err := store.Install(job.Spec{
		Name:    "hello-world",
		Target:  job.TargetAgent,
		Command: "/bin/echo",
		Schedule: job.Schedule{
			Kind: job.ScheduleLogin,
		},
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

func TestStoreLooksUpJobsByExactManagedKey(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(Paths{
		Home:      filepath.Join(dir, "state"),
		LaunchDir: filepath.Join(dir, "LaunchAgents"),
	})
	spec, err := store.Install(job.Spec{
		Group:    "G",
		Name:     "backup",
		Target:   job.TargetAgent,
		Command:  "/bin/echo",
		Schedule: job.Schedule{Kind: job.ScheduleLogin},
	})
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}

	if _, err := store.Load("G.backup"); err != nil {
		t.Fatalf("Load(G.backup) error = %v", err)
	}
	if _, err := store.Load("backup"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load(backup) error = %v, want not exist", err)
	}
	if err := store.RecordExecutionStart("backup", time.Now()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("RecordExecutionStart(backup) error = %v, want not exist", err)
	}
	if _, _, err := store.TryLockExecution("backup"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("TryLockExecution(backup) error = %v, want not exist", err)
	}
	if _, err := store.Remove("backup"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Remove(backup) error = %v, want not exist", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "state", "jobs", "backup")); !os.IsNotExist(err) {
		t.Fatalf("lookup by bare name created a job directory, stat err = %v", err)
	}
	if _, err := os.Stat(store.MetadataPathForSpec(spec)); err != nil {
		t.Fatalf("G.backup metadata missing after bare-name calls: %v", err)
	}
}
