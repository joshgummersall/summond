package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshgummersall/summond/internal/job"
)

func TestExecFailureIncludesMessage(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app.stdout = &stdout
	app.stderr = &stderr

	configPath := filepath.Join(testHome(t), "summond.toml")
	data := strings.Join([]string{
		"[jobs.failer]",
		`command = "/bin/sh"`,
		`args = ["-c", "exit 7"]`,
		`target = "agent"`,
		`schedule = "daily"`,
		"hour = 3",
		"minute = 45",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := app.run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	err := app.run([]string{"exec", "failer"})
	var exitErr ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 7 {
		t.Fatalf("exec error = %#v", err)
	}
	if exitErr.Error() == "" {
		t.Fatal("expected exec error message")
	}
}

func TestExecRecordsSuccessfulRun(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout

	configPath := filepath.Join(testHome(t), "summond.toml")
	data := strings.Join([]string{
		"[jobs.cleanup]",
		`command = "/bin/echo"`,
		`args = ["clean"]`,
		`target = "agent"`,
		`schedule = "daily"`,
		"hour = 3",
		"minute = 45",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := app.run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	stdout.Reset()
	if err := app.run([]string{"exec", "cleanup"}); err != nil {
		t.Fatalf("exec error = %v", err)
	}
	if got := stdout.String(); got != "clean\n" {
		t.Fatalf("stdout = %q", got)
	}
	spec, err := app.store.Load("cleanup")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if spec.RunCount != 1 || spec.SuccessCount != 1 || spec.FailureCount != 0 {
		t.Fatalf("unexpected counters: %+v", spec)
	}
	if spec.LastExitCode == nil || *spec.LastExitCode != 0 {
		t.Fatalf("LastExitCode = %v", spec.LastExitCode)
	}
}

func TestExecRecordsFailingRun(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout

	configPath := filepath.Join(testHome(t), "summond.toml")
	data := strings.Join([]string{
		"[jobs.failer]",
		`command = "/bin/sh"`,
		`args = ["-c", "exit 7"]`,
		`target = "agent"`,
		`schedule = "daily"`,
		"hour = 3",
		"minute = 45",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := app.run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	err := app.run([]string{"exec", "failer"})
	var exitErr ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 7 {
		t.Fatalf("exec error = %#v", err)
	}
	spec, loadErr := app.store.Load("failer")
	if loadErr != nil {
		t.Fatalf("Load() error = %v", loadErr)
	}
	if spec.RunCount != 1 || spec.SuccessCount != 0 || spec.FailureCount != 1 {
		t.Fatalf("unexpected counters: %+v", spec)
	}
	if spec.LastExitCode == nil || *spec.LastExitCode != 7 {
		t.Fatalf("LastExitCode = %v", spec.LastExitCode)
	}
	if spec.LastError == "" {
		t.Fatal("expected LastError")
	}
}

func TestExecDaemonRequiresSudo(t *testing.T) {
	app := newTestApp(t)
	app.geteuid = func() int { return 501 }

	spec, err := app.daemonStore.Install(job.Spec{
		Name:     "daemon-job",
		Target:   job.TargetDaemon,
		Command:  "/bin/echo",
		Schedule: job.Schedule{Kind: job.ScheduleBoot},
	})
	if err != nil {
		t.Fatalf("Install(daemon-job) error = %v", err)
	}

	err = app.run([]string{"exec", spec.Name})
	if err == nil || err.Error() != "daemon jobs are owned by root; re-run as: sudo summond exec daemon-job" {
		t.Fatalf("exec error = %v", err)
	}
	loaded, loadErr := app.daemonStore.Load(spec.Name)
	if loadErr != nil {
		t.Fatalf("Load() error = %v", loadErr)
	}
	if loaded.RunCount != 0 {
		t.Fatalf("RunCount = %d, want 0", loaded.RunCount)
	}
}

func TestExecAgentRequiresNonRoot(t *testing.T) {
	app := newTestApp(t)
	app.geteuid = func() int { return 0 }

	spec, err := app.store.Install(job.Spec{
		Name:    "cleanup",
		Command: "/bin/echo",
		Schedule: job.Schedule{
			Kind: job.ScheduleDaily,
			Hour: 3, HourSet: true,
			Minute: 45, MinuteSet: true,
		},
	})
	if err != nil {
		t.Fatalf("Install(cleanup) error = %v", err)
	}

	err = app.run([]string{"exec", spec.Name})
	if err == nil || err.Error() != "agent jobs run as the logged-in user; re-run without sudo: summond exec cleanup" {
		t.Fatalf("exec error = %v", err)
	}
	loaded, loadErr := app.store.Load(spec.Name)
	if loadErr != nil {
		t.Fatalf("Load() error = %v", loadErr)
	}
	if loaded.RunCount != 0 {
		t.Fatalf("RunCount = %d, want 0", loaded.RunCount)
	}
}

func TestExecWithSudoHintsWhenAgentJobIsNotFoundInRootStore(t *testing.T) {
	app := newTestApp(t)
	app.geteuid = func() int { return 0 }
	t.Setenv("SUDO_USER", "josh")

	err := app.run([]string{"exec", "agent-test"})
	if err == nil {
		t.Fatal("expected exec error")
	}
	got := err.Error()
	if !strings.Contains(got, "read job metadata: file does not exist") ||
		!strings.Contains(got, "running with sudo uses root-managed jobs") ||
		!strings.Contains(got, "re-run without sudo: summond exec agent-test") {
		t.Fatalf("exec error = %q", got)
	}
}

func TestExecuteSpecSourcesEnvFileForCommandJobs(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app.stdout = &stdout
	app.stderr = &stderr

	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	commandPath := filepath.Join(binDir, "hello")
	if err := os.WriteFile(commandPath, []byte("#!/bin/sh\nprintf sourced\n"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	envPath := filepath.Join(dir, "env.json")
	envContent := fmt.Sprintf(`{"PATH": %q}`, binDir)
	if err := os.WriteFile(envPath, []byte(envContent), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	exitCode, err := app.executeSpec(job.Spec{
		Name:                "hello",
		Target:              job.TargetAgent,
		Command:             "hello",
		EnvironmentFilePath: envPath,
		Schedule: job.Schedule{
			Kind: job.ScheduleDaily,
		},
	})
	if err != nil {
		t.Fatalf("executeSpec() error = %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("exitCode = %d", exitCode)
	}
	if got := stdout.String(); got != "sourced" {
		t.Fatalf("stdout = %q", got)
	}
}
