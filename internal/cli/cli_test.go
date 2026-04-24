package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/standardlabs/summond/internal/bootstrap"
	"github.com/standardlabs/summond/internal/config"
	"github.com/standardlabs/summond/internal/job"
	"github.com/standardlabs/summond/internal/state"
)

type fakeRunner struct {
	bootstrapped []string
	bootedOut    []string
	kickstarted  []string
	stopped      []string
	bootstrapErr map[string]error
	bootoutErr   map[string]error
	printErr     map[string]error
	printText    map[string]string
	printCalls   map[string]int
}

func (f *fakeRunner) Bootstrap(spec job.Spec) error {
	f.bootstrapped = append(f.bootstrapped, spec.Name)
	if f.bootstrapErr != nil {
		if err, ok := f.bootstrapErr[spec.Name]; ok {
			return err
		}
	}
	return nil
}

func (f *fakeRunner) Bootout(spec job.Spec) error {
	f.bootedOut = append(f.bootedOut, spec.Name)
	if f.bootoutErr != nil {
		if err, ok := f.bootoutErr[spec.Name]; ok {
			return err
		}
	}
	return nil
}

func (f *fakeRunner) Kickstart(spec job.Spec) error {
	f.kickstarted = append(f.kickstarted, spec.Name)
	return nil
}

func (f *fakeRunner) Stop(spec job.Spec) error {
	f.stopped = append(f.stopped, spec.Name)
	return nil
}

func (f *fakeRunner) Print(spec job.Spec) (string, error) {
	if f.printCalls == nil {
		f.printCalls = map[string]int{}
	}
	f.printCalls[spec.Name]++
	if f.printErr != nil {
		if err, ok := f.printErr[spec.Name]; ok && f.printCalls[spec.Name] == 1 {
			return "", err
		}
	}
	if f.printText != nil {
		if text, ok := f.printText[spec.Name]; ok {
			return text, nil
		}
	}
	var b strings.Builder
	b.WriteString(spec.Label)
	b.WriteString("\n")
	b.WriteString(spec.StdoutPath)
	b.WriteString("\n")
	b.WriteString(spec.StderrPath)
	b.WriteString("\n")
	for _, path := range spec.WatchPaths {
		b.WriteString(path)
		b.WriteString("\n")
	}
	b.WriteString(spec.RuntimeBinaryPath)
	b.WriteString("\n")
	b.WriteString("exec\n")
	b.WriteString(spec.Name)
	b.WriteString("\n")
	return b.String(), nil
}

func TestRunVersion(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout

	if err := app.Run([]string{"version"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := stdout.String(), "summond 1.0.0\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}


func TestApplyFailsWithoutInstall(t *testing.T) {
	app := newRawTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout
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
	if err := os.WriteFile("summond.toml", []byte("[jobs.cleanup]\ncommand = \"/bin/echo\"\nschedule = \"daily\"\nhour = 3\nminute = 45\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err = app.Run([]string{"apply"})
	if err == nil || err.Error() != "apply requires install to be run first" {
		t.Fatalf("apply error = %v", err)
	}
}

func TestAddFailsWithoutInstall(t *testing.T) {
	app := newRawTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout
	err := app.Run([]string{"add", "agent", "echo-job", "--schedule", "daily", "--", "/bin/echo"})
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

	if err := app.Run([]string{"add", "agent", "echo-job", "--schedule", "daily", "--hour", "0", "--minute", "15", "--", "/bin/echo", "hello", "--flag"}); err != nil {
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

func TestAddInstallsManagedJobFromShellStdin(t *testing.T) {
	app := newTestApp(t)
	app.stdin = strings.NewReader("echo from-stdin\nexit 0\n")
	var stdout bytes.Buffer
	app.stdout = &stdout

	if err := app.Run([]string{"add", "agent", "script-job", "--schedule", "daily", "--hour", "3", "--minute", "45"}); err != nil {
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

func TestAddRequiresJobName(t *testing.T) {
	app := newTestApp(t)
	err := app.Run([]string{"add", "agent", "--", "/bin/echo"})
	if err == nil || err.Error() != "add agent requires --schedule" {
		t.Fatalf("add error = %v", err)
	}
}

func TestAddRequiresSchedule(t *testing.T) {
	app := newTestApp(t)
	err := app.Run([]string{"add", "agent", "cleanup", "--", "/bin/echo"})
	if err == nil || err.Error() != "add agent requires --schedule" {
		t.Fatalf("add error = %v", err)
	}
}

func TestAddRequiresSubcommand(t *testing.T) {
	app := newTestApp(t)
	err := app.Run([]string{"add", "cleanup", "--schedule", "daily", "--", "/bin/echo"})
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

	err := app.Run([]string{"add", "agent", "cleanup", "--schedule", "daily", "--", "/bin/echo"})
	if err == nil || !strings.Contains(err.Error(), `job "cleanup" already exists`) {
		t.Fatalf("add error = %v", err)
	}
}

func TestAddDaemonInstallsManagedJob(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout

	if err := app.Run([]string{"add", "daemon", "daemon-job", "--schedule", "boot", "--", "/bin/echo", "hello"}); err != nil {
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


func TestApplyConfig(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout
	runner := app.runner.(*fakeRunner)

	configPath := filepath.Join(testHome(t), "summond.toml")
	data := strings.Join([]string{
		"[jobs.cleanup]",
		`command = "/bin/echo"`,
		`args = ["clean"]`,
		`target = "agent"`,
		`schedule = "daily"`,
		"hour = 3",
		"minute = 45",
		"",
		"[jobs.cleanup.env]",
		`MODE = "nightly"`,
	}, "\n")
	if err := os.WriteFile(configPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := app.Run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	if len(runner.bootedOut) != 1 || runner.bootedOut[0] != "cleanup" {
		t.Fatalf("bootedOut = %#v", runner.bootedOut)
	}
	if len(runner.bootstrapped) != 1 || runner.bootstrapped[0] != "cleanup" {
		t.Fatalf("bootstrapped = %#v", runner.bootstrapped)
	}
	spec, err := app.store.Load("cleanup")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := spec.EnvironmentFilePath, app.store.EnvFilePath(); got != want {
		t.Fatalf("EnvironmentFilePath = %q, want %q", got, want)
	}
	if got := stdout.String(); got != "applied 1 job(s)\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestApplyDefaultsToSummondTomlInWorkingDirectory(t *testing.T) {
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

	data := strings.Join([]string{
		"[jobs.cleanup]",
		`command = "/bin/echo"`,
		`args = ["clean"]`,
		`target = "agent"`,
		`schedule = "daily"`,
		"hour = 3",
		"minute = 45",
	}, "\n")
	if err := os.WriteFile("summond.toml", []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := app.Run([]string{"apply"}); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	if len(runner.bootstrapped) != 1 || runner.bootstrapped[0] != "cleanup" {
		t.Fatalf("bootstrapped = %#v", runner.bootstrapped)
	}
	if got := stdout.String(); got != "applied 1 job(s)\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestApplyFailsWhenBootstrapFails(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout
	runner := app.runner.(*fakeRunner)
	runner.bootstrapErr = map[string]error{"cleanup": errors.New("launchctl bootstrap failed")}

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

	err := app.Run([]string{"apply", configPath})
	var exitErr ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 1 {
		t.Fatalf("apply error = %v", err)
	}
	if len(runner.bootedOut) < 1 || runner.bootedOut[0] != "cleanup" {
		t.Fatalf("bootedOut = %#v", runner.bootedOut)
	}
	if len(runner.bootstrapped) != 1 || runner.bootstrapped[0] != "cleanup" {
		t.Fatalf("bootstrapped = %#v", runner.bootstrapped)
	}
	if got := stdout.String(); got != "applied 0 job(s)\nfailed 1 job(s):\n- cleanup: bootstrap failed: launchctl bootstrap failed\n" {
		t.Fatalf("stdout = %q", got)
	}
	if _, err := app.store.Load("cleanup"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load(cleanup) error = %v, want not exist", err)
	}
}

func TestApplyRollbackRestoresPreviousSpecOnFailure(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout
	runner := app.runner.(*fakeRunner)

	configPath := filepath.Join(testHome(t), "summond.toml")
	initialData := strings.Join([]string{
		`group = "tests"`,
		"",
		"[jobs.cleanup]",
		`command = "/bin/echo"`,
		`args = ["old"]`,
		`target = "agent"`,
		`schedule = "daily"`,
		"hour = 3",
		"minute = 45",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(initialData), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := app.Run([]string{"apply", configPath}); err != nil {
		t.Fatalf("initial apply error = %v", err)
	}

	stdout.Reset()
	runner.bootstrapped = nil
	runner.bootedOut = nil
	runner.bootstrapErr = map[string]error{"cleanup": errors.New("launchctl bootstrap failed")}

	updatedData := strings.Join([]string{
		`group = "tests"`,
		"",
		"[jobs.cleanup]",
		`command = "/bin/echo"`,
		`args = ["new"]`,
		`target = "agent"`,
		`schedule = "daily"`,
		"hour = 3",
		"minute = 45",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(updatedData), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := app.Run([]string{"apply", configPath})
	var exitErr ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 1 {
		t.Fatalf("apply error = %v", err)
	}
	spec, loadErr := app.store.Load("cleanup")
	if loadErr != nil {
		t.Fatalf("Load(cleanup) error = %v", loadErr)
	}
	if got, want := spec.Args, []string{"old"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("Args = %#v, want %#v", got, want)
	}
}

func TestApplyRollbackRemovesNewSpecOnFailure(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout
	runner := app.runner.(*fakeRunner)
	runner.bootstrapErr = map[string]error{"cleanup": errors.New("launchctl bootstrap failed")}

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

	err := app.Run([]string{"apply", configPath})
	var exitErr ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 1 {
		t.Fatalf("apply error = %v", err)
	}
	if _, loadErr := app.store.Load("cleanup"); !errors.Is(loadErr, os.ErrNotExist) {
		t.Fatalf("Load(cleanup) error = %v, want not exist", loadErr)
	}
}

func TestApplySkipsBootoutWhenServiceIsMissing(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout
	runner := app.runner.(*fakeRunner)
	runner.printErr = map[string]error{"cleanup": errors.New("Could not find service \"com.standardlabs.summond.cleanup\" in domain")}

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

	if err := app.Run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	if len(runner.bootedOut) != 0 {
		t.Fatalf("bootedOut = %#v", runner.bootedOut)
	}
	if len(runner.bootstrapped) != 1 || runner.bootstrapped[0] != "cleanup" {
		t.Fatalf("bootstrapped = %#v", runner.bootstrapped)
	}
	if got := stdout.String(); strings.Contains(got, "bootout before bootstrap failed") || strings.Contains(got, "load-state check failed") {
		t.Fatalf("stdout = %q", got)
	}
}

func TestApplyFailsWhenBootoutFails(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout
	runner := app.runner.(*fakeRunner)
	runner.bootoutErr = map[string]error{"cleanup": errors.New("launchctl bootout failed")}

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

	err := app.Run([]string{"apply", configPath})
	var exitErr ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 1 {
		t.Fatalf("apply error = %v", err)
	}
	if len(runner.bootedOut) < 1 || runner.bootedOut[0] != "cleanup" {
		t.Fatalf("bootedOut = %#v", runner.bootedOut)
	}
	if got := stdout.String(); got != "applied 0 job(s)\nfailed 1 job(s):\n- cleanup: bootout before bootstrap failed: launchctl bootout failed\n" {
		t.Fatalf("stdout = %q", got)
	}
	if _, err := app.store.Load("cleanup"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load(cleanup) error = %v, want not exist", err)
	}
}

func TestApplyFailsWhenVerificationFails(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout
	runner := app.runner.(*fakeRunner)
	runner.printText = map[string]string{"cleanup": "com.standardlabs.summond.cleanup\n/bin/echo\n"}

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

	err := app.Run([]string{"apply", configPath})
	var exitErr ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 1 {
		t.Fatalf("apply error = %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "applied 0 job(s)\nfailed 1 job(s):\n- cleanup: loaded job verification failed: missing ") {
		t.Fatalf("stdout = %q", got)
	}
}

func TestApplyUsesChecksumForVerificationWhenAvailable(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout
	runner := app.runner.(*fakeRunner)

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

	specs, err := config.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	spec := specs[0]
	spec.EnvironmentFilePath = app.store.EnvFilePath()
	spec.RuntimeBinaryPath = app.store.RuntimeBinaryPath()
	installed, err := app.store.Install(spec)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	runner.printText = map[string]string{"cleanup": "loaded checksum " + installed.Checksum}
	stdout.Reset()

	if err := app.Run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	if got := stdout.String(); got != "applied 1 job(s)\n" {
		t.Fatalf("stdout = %q", got)
	}
}

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
	if err := app.Run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	err := app.Run([]string{"exec", "failer"})
	var exitErr ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 7 {
		t.Fatalf("exec error = %#v", err)
	}
	if exitErr.Error() == "" {
		t.Fatal("expected exec error message")
	}
}

func TestApplyPromptsAboutOrphanedManagedJobs(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stdin = strings.NewReader("n\n")

	initialConfigPath := filepath.Join(testHome(t), "before.toml")
	initialData := strings.Join([]string{
		`group = "tests"`,
		"",
		"[jobs.cleanup]",
		`command = "/bin/echo"`,
		`schedule = "daily"`,
		"hour = 3",
		"minute = 45",
		"",
		"[jobs.sync]",
		`command = "/bin/echo"`,
		`schedule = "hourly"`,
		"minute = 15",
	}, "\n")
	if err := os.WriteFile(initialConfigPath, []byte(initialData), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := app.Run([]string{"apply", initialConfigPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	stdout.Reset()

	updatedConfigPath := filepath.Join(testHome(t), "after.toml")
	updatedData := strings.Join([]string{
		`group = "tests"`,
		"",
		"[jobs.cleanup]",
		`command = "/bin/echo"`,
		`schedule = "daily"`,
		"hour = 3",
		"minute = 45",
	}, "\n")
	if err := os.WriteFile(updatedConfigPath, []byte(updatedData), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := app.Run([]string{"apply", updatedConfigPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "applied 1 job(s)\n") || !strings.Contains(got, "jobs to prune:\n- sync\n") || !strings.Contains(got, "apply will remove these Summond-managed jobs. Continue? [y/N]: ") || !strings.Contains(got, "prune cancelled\n") {
		t.Fatalf("stdout = %q", got)
	}
	if _, err := app.store.Load("sync"); err != nil {
		t.Fatalf("expected sync metadata to remain, err = %v", err)
	}
}

func TestApplyPrunesManagedJobsMissingFromConfigWithFlag(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout
	runner := app.runner.(*fakeRunner)

	firstConfigPath := filepath.Join(testHome(t), "before.toml")
	firstData := strings.Join([]string{
		`group = "tests"`,
		"",
		"[jobs.cleanup]",
		`command = "/bin/echo"`,
		`schedule = "daily"`,
		"hour = 3",
		"minute = 45",
		"",
		"[jobs.sync]",
		`command = "/bin/echo"`,
		`schedule = "hourly"`,
		"minute = 15",
	}, "\n")
	if err := os.WriteFile(firstConfigPath, []byte(firstData), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := app.Run([]string{"apply", firstConfigPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	stdout.Reset()
	runner.bootedOut = nil

	pruneConfigPath := filepath.Join(testHome(t), "after.toml")
	pruneData := strings.Join([]string{
		`group = "tests"`,
		"",
		"[jobs.cleanup]",
		`command = "/bin/echo"`,
		`schedule = "daily"`,
		"hour = 3",
		"minute = 45",
	}, "\n")
	if err := os.WriteFile(pruneConfigPath, []byte(pruneData), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := app.Run([]string{"apply", "--prune", pruneConfigPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	if got := stdout.String(); got != "applied 1 job(s)\njobs to prune:\n- sync\npruned 1 job(s)\n" {
		t.Fatalf("stdout = %q", got)
	}
	if !containsString(runner.bootedOut, "sync") {
		t.Fatalf("bootedOut = %#v", runner.bootedOut)
	}
	if _, err := app.store.Load("sync"); err == nil {
		t.Fatalf("expected sync metadata to be removed")
	}
	if _, err := app.store.Load("cleanup"); err != nil {
		t.Fatalf("expected cleanup metadata to remain, err = %v", err)
	}
}

func TestApplyPrunesPerJobLogsAndLockFiles(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout

	stale, err := app.store.Install(job.Spec{
		Group:    "tests",
		Name:     "stale",
		Command:  "/bin/echo",
		Schedule: job.Schedule{Kind: job.ScheduleDaily, Hour: 3, HourSet: true, Minute: 45, MinuteSet: true},
	})
	if err != nil {
		t.Fatalf("Install(stale) error = %v", err)
	}
	if err := os.WriteFile(stale.StdoutPath, []byte("stdout\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(stdout) error = %v", err)
	}
	if err := os.WriteFile(stale.StderrPath, []byte("stderr\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(stderr) error = %v", err)
	}
	if err := os.WriteFile(app.store.MetadataPathForSpec(stale)+".lock", []byte("lock"), 0o644); err != nil {
		t.Fatalf("WriteFile(lock) error = %v", err)
	}

	configPath := filepath.Join(testHome(t), "current.toml")
	if err := os.WriteFile(configPath, []byte("group = \"tests\"\n\n[jobs.cleanup]\ncommand = \"/bin/echo\"\nschedule = \"daily\"\nhour = 3\nminute = 45\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := app.Run([]string{"apply", "--prune", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	for _, path := range []string{
		stale.StdoutPath,
		stale.StderrPath,
		app.store.MetadataPathForSpec(stale),
		app.store.MetadataPathForSpec(stale) + ".lock",
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, stat err = %v", path, err)
		}
	}
}

func TestRemoveDeletesAgentJobAndState(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout
	runner := app.runner.(*fakeRunner)

	spec, err := app.store.Install(job.Spec{
		Group:    "tests",
		Name:     "cleanup",
		Command:  "/bin/echo",
		Schedule: job.Schedule{Kind: job.ScheduleDaily, Hour: 3, HourSet: true, Minute: 45, MinuteSet: true},
	})
	if err != nil {
		t.Fatalf("Install(cleanup) error = %v", err)
	}
	if err := os.WriteFile(spec.StdoutPath, []byte("stdout\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(stdout) error = %v", err)
	}
	if err := os.WriteFile(spec.StderrPath, []byte("stderr\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(stderr) error = %v", err)
	}

	if err := app.Run([]string{"remove", "cleanup"}); err != nil {
		t.Fatalf("remove error = %v", err)
	}
	if got := stdout.String(); got != "removed cleanup\n" {
		t.Fatalf("stdout = %q", got)
	}
	if !containsString(runner.bootedOut, "cleanup") {
		t.Fatalf("bootedOut = %#v", runner.bootedOut)
	}
	for _, path := range []string{
		app.store.JobDirForSpec(spec),
		spec.StdoutPath,
		spec.StderrPath,
		app.store.MetadataPathForSpec(spec),
		app.store.MetadataPathForSpec(spec) + ".lock",
		spec.PlistPath,
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, stat err = %v", path, err)
		}
	}
}

func TestRemoveDaemonJobRetriesWithSudo(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stdin = strings.NewReader("y\n")
	runner := app.runner.(*fakeRunner)
	runner.bootoutErr = map[string]error{"cleanup": errors.New("launchctl bootout failed")}
	priv := app.priv.(*fakePrivilegedOperator)

	spec, err := app.daemonStore.Install(job.Spec{
		Group:    "tests",
		Name:     "cleanup",
		Target:   job.TargetDaemon,
		Command:  "/bin/echo",
		Schedule: job.Schedule{Kind: job.ScheduleBoot},
	})
	if err != nil {
		t.Fatalf("Install(cleanup daemon) error = %v", err)
	}

	if err := app.Run([]string{"remove", "cleanup"}); err != nil {
		t.Fatalf("remove error = %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "removing daemon jobs requires sudo. Retry with sudo? [Y/n]: ") || !strings.Contains(got, "removed cleanup\n") {
		t.Fatalf("stdout = %q", got)
	}
	if !containsString(priv.bootout, spec.PlistPath) {
		t.Fatalf("priv.bootout = %#v", priv.bootout)
	}
	if !containsString(priv.removed, spec.PlistPath) ||
		!containsString(priv.removed, app.daemonStore.MetadataPathForSpec(spec)) ||
		!containsString(priv.removed, app.daemonStore.JobDirForSpec(spec)) {
		t.Fatalf("priv.removed = %#v", priv.removed)
	}
}

func TestRemoveDaemonJobDeletesStaleJobDirectoryFromList(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stdin = strings.NewReader("y\n")
	runner := app.runner.(*fakeRunner)
	runner.bootoutErr = map[string]error{"cleanup": errors.New("launchctl bootout failed")}

	spec, err := app.daemonStore.Install(job.Spec{
		Group:    "tests",
		Name:     "cleanup",
		Target:   job.TargetDaemon,
		Command:  "/bin/echo",
		Schedule: job.Schedule{Kind: job.ScheduleBoot},
	})
	if err != nil {
		t.Fatalf("Install(cleanup daemon) error = %v", err)
	}
	started := time.Now().Add(-time.Second).UTC()
	finished := time.Now().UTC()
	exitCode := 0
	if err := app.daemonStore.RecordExecutionStart(spec.Name, started); err != nil {
		t.Fatalf("RecordExecutionStart() error = %v", err)
	}
	if err := app.daemonStore.RecordExecutionFinish(spec.Name, job.ExecutionRecord{
		StartedAt:  started,
		FinishedAt: &finished,
		ExitCode:   &exitCode,
	}); err != nil {
		t.Fatalf("RecordExecutionFinish() error = %v", err)
	}

	if err := app.Run([]string{"remove", "cleanup"}); err != nil {
		t.Fatalf("remove error = %v", err)
	}
	stdout.Reset()

	if err := app.Run([]string{"list"}); err != nil {
		t.Fatalf("list error = %v", err)
	}
	if got := stdout.String(); got != "no managed jobs\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestApplyPrunePromptCancelsWithoutFlag(t *testing.T) {
	home := testHome(t)
	store := state.NewStore(state.Paths{
		Home:         filepath.Join(home, "managed"),
		AgentsDir:    filepath.Join(home, "LaunchAgents"),
		DaemonsDir:   filepath.Join(home, "LaunchDaemons"),
		NewsyslogDir: filepath.Join(home, "newsyslog.d"),
	})
	runner := &fakeRunner{}
	app := NewApp(strings.NewReader("n\n"), &bytes.Buffer{}, store, runner, bootstrap.NewManager(store.Paths(), &fakeBootstrapInstaller{}))
	app.priv = &fakePrivilegedOperator{}
	var stdout bytes.Buffer
	app.stdout = &stdout
	if err := os.MkdirAll(store.Paths().Home, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.Paths().Home, ".installed"), []byte("installed\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	configPath := filepath.Join(home, "current.toml")
	if err := os.WriteFile(configPath, []byte("group = \"tests\"\n\n[jobs.cleanup]\ncommand = \"/bin/echo\"\nschedule = \"daily\"\nhour = 3\nminute = 45\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := app.Run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	stale := job.Spec{Group: "tests", Name: "stale", Command: "/bin/echo", Schedule: job.Schedule{Kind: "hourly"}, Target: job.TargetAgent}
	if _, err := store.Install(stale); err != nil {
		t.Fatalf("Install(stale) error = %v", err)
	}

	stdout.Reset()
	runner.bootedOut = nil

	app.stdin = strings.NewReader("n\n")
	if err := app.Run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "applied 1 job(s)\n") || !strings.Contains(got, "jobs to prune:\n- stale\n") || !strings.Contains(got, "apply will remove these Summond-managed jobs. Continue? [y/N]: ") || !strings.Contains(got, "prune cancelled\n") {
		t.Fatalf("stdout = %q", got)
	}
	if containsString(runner.bootedOut, "stale") {
		t.Fatalf("bootedOut = %#v", runner.bootedOut)
	}
	if _, err := store.Load("stale"); err != nil {
		t.Fatalf("expected stale metadata to remain, err = %v", err)
	}
}

func TestApplyWithPruneReportsNoJobsToPrune(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout

	configPath := filepath.Join(testHome(t), "summond.toml")
	if err := os.WriteFile(configPath, []byte("[jobs.cleanup]\ncommand = \"/bin/echo\"\nschedule = \"daily\"\nhour = 3\nminute = 45\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := app.Run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	stdout.Reset()
	if err := app.Run([]string{"apply", "--prune", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	if got := stdout.String(); got != "applied 1 job(s)\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestLogsTailArgsDefault(t *testing.T) {
	got := logsTailArgs(40, false, "/tmp/job.log")
	want := []string{"-n", "40", "/tmp/job.log"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("logsTailArgs() = %#v, want %#v", got, want)
	}
}

func TestLogsTailArgsFollow(t *testing.T) {
	got := logsTailArgs(12, true, "/tmp/job.log")
	want := []string{"-n", "12", "-f", "/tmp/job.log"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("logsTailArgs() = %#v, want %#v", got, want)
	}
}

func TestLogsRejectsNegativeLineCount(t *testing.T) {
	app := newTestApp(t)

	err := app.Run([]string{"logs", "-n", "-1", "cleanup"})
	if err == nil || err.Error() != "logs requires -n >= 0" {
		t.Fatalf("logs error = %v", err)
	}
}

func TestLogsWritesStdoutAndStderrSeparately(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app.stdout = &stdout
	app.stderr = &stderr

	spec, err := app.store.Install(job.Spec{
		Name:     "cleanup",
		Command:  "/bin/echo",
		Schedule: job.Schedule{Kind: job.ScheduleDaily, Hour: 3, HourSet: true, Minute: 45, MinuteSet: true},
	})
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if err := os.WriteFile(spec.StdoutPath, []byte("out-1\nout-2\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(stdout) error = %v", err)
	}
	if err := os.WriteFile(spec.StderrPath, []byte("err-1\nerr-2\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(stderr) error = %v", err)
	}

	if err := app.Run([]string{"logs", "-n", "1", "cleanup"}); err != nil {
		t.Fatalf("logs error = %v", err)
	}
	if got := stdout.String(); got != "out-2\n" {
		t.Fatalf("stdout = %q", got)
	}
	if got := stderr.String(); got != "err-2\n" {
		t.Fatalf("stderr = %q", got)
	}
}

func TestStatePrintsStateJSON(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout

	configPath := filepath.Join(testHome(t), "summond.toml")
	data := strings.Join([]string{
		"[jobs.watcher]",
		`command = "/bin/echo"`,
		`args = ["watch"]`,
		`target = "agent"`,
		`trigger = "on_change"`,
		`watch_paths = ["/tmp/watch.txt"]`,
	}, "\n")
	if err := os.WriteFile(configPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := app.Run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	stdout.Reset()
	if err := app.Run([]string{"state", "watcher"}); err != nil {
		t.Fatalf("state error = %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, `"name": "watcher"`) || !strings.Contains(got, `"trigger": "on_change"`) || !strings.Contains(got, `"/tmp/watch.txt"`) {
		t.Fatalf("stdout = %q", got)
	}
}

func TestStatePrintsShellCommandJSON(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout

	configPath := filepath.Join(testHome(t), "summond.toml")
	data := strings.Join([]string{
		"[jobs.scripted]",
		`shell_command = """`,
		"echo hello",
		"echo world",
		`"""`,
		`target = "agent"`,
		`schedule = "login"`,
	}, "\n")
	if err := os.WriteFile(configPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := app.Run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	stdout.Reset()
	if err := app.Run([]string{"state", "scripted"}); err != nil {
		t.Fatalf("state error = %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "\"shell_command\": \"echo hello\\necho world\\n\"") {
		t.Fatalf("stdout = %q", got)
	}
}

func TestStateShowsNoRecentRunsBeforeExecution(t *testing.T) {
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
	if err := app.Run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	stdout.Reset()
	if err := app.Run([]string{"state", "cleanup"}); err != nil {
		t.Fatalf("state error = %v", err)
	}
	if got := stdout.String(); strings.Contains(got, `"recent_runs":`) {
		t.Fatalf("stdout = %q", got)
	}
}

func TestStateShowsRecentRunsAfterExecution(t *testing.T) {
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
	if err := app.Run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	stdout.Reset()
	if err := app.Run([]string{"exec", "cleanup"}); err != nil {
		t.Fatalf("exec error = %v", err)
	}

	stdout.Reset()
	if err := app.Run([]string{"state", "cleanup"}); err != nil {
		t.Fatalf("state error = %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, `"run_count": 1`) || !strings.Contains(got, `"success_count": 1`) || !strings.Contains(got, `"recent_runs": [`) || !strings.Contains(got, `"exit_code": 0`) {
		t.Fatalf("stdout = %q", got)
	}
}

func TestPlistPrintsJobPlist(t *testing.T) {
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
	if err := app.Run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	stdout.Reset()
	if err := app.Run([]string{"plist", "cleanup"}); err != nil {
		t.Fatalf("plist error = %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "<plist") || !strings.Contains(got, "<key>Label</key>") || !strings.Contains(got, "<string>com.standardlabs.summond.") || !strings.Contains(got, "<key>ProgramArguments</key>") {
		t.Fatalf("stdout = %q", got)
	}
}

func TestListOutputsTSV(t *testing.T) {
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
	if err := app.Run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	stdout.Reset()
	if err := app.Run([]string{"list"}); err != nil {
		t.Fatalf("list error = %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "NAME") || !strings.Contains(got, "TARGET") || !strings.Contains(got, "SCHEDULE") || !strings.Contains(got, "STATUS") {
		t.Fatalf("stdout missing headers: %q", got)
	}
	if !strings.Contains(got, "cleanup") || !strings.Contains(got, "agent") || !strings.Contains(got, "daily at 03:45") || !strings.Contains(got, "never") {
		t.Fatalf("stdout = %q", got)
	}
	if strings.Contains(got, "\t") {
		t.Fatalf("stdout still contains raw tabs: %q", got)
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
	if err := app.Run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	stdout.Reset()
	if err := app.Run([]string{"exec", "cleanup"}); err != nil {
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
	if err := app.Run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	err := app.Run([]string{"exec", "failer"})
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

func TestInstallCreatesStarterFiles(t *testing.T) {
	app := newTestApp(t)
	app.stdin = strings.NewReader("y\n")
	var stdout bytes.Buffer
	app.stdout = &stdout
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(testHome(t)); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	defer func() {
		_ = os.Chdir(prevWD)
	}()

	if err := app.Run([]string{"install", "--skip-newsyslog"}); err != nil {
		t.Fatalf("install error = %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "install will:\n") || !strings.Contains(got, "Proceed with install? [y/N]: ") {
		t.Fatalf("unexpected output: %q", got)
	}
	if _, err := os.Stat("summond.toml"); err != nil {
		t.Fatalf("expected summond.toml in cwd, stat err = %v", err)
	}
}

func TestInstallPermissionDeniedCanUseSudoRetry(t *testing.T) {
	home := testHome(t)
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(home); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	defer func() {
		_ = os.Chdir(prevWD)
	}()
	store := state.NewStore(state.Paths{
		Home:         filepath.Join(home, "managed"),
		AgentsDir:    filepath.Join(home, "LaunchAgents"),
		DaemonsDir:   filepath.Join(home, "LaunchDaemons"),
		NewsyslogDir: filepath.Join(home, "newsyslog.d"),
	})
	installer := &fakeBootstrapInstaller{err: &bootstrap.PermissionError{Err: os.ErrPermission}}
	app := NewApp(strings.NewReader("y\ny\ny\n"), &bytes.Buffer{}, store, &fakeRunner{}, bootstrap.NewManager(store.Paths(), installer))
	var stdout bytes.Buffer
	app.stdout = &stdout

	if err := app.Run([]string{"install"}); err != nil {
		t.Fatalf("install error = %v", err)
	}
	if len(installer.sudoCalls) != 1 {
		t.Fatalf("sudoCalls = %#v", installer.sudoCalls)
	}
	if got := stdout.String(); !strings.Contains(got, "install will:\n") || !strings.Contains(got, "Proceed with install? [y/N]: ") || !strings.Contains(got, "newsyslog install requires sudo. Retry with sudo? [Y/n]: ") {
		t.Fatalf("unexpected output: %q", got)
	}
}

func TestInstallCancelSkipsChanges(t *testing.T) {
	home := testHome(t)
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(home); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	defer func() {
		_ = os.Chdir(prevWD)
	}()
	store := state.NewStore(state.Paths{
		Home:         filepath.Join(home, "managed"),
		AgentsDir:    filepath.Join(home, "LaunchAgents"),
		DaemonsDir:   filepath.Join(home, "LaunchDaemons"),
		NewsyslogDir: filepath.Join(home, "newsyslog.d"),
	})
	installer := &fakeBootstrapInstaller{err: &bootstrap.PermissionError{Err: os.ErrPermission}}
	app := NewApp(strings.NewReader("n\n"), &bytes.Buffer{}, store, &fakeRunner{}, bootstrap.NewManager(store.Paths(), installer))
	var stdout bytes.Buffer
	app.stdout = &stdout

	if err := app.Run([]string{"install"}); err != nil {
		t.Fatalf("install error = %v", err)
	}
	if len(installer.sudoCalls) != 0 || len(installer.installs) != 0 {
		t.Fatalf("unexpected installer activity: installs=%#v sudoCalls=%#v", installer.installs, installer.sudoCalls)
	}
	if got := stdout.String(); !strings.Contains(got, "Proceed with install? [y/N]: ") || !strings.Contains(got, "install cancelled\n") {
		t.Fatalf("unexpected prompt output: %q", got)
	}
}

func TestInstallSkipNewsyslogAvoidsPromptAndManualSudo(t *testing.T) {
	home := testHome(t)
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(home); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	defer func() {
		_ = os.Chdir(prevWD)
	}()
	store := state.NewStore(state.Paths{
		Home:         filepath.Join(home, "managed"),
		AgentsDir:    filepath.Join(home, "LaunchAgents"),
		DaemonsDir:   filepath.Join(home, "LaunchDaemons"),
		NewsyslogDir: filepath.Join(home, "newsyslog.d"),
	})
	installer := &fakeBootstrapInstaller{err: &bootstrap.PermissionError{Err: os.ErrPermission}}
	app := NewApp(strings.NewReader("y\n"), &bytes.Buffer{}, store, &fakeRunner{}, bootstrap.NewManager(store.Paths(), installer))
	var stdout bytes.Buffer
	app.stdout = &stdout

	if err := app.Run([]string{"install", "--skip-newsyslog"}); err != nil {
		t.Fatalf("install error = %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "install will:\n") || !strings.Contains(got, "- skip newsyslog generation and system install\n") || !strings.Contains(got, "Proceed with install? [y/N]: ") {
		t.Fatalf("unexpected output: %q", got)
	}
	if len(installer.installs) != 0 || len(installer.sudoCalls) != 0 {
		t.Fatalf("unexpected installer activity: installs=%#v sudoCalls=%#v", installer.installs, installer.sudoCalls)
	}
}

func TestInstallPlanShowsExistingConfigUnchangedWithoutOverwrite(t *testing.T) {
	app := newTestApp(t)
	app.stdin = strings.NewReader("n\n")
	var stdout bytes.Buffer
	app.stdout = &stdout
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
	if err := os.WriteFile("summond.toml", []byte("# existing\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := app.Run([]string{"install", "--skip-newsyslog"}); err != nil {
		t.Fatalf("install error = %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "- leave unchanged starter config: ") {
		t.Fatalf("unexpected output: %q", got)
	}
}

func TestUninstallRemovesManagedArtifacts(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout
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

	configPath := filepath.Join(wd, "summond.toml")
	data := strings.Join([]string{
		"[jobs.cleanup]",
		`command = "/bin/echo"`,
		`args = ["cleanup"]`,
		`target = "agent"`,
		`schedule = "hourly"`,
		"minute = 5",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := app.Run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	stdout.Reset()
	app.stdin = strings.NewReader("y\n")
	if err := app.Run([]string{"install", "--skip-newsyslog"}); err != nil {
		t.Fatalf("install error = %v", err)
	}
	stdout.Reset()

	app.stdin = strings.NewReader("y\n")
	if err := app.Run([]string{"uninstall"}); err != nil {
		t.Fatalf("uninstall error = %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "uninstall will:\n") || !strings.Contains(got, "Proceed with uninstall? [y/N]: ") {
		t.Fatalf("unexpected output: %q", got)
	}
	if _, err := os.Stat(app.store.Paths().Home); !os.IsNotExist(err) {
		t.Fatalf("expected managed state to be removed, stat err = %v", err)
	}
	if _, err := os.Stat("summond.toml"); err != nil {
		t.Fatalf("expected cwd config preserved, stat err = %v", err)
	}
}

func TestUninstallPermissionDeniedPromptsForNewsyslogCleanup(t *testing.T) {
	home := testHome(t)
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(home); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	defer func() {
		_ = os.Chdir(prevWD)
	}()
	store := state.NewStore(state.Paths{
		Home:         filepath.Join(home, "managed"),
		AgentsDir:    filepath.Join(home, "LaunchAgents"),
		DaemonsDir:   filepath.Join(home, "LaunchDaemons"),
		NewsyslogDir: filepath.Join(home, "newsyslog.d"),
	})
	installer := &fakeBootstrapInstaller{}
	app := NewApp(strings.NewReader("y\ny\ny\n"), &bytes.Buffer{}, store, &fakeRunner{}, bootstrap.NewManager(store.Paths(), installer))
	app.priv = &fakePrivilegedOperator{}
	var stdout bytes.Buffer
	app.stdout = &stdout

	if err := app.Run([]string{"install", "--skip-newsyslog"}); err != nil {
		t.Fatalf("install error = %v", err)
	}
	installer.removeErr = &bootstrap.PermissionError{Err: os.ErrPermission}
	stdout.Reset()
	app.stdin = strings.NewReader("y\ny\n")

	if err := app.Run([]string{"uninstall"}); err != nil {
		t.Fatalf("uninstall error = %v", err)
	}
	if len(installer.sudoCalls) != 1 {
		t.Fatalf("sudoCalls = %#v", installer.sudoCalls)
	}
	if got := stdout.String(); !strings.Contains(got, "Proceed with uninstall? [y/N]: ") || !strings.Contains(got, "some system-owned files require sudo to remove. Retry with sudo? [Y/n]: ") {
		t.Fatalf("unexpected output: %q", got)
	}
}

func TestUninstallCancelSkipsChanges(t *testing.T) {
	home := testHome(t)
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(home); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	defer func() {
		_ = os.Chdir(prevWD)
	}()
	store := state.NewStore(state.Paths{
		Home:         filepath.Join(home, "managed"),
		AgentsDir:    filepath.Join(home, "LaunchAgents"),
		DaemonsDir:   filepath.Join(home, "LaunchDaemons"),
		NewsyslogDir: filepath.Join(home, "newsyslog.d"),
	})
	installer := &fakeBootstrapInstaller{}
	app := NewApp(strings.NewReader("n\n"), &bytes.Buffer{}, store, &fakeRunner{}, bootstrap.NewManager(store.Paths(), installer))
	app.priv = &fakePrivilegedOperator{}
	var stdout bytes.Buffer
	app.stdout = &stdout

	app.stdin = strings.NewReader("y\n")
	if err := app.Run([]string{"install", "--skip-newsyslog"}); err != nil {
		t.Fatalf("install error = %v", err)
	}
	installer.removeErr = &bootstrap.PermissionError{Err: os.ErrPermission}
	stdout.Reset()

	app.stdin = strings.NewReader("n\n")
	if err := app.Run([]string{"uninstall"}); err != nil {
		t.Fatalf("uninstall error = %v", err)
	}
	if len(installer.sudoCalls) != 0 {
		t.Fatalf("sudoCalls = %#v", installer.sudoCalls)
	}
	if got := stdout.String(); !strings.Contains(got, "Proceed with uninstall? [y/N]: ") || !strings.Contains(got, "uninstall cancelled\n") {
		t.Fatalf("unexpected prompt output: %q", got)
	}
}

func TestRunEnvSetGetList(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout

	// set a new key
	if err := app.Run([]string{"env", "set", "MY_KEY=hello"}); err != nil {
		t.Fatalf("env set error = %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "set MY_KEY" {
		t.Fatalf("stdout = %q", got)
	}

	// get it back
	stdout.Reset()
	if err := app.Run([]string{"env", "get", "MY_KEY"}); err != nil {
		t.Fatalf("env get error = %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "hello" {
		t.Fatalf("stdout = %q", got)
	}

	// update the key
	stdout.Reset()
	if err := app.Run([]string{"env", "set", "MY_KEY=world"}); err != nil {
		t.Fatalf("env set update error = %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "updated MY_KEY" {
		t.Fatalf("stdout = %q", got)
	}

	// list shows the key
	stdout.Reset()
	if err := app.Run([]string{"env", "list"}); err != nil {
		t.Fatalf("env list error = %v", err)
	}
	if !strings.Contains(stdout.String(), "MY_KEY=world") {
		t.Fatalf("list output = %q", stdout.String())
	}

	// get missing key returns error
	if err := app.Run([]string{"env", "get", "DOES_NOT_EXIST"}); err == nil {
		t.Fatal("expected error for missing key")
	}

	// env file is valid JSON and contains the key
	data, err := os.ReadFile(app.store.EnvFilePath())
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	env, err := readEnvJSON(data)
	if err != nil {
		t.Fatalf("readEnvJSON() error = %v", err)
	}
	if env["MY_KEY"] != "world" {
		t.Fatalf("MY_KEY = %q", env["MY_KEY"])
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

func newTestApp(t *testing.T) *App {
	t.Helper()
	home := testHome(t)
	store := state.NewStore(state.Paths{
		Home:         filepath.Join(home, "managed"),
		AgentsDir:    filepath.Join(home, "LaunchAgents"),
		DaemonsDir:   filepath.Join(home, "LaunchDaemons"),
		NewsyslogDir: filepath.Join(home, "newsyslog.d"),
	})
	app := NewApp(strings.NewReader(""), ioDiscard{}, store, &fakeRunner{}, bootstrap.NewManager(store.Paths(), &fakeBootstrapInstaller{}))
	app.priv = &fakePrivilegedOperator{}
	if err := os.MkdirAll(store.Paths().Home, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.Paths().Home, ".installed"), []byte("installed\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return app
}

func newRawTestApp(t *testing.T) *App {
	t.Helper()
	home := testHome(t)
	store := state.NewStore(state.Paths{
		Home:         filepath.Join(home, "managed"),
		AgentsDir:    filepath.Join(home, "LaunchAgents"),
		DaemonsDir:   filepath.Join(home, "LaunchDaemons"),
		NewsyslogDir: filepath.Join(home, "newsyslog.d"),
	})
	app := NewApp(strings.NewReader(""), ioDiscard{}, store, &fakeRunner{}, bootstrap.NewManager(store.Paths(), &fakeBootstrapInstaller{}))
	app.priv = &fakePrivilegedOperator{}
	return app
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) {
	return len(p), nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func testHome(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

type fakeBootstrapInstaller struct {
	err       error
	sudoErr   error
	removeErr error
	installs  [][2]string
	sudoCalls [][2]string
}

func (f *fakeBootstrapInstaller) Install(src, dst string) error {
	f.installs = append(f.installs, [2]string{src, dst})
	return f.err
}

func (f *fakeBootstrapInstaller) InstallWithSudo(src, dst string) error {
	f.sudoCalls = append(f.sudoCalls, [2]string{src, dst})
	return f.sudoErr
}

func (f *fakeBootstrapInstaller) Remove(path string) error {
	return f.removeErr
}

func (f *fakeBootstrapInstaller) RemoveWithSudo(path string) error {
	f.sudoCalls = append(f.sudoCalls, [2]string{"rm", path})
	return f.sudoErr
}

type fakePrivilegedOperator struct {
	removed      []string
	created      []string
	installed    [][2]string
	bootstrapped []string
	bootout      []string
	err          error
}

func (f *fakePrivilegedOperator) InstallDaemonSpecWithSudo(dirs []string, runtimeSource string, runtimeDest string, plistSource string, plistDest string, metadataSource string, metadataDest string) error {
	f.created = append(f.created, dirs...)
	f.installed = append(f.installed,
		[2]string{runtimeSource, runtimeDest},
		[2]string{plistSource, plistDest},
		[2]string{metadataSource, metadataDest},
	)
	f.bootstrapped = append(f.bootstrapped, plistDest)
	return f.err
}

func (f *fakePrivilegedOperator) RemoveDaemonArtifactsWithSudo(plistPaths []string, cleanupPaths []string, extraPaths []string) error {
	f.bootout = append(f.bootout, plistPaths...)
	f.removed = append(f.removed, plistPaths...)
	f.removed = append(f.removed, cleanupPaths...)
	f.removed = append(f.removed, extraPaths...)
	for _, path := range append(append([]string{}, plistPaths...), cleanupPaths...) {
		if path == "" {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	for _, path := range extraPaths {
		if path == "" {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return f.err
}

func (f *fakePrivilegedOperator) RemovePathWithSudo(path string) error {
	f.removed = append(f.removed, path)
	return f.err
}

func (f *fakePrivilegedOperator) BootoutDaemonWithSudo(plistPath string) error {
	f.bootout = append(f.bootout, plistPath)
	return f.err
}

func (f *fakePrivilegedOperator) WriteFileWithSudo(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}
