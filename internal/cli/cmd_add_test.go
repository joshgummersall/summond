package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshgummersall/summond/internal/job"
)

func TestAddFailsWithoutInstall(t *testing.T) {
	app := newRawTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout
	err := app.run([]string{"add", "agent", "echo-job", "--schedule", "daily", "--", "/bin/echo"})
	if err == nil || err.Error() != "add agent requires install to be run first" {
		t.Fatalf("add error = %v", err)
	}
}

func TestAddInstallsManagedJobFromArgvWithoutConfig(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout
	runner := app.runner.(*fakeRunner)
	wd := testHome(t)
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(wd); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	actualWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() after chdir error = %v", err)
	}
	defer func() {
		_ = os.Chdir(prevWD)
	}()

	if err := app.run([]string{"add", "agent", "echo-job", "--schedule", "daily", "--hour", "0", "--minute", "15", "--", "/bin/echo", "hello", "--flag"}); err != nil {
		t.Fatalf("add error = %v", err)
	}
	if got := stdout.String(); got != "added echo-job\n" {
		t.Fatalf("stdout = %q", got)
	}
	if _, err := os.Stat(filepath.Join(wd, "summond.toml")); !os.IsNotExist(err) {
		t.Fatalf("expected no summond.toml, stat err = %v", err)
	}
	if len(runner.bootstrapped) != 1 || runner.bootstrapped[0] != "echo-job" {
		t.Fatalf("bootstrapped = %#v", runner.bootstrapped)
	}
	spec, err := app.store.Load("echo-job")
	if err != nil {
		t.Fatalf("Load(echo-job) error = %v", err)
	}
	if spec.Command != "/bin/echo" {
		t.Fatalf("Command = %q", spec.Command)
	}
	if len(spec.Args) != 2 || spec.Args[0] != "hello" || spec.Args[1] != "--flag" {
		t.Fatalf("Args = %#v", spec.Args)
	}
	if spec.WorkingDir != actualWD {
		t.Fatalf("WorkingDir = %q, want %q", spec.WorkingDir, actualWD)
	}
	if !spec.Schedule.HourSet || spec.Schedule.Hour != 0 || !spec.Schedule.MinuteSet || spec.Schedule.Minute != 15 {
		t.Fatalf("unexpected schedule: %+v", spec.Schedule)
	}
}

func TestAddDryRunPrintsPlanWithoutInstalling(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout
	runner := app.runner.(*fakeRunner)
	wd := testHome(t)
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(wd); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	defer func() {
		_ = os.Chdir(prevWD)
	}()

	if err := app.run([]string{"add", "agent", "echo-job", "--dry-run", "--schedule", "daily", "--hour", "0", "--minute", "15", "--", "/bin/echo", "hello"}); err != nil {
		t.Fatalf("add dry-run error = %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "add will:\n") ||
		!strings.Contains(got, "- create agent job: echo-job (daily at 00:15)\n") ||
		!strings.Contains(got, "dry run: no changes made\n") {
		t.Fatalf("stdout = %q", got)
	}
	if len(runner.bootstrapped) != 0 {
		t.Fatalf("bootstrapped = %#v", runner.bootstrapped)
	}
	if _, err := app.store.Load("echo-job"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load(echo-job) error = %v, want not exist", err)
	}
}

func TestAddInstallsManagedJobFromShellStdin(t *testing.T) {
	app := newTestApp(t)
	app.stdin = strings.NewReader("echo from-stdin\nexit 0\n")
	var stdout bytes.Buffer
	app.stdout = &stdout

	if err := app.run([]string{"add", "agent", "script-job", "--schedule", "daily", "--hour", "3", "--minute", "45"}); err != nil {
		t.Fatalf("add error = %v", err)
	}
	if got := stdout.String(); got != "added script-job\n" {
		t.Fatalf("stdout = %q", got)
	}
	spec, err := app.store.Load("script-job")
	if err != nil {
		t.Fatalf("Load(script-job) error = %v", err)
	}
	if spec.ShellCommand != "echo from-stdin\nexit 0" {
		t.Fatalf("ShellCommand = %q", spec.ShellCommand)
	}
}

func TestAddCanAbandonProcessGroup(t *testing.T) {
	app := newTestApp(t)

	if err := app.run([]string{"add", "agent", "launcher", "--schedule", "login", "--abandon-process-group", "--", "/bin/echo", "launch"}); err != nil {
		t.Fatalf("add error = %v", err)
	}
	spec, err := app.store.Load("launcher")
	if err != nil {
		t.Fatalf("Load(launcher) error = %v", err)
	}
	if !spec.AbandonProcessGroup {
		t.Fatal("AbandonProcessGroup = false, want true")
	}
}

func TestAddRequiresJobName(t *testing.T) {
	app := newTestApp(t)
	err := app.run([]string{"add", "agent", "--", "/bin/echo"})
	if err == nil || err.Error() != "add agent requires --schedule" {
		t.Fatalf("add error = %v", err)
	}
}

func TestAddRequiresSchedule(t *testing.T) {
	app := newTestApp(t)
	err := app.run([]string{"add", "agent", "cleanup", "--", "/bin/echo"})
	if err == nil || err.Error() != "add agent requires --schedule" {
		t.Fatalf("add error = %v", err)
	}
}

func TestAddRequiresSubcommand(t *testing.T) {
	app := newTestApp(t)
	err := app.run([]string{"add", "cleanup", "--schedule", "daily", "--", "/bin/echo"})
	if err == nil || err.Error() != "unknown flag: --schedule" {
		t.Fatalf("add error = %v", err)
	}
}

func TestAddRejectsDuplicateJobName(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout

	if _, err := app.store.Install(job.Spec{
		Name:    "cleanup",
		Command: "/bin/echo",
		Schedule: job.Schedule{
			Kind: job.ScheduleDaily,
			Hour: 3, HourSet: true,
			Minute: 45, MinuteSet: true,
		},
	}); err != nil {
		t.Fatalf("Install(cleanup) error = %v", err)
	}

	err := app.run([]string{"add", "agent", "cleanup", "--schedule", "daily", "--", "/bin/echo"})
	if err == nil || !strings.Contains(err.Error(), `job "cleanup" already exists`) {
		t.Fatalf("add error = %v", err)
	}
}

func TestAddDaemonInstallsManagedJob(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout

	if err := app.run([]string{"add", "daemon", "daemon-job", "--schedule", "boot", "--", "/bin/echo", "hello"}); err != nil {
		t.Fatalf("add daemon error = %v", err)
	}
	if got := stdout.String(); got != "added daemon-job\n" {
		t.Fatalf("stdout = %q", got)
	}
	spec, err := app.daemonStore.Load("daemon-job")
	if err != nil {
		t.Fatalf("Load(daemon-job) error = %v", err)
	}
	if spec.Target != job.TargetDaemon {
		t.Fatalf("Target = %q", spec.Target)
	}
	if spec.Schedule.Kind != job.ScheduleBoot {
		t.Fatalf("Schedule = %+v", spec.Schedule)
	}
}
