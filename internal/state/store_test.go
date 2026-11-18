package state

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
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

func TestDiscoverPathsMakesOnlyDaemonScopePrivate(t *testing.T) {
	paths, err := DiscoverPaths(0)
	if err != nil {
		t.Fatalf("DiscoverPaths(0) error = %v", err)
	}
	if !paths.Private {
		t.Fatal("daemon paths are not private")
	}
	if paths.DirMode() != 0o700 || paths.FileMode() != 0o600 {
		t.Fatalf("daemon modes = %o, %o, want 700, 600", paths.DirMode(), paths.FileMode())
	}
	if agent := (Paths{}); agent.DirMode() != 0o755 || agent.FileMode() != 0o644 {
		t.Fatalf("agent modes = %o, %o, want 755, 644", agent.DirMode(), agent.FileMode())
	}
}

// withUmask sets the process umask for the test, so created modes are
// predictable.
func withUmask(t *testing.T, mask int) {
	t.Helper()
	old := syscall.Umask(mask)
	t.Cleanup(func() { syscall.Umask(old) })
}

func installHello(t *testing.T, store *Store) job.Spec {
	t.Helper()
	spec, err := store.Install(job.Spec{
		Name:        "hello-world",
		Target:      job.TargetDaemon,
		Command:     "/bin/echo",
		Environment: map[string]string{"TOKEN": "secret"},
		Schedule:    job.Schedule{Kind: job.ScheduleBoot},
	})
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	return spec
}

// stateModes lists every path summond keeps for spec, with the mode that the
// scope's dirMode and fileMode call for.
func stateModes(store *Store, spec job.Spec, dirMode, fileMode os.FileMode) map[string]os.FileMode {
	return map[string]os.FileMode{
		store.Paths().Home:                        dirMode,
		store.JobsFilePath():                      dirMode,
		store.JobDirForSpec(spec):                 dirMode,
		store.MetadataPathForSpec(spec):           fileMode,
		store.MetadataPathForSpec(spec) + ".lock": fileMode,
		spec.StdoutPath:                           fileMode,
		spec.StderrPath:                           fileMode,
		spec.PlistPath:                            fileMode,
		filepath.Dir(store.RuntimeBinaryPath()):   0o755,
	}
}

func assertModes(t *testing.T, want map[string]os.FileMode) {
	t.Helper()
	for path, mode := range want {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat(%q) error = %v", path, err)
		}
		if got := info.Mode().Perm(); got != mode {
			t.Errorf("mode of %s = %o, want %o", path, got, mode)
		}
	}
}

func TestInstallCreatesOwnerOnlyStateInDaemonScope(t *testing.T) {
	withUmask(t, 0o022)
	dir := t.TempDir()
	store := NewStore(Paths{
		Home:      filepath.Join(dir, "state"),
		LaunchDir: filepath.Join(dir, "LaunchDaemons"),
		Private:   true,
	})
	spec := installHello(t, store)
	assertModes(t, stateModes(store, spec, 0o700, 0o600))
}

func TestInstallKeepsAgentScopeModes(t *testing.T) {
	withUmask(t, 0o022)
	dir := t.TempDir()
	store := NewStore(Paths{
		Home:      filepath.Join(dir, "state"),
		LaunchDir: filepath.Join(dir, "LaunchAgents"),
	})
	spec := installHello(t, store)
	assertModes(t, stateModes(store, spec, 0o755, 0o644))
}

func TestInstallTightensExistingStateInDaemonScope(t *testing.T) {
	withUmask(t, 0o022)
	dir := t.TempDir()
	paths := Paths{
		Home:      filepath.Join(dir, "state"),
		LaunchDir: filepath.Join(dir, "LaunchDaemons"),
	}
	// State that an earlier summond left readable by every user.
	old := NewStore(paths)
	spec := installHello(t, old)
	extra := []string{
		spec.StdoutPath + ".1",
		spec.StderrPath + ".1",
		filepath.Join(old.JobDirForSpec(spec), "exec.lock"),
	}
	for _, path := range extra {
		if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}
	want := stateModes(old, spec, 0o755, 0o644)
	for _, path := range extra {
		want[path] = 0o644
	}
	assertModes(t, want)

	paths.Private = true
	store := NewStore(paths)
	spec = installHello(t, store)
	want = stateModes(store, spec, 0o700, 0o600)
	for _, path := range extra {
		want[path] = 0o600
	}
	assertModes(t, want)
}

func TestTryLockExecutionCreatesOwnerOnlyLockInDaemonScope(t *testing.T) {
	withUmask(t, 0o022)
	dir := t.TempDir()
	store := NewStore(Paths{
		Home:      filepath.Join(dir, "state"),
		LaunchDir: filepath.Join(dir, "LaunchDaemons"),
		Private:   true,
	})
	spec := installHello(t, store)
	unlock, acquired, err := store.TryLockExecution(spec.Name)
	if err != nil || !acquired {
		t.Fatalf("TryLockExecution() = %v, %v", acquired, err)
	}
	unlock()
	assertModes(t, map[string]os.FileMode{filepath.Join(store.JobDirForSpec(spec), "exec.lock"): 0o600})
}
