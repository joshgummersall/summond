package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

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

func TestExecInDaemonScopeRunsDaemonJob(t *testing.T) {
	app := newDaemonTestApp(t)

	spec, err := app.store.Install(job.Spec{
		Name:     "daemon-job",
		Target:   job.TargetDaemon,
		Command:  "/usr/bin/true",
		Schedule: job.Schedule{Kind: job.ScheduleBoot},
	})
	if err != nil {
		t.Fatalf("Install(daemon-job) error = %v", err)
	}

	if err := app.run([]string{"exec", spec.Name}); err != nil {
		t.Fatalf("exec error = %v", err)
	}
	loaded, err := app.store.Load(spec.Name)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.RunCount != 1 {
		t.Fatalf("RunCount = %d, want 1", loaded.RunCount)
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

	exitCode, _, err := app.executeSpec(job.Spec{
		Name:                "hello",
		Target:              job.TargetAgent,
		Command:             "hello",
		EnvironmentFilePath: envPath,
		Schedule: job.Schedule{
			Kind: job.ScheduleDaily,
		},
	}, nil, 0, nil)
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

func TestExecWritesOutputToLogFiles(t *testing.T) {
	app := newTestApp(t)
	var stdout, stderr bytes.Buffer
	app.stdout = &stdout
	app.stderr = &stderr

	configPath := filepath.Join(testHome(t), "summond.toml")
	data := strings.Join([]string{
		"[jobs.logger]",
		`command = "/bin/sh"`,
		`args = ["-c", "echo out; echo err >&2"]`,
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
	stderr.Reset()
	if err := app.run([]string{"exec", "logger"}); err != nil {
		t.Fatalf("exec error = %v", err)
	}
	if got := stdout.String(); got != "out\n" {
		t.Fatalf("stdout = %q", got)
	}
	if got := stderr.String(); got != "err\n" {
		t.Fatalf("stderr = %q", got)
	}
	spec, err := app.store.Load("logger")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	for path, want := range map[string]string{spec.StdoutPath: "out\n", spec.StderrPath: "err\n"} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", path, err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", path, got, want)
		}
	}
}

func TestExecDoesNotDuplicateWhenStdoutIsLogFile(t *testing.T) {
	app := newTestApp(t)
	configPath := filepath.Join(testHome(t), "summond.toml")
	data := strings.Join([]string{
		"[jobs.logger]",
		`command = "/bin/echo"`,
		`args = ["once"]`,
		`schedule = "daily"`,
		"hour = 3",
		"minute = 45",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	app.stdout = &bytes.Buffer{}
	if err := app.run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	spec, err := app.store.Load("logger")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Simulate launchd, which redirects stdout to the log file via the plist.
	f, err := os.OpenFile(spec.StdoutPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatalf("OpenFile() error = %v", err)
	}
	defer f.Close()
	app.stdout = f
	if err := app.run([]string{"exec", "logger"}); err != nil {
		t.Fatalf("exec error = %v", err)
	}
	got, err := os.ReadFile(spec.StdoutPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != "once\n" {
		t.Fatalf("log = %q, want %q", got, "once\n")
	}
}

// A SIGTERM from launchd (restart, kill, logout) must not leave the run
// recorded as "running". summond runs in its own process group, as under
// launchd, and only summond is signaled. The shell_command case covers bash
// deferring SIGTERM while it waits on a foreground child (brew in issue #22).
func TestExecRecordsRunStoppedBySIGTERM(t *testing.T) {
	cases := map[string]string{
		"command":       "command = \"/bin/sh\"\nargs = [\"-c\", \"touch $MARKER && exec sleep 30\"]",
		"shell_command": "shell_command = \"touch $MARKER && sleep 30\"",
	}
	for name, job := range cases {
		t.Run(name, func(t *testing.T) {
			home := testHome(t)
			marker := filepath.Join(home, "started")
			app := newTestAppAt(t, home)
			configPath := filepath.Join(home, "summond.toml")
			data := strings.Join([]string{
				"[jobs.sleeper]",
				strings.ReplaceAll(job, "$MARKER", marker),
				`schedule = "daily"`,
				"hour = 3",
				"minute = 45",
				"[jobs.sleeper.retry]",
				"attempts = 2",
			}, "\n")
			if err := os.WriteFile(configPath, []byte(data), 0o644); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			if err := app.run([]string{"apply", configPath}); err != nil {
				t.Fatalf("apply error = %v", err)
			}

			cmd := exec.Command(os.Args[0], "-test.run=^TestExecSIGTERMHelper$")
			cmd.Env = append(os.Environ(), "SUMMOND_SIGTERM_HELPER_HOME="+home)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := cmd.Start(); err != nil {
				t.Fatalf("Start() error = %v", err)
			}
			defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("job never started")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatalf("Signal() error = %v", err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("job was not stopped by SIGTERM")
			}
			if got := cmd.ProcessState.ExitCode(); got != 128+int(syscall.SIGTERM) {
				t.Fatalf("exit code = %d", got)
			}

			spec, err := app.store.Load("sleeper")
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if spec.LastFinishedAt == nil {
				t.Fatal("run still recorded as running")
			}
			if spec.RunCount != 1 || spec.FailureCount != 1 {
				t.Fatalf("RunCount = %d, FailureCount = %d", spec.RunCount, spec.FailureCount)
			}
			if spec.LastExitCode == nil || *spec.LastExitCode != 128+int(syscall.SIGTERM) {
				t.Fatalf("LastExitCode = %v", spec.LastExitCode)
			}
		})
	}
}

// TestExecSIGTERMHelper runs "summond exec sleeper" in a child process for
// TestExecRecordsRunStoppedBySIGTERM.
func TestExecSIGTERMHelper(t *testing.T) {
	home := os.Getenv("SUMMOND_SIGTERM_HELPER_HOME")
	if home == "" {
		t.Skip("helper process only")
	}
	app := newTestAppAt(t, home)
	err := app.run([]string{"exec", "sleeper"})
	var exitErr ExitError
	if errors.As(err, &exitErr) {
		os.Exit(exitErr.Code)
	}
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// applyEchoJob applies a job that prints "out" to stdout and "err" to stderr,
// and returns its stored spec.
func applyEchoJob(t *testing.T, app *App) job.Spec {
	t.Helper()
	configPath := filepath.Join(testHome(t), "summond.toml")
	data := strings.Join([]string{
		"[jobs.rotator]",
		`command = "/bin/sh"`,
		`args = ["-c", "echo out; echo err >&2"]`,
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
	spec, err := app.store.Load("rotator")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return spec
}

func setLogRotateMax(t *testing.T, max int64) {
	t.Helper()
	old := logRotateMaxBytes
	logRotateMaxBytes = max
	t.Cleanup(func() { logRotateMaxBytes = old })
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	return string(data)
}

func TestExecRotatesLogsOverCap(t *testing.T) {
	setLogRotateMax(t, 8)
	app := newTestApp(t)
	var stdout, stderr bytes.Buffer
	app.stdout = &stdout
	app.stderr = &stderr
	spec := applyEchoJob(t, app)

	oldOut := "old stdout over cap\n"
	oldErr := "old stderr over cap\n"
	for path, content := range map[string]string{spec.StdoutPath: oldOut, spec.StderrPath: oldErr} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		if err := os.WriteFile(path+".1", []byte("older copy\n"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}

	if err := app.run([]string{"exec", "rotator"}); err != nil {
		t.Fatalf("exec error = %v", err)
	}
	for path, want := range map[string]string{
		spec.StdoutPath:        "out\n",
		spec.StderrPath:        "err\n",
		spec.StdoutPath + ".1": oldOut,
		spec.StderrPath + ".1": oldErr,
	} {
		if got := readFile(t, path); got != want {
			t.Fatalf("%s = %q, want %q", path, got, want)
		}
	}

	spec, err := app.store.Load("rotator")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if spec.LastStdoutOffset != 0 || spec.LastStderrOffset != 0 {
		t.Fatalf("offsets = %d, %d, want 0, 0", spec.LastStdoutOffset, spec.LastStderrOffset)
	}

	stdout.Reset()
	stderr.Reset()
	if err := app.run([]string{"logs", "rotator"}); err != nil {
		t.Fatalf("logs error = %v", err)
	}
	if got := stdout.String(); got != "out\n" {
		t.Fatalf("logs stdout = %q", got)
	}
	if got := stderr.String(); got != "err\n" {
		t.Fatalf("logs stderr = %q", got)
	}
}

func TestExecLeavesLogsUnderCap(t *testing.T) {
	setLogRotateMax(t, 1<<20)
	app := newTestApp(t)
	app.stdout = &bytes.Buffer{}
	app.stderr = &bytes.Buffer{}
	spec := applyEchoJob(t, app)

	if err := os.WriteFile(spec.StdoutPath, []byte("prev\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := app.run([]string{"exec", "rotator"}); err != nil {
		t.Fatalf("exec error = %v", err)
	}
	if got := readFile(t, spec.StdoutPath); got != "prev\nout\n" {
		t.Fatalf("stdout log = %q", got)
	}
	if _, err := os.Stat(spec.StdoutPath + ".1"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(.1) error = %v, want not exist", err)
	}
	spec, err := app.store.Load("rotator")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if spec.LastStdoutOffset != int64(len("prev\n")) {
		t.Fatalf("stdout offset = %d", spec.LastStdoutOffset)
	}
}

// launchd opens the log with O_APPEND before exec starts. After rotation the
// same open file must still be the log, so output lands in the truncated file
// once, without a duplicate from teeToLog.
func TestExecRotationKeepsLaunchdLogFile(t *testing.T) {
	setLogRotateMax(t, 8)
	app := newTestApp(t)
	app.stdout = &bytes.Buffer{}
	app.stderr = &bytes.Buffer{}
	spec := applyEchoJob(t, app)

	if err := os.WriteFile(spec.StdoutPath, []byte("old stdout over cap\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	f, err := os.OpenFile(spec.StdoutPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatalf("OpenFile() error = %v", err)
	}
	defer f.Close()
	app.stdout = f

	if err := app.run([]string{"exec", "rotator"}); err != nil {
		t.Fatalf("exec error = %v", err)
	}
	if got := readFile(t, spec.StdoutPath); got != "out\n" {
		t.Fatalf("stdout log = %q, want %q", got, "out\n")
	}
	if got := readFile(t, spec.StdoutPath+".1"); got != "old stdout over cap\n" {
		t.Fatalf("stdout log.1 = %q", got)
	}
}
