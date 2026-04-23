package state

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/joshgummersall/summond/internal/job"
)

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
}

func TestInstallCreatesCustomLogDirectoriesAndFiles(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(Paths{
		Home:       filepath.Join(dir, "state"),
		AgentsDir:  filepath.Join(dir, "LaunchAgents"),
		DaemonsDir: filepath.Join(dir, "LaunchDaemons"),
	})
	stdoutPath := filepath.Join(dir, "custom", "logs", "stdout.log")
	stderrPath := filepath.Join(dir, "custom", "logs", "stderr.log")

	_, err := store.Install(job.Spec{
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

	for _, path := range []string{stdoutPath, stderrPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("Stat(%q) error = %v", path, err)
		}
	}
}
