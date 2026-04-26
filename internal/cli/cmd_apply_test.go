package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshgummersall/summond/internal/config"
	"github.com/joshgummersall/summond/internal/job"
)

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

	err = app.run([]string{"apply"})
	if err == nil || err.Error() != "apply requires install to be run first" {
		t.Fatalf("apply error = %v", err)
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

	if err := app.run([]string{"apply", configPath}); err != nil {
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

func TestApplyDryRunPrintsPlanWithoutApplying(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout
	runner := app.runner.(*fakeRunner)

	configPath := filepath.Join(testHome(t), "summond.toml")
	data := strings.Join([]string{
		"[jobs.cleanup]",
		`command = "/bin/echo"`,
		`target = "agent"`,
		`schedule = "daily"`,
		"hour = 3",
		"minute = 45",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := app.run([]string{"apply", "--dry-run", configPath}); err != nil {
		t.Fatalf("apply dry-run error = %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "apply will:\n") ||
		!strings.Contains(got, "- create agent job: cleanup (daily at 03:45)\n") ||
		!strings.Contains(got, "dry run: no changes made\n") {
		t.Fatalf("stdout = %q", got)
	}
	if len(runner.bootstrapped) != 0 || len(runner.bootedOut) != 0 {
		t.Fatalf("runner changed state: bootstrapped=%#v bootedOut=%#v", runner.bootstrapped, runner.bootedOut)
	}
	if _, err := app.store.Load("cleanup"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load(cleanup) error = %v, want not exist", err)
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

	if err := app.run([]string{"apply"}); err != nil {
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

	err := app.run([]string{"apply", configPath})
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
	if err := app.run([]string{"apply", configPath}); err != nil {
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

	err := app.run([]string{"apply", configPath})
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

	err := app.run([]string{"apply", configPath})
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
	runner.printErr = map[string]error{"cleanup": errors.New("Could not find service \"com.joshgummersall.summond.cleanup\" in domain")}

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

	err := app.run([]string{"apply", configPath})
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
	runner.printText = map[string]string{"cleanup": "com.joshgummersall.summond.cleanup\n/bin/echo\n"}

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

	err := app.run([]string{"apply", configPath})
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

	if err := app.run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	if got := stdout.String(); got != "applied 1 job(s)\n" {
		t.Fatalf("stdout = %q", got)
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
	if err := app.run([]string{"apply", initialConfigPath}); err != nil {
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

	if err := app.run([]string{"apply", updatedConfigPath}); err != nil {
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
	if err := app.run([]string{"apply", firstConfigPath}); err != nil {
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

	if err := app.run([]string{"apply", "--prune", pruneConfigPath}); err != nil {
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

func TestApplyDryRunWithPruneDoesNotRemoveOrphanedJobs(t *testing.T) {
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
	if err := app.run([]string{"apply", firstConfigPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	stdout.Reset()
	runner.bootedOut = nil
	runner.bootstrapped = nil

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

	if err := app.run([]string{"apply", "--dry-run", "--prune", pruneConfigPath}); err != nil {
		t.Fatalf("apply dry-run error = %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "- update agent job: cleanup (daily at 03:45)\n") ||
		!strings.Contains(got, "- prune managed job: sync (agent)\n") ||
		!strings.Contains(got, "dry run: no changes made\n") {
		t.Fatalf("stdout = %q", got)
	}
	if len(runner.bootedOut) != 0 || len(runner.bootstrapped) != 0 {
		t.Fatalf("runner changed state: bootedOut=%#v bootstrapped=%#v", runner.bootedOut, runner.bootstrapped)
	}
	if _, err := app.store.Load("sync"); err != nil {
		t.Fatalf("expected sync metadata to remain, err = %v", err)
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
	if err := app.run([]string{"apply", "--prune", configPath}); err != nil {
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

func TestApplyWithPruneReportsNoJobsToPrune(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout

	configPath := filepath.Join(testHome(t), "summond.toml")
	if err := os.WriteFile(configPath, []byte("[jobs.cleanup]\ncommand = \"/bin/echo\"\nschedule = \"daily\"\nhour = 3\nminute = 45\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := app.run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	stdout.Reset()
	if err := app.run([]string{"apply", "--prune", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	if got := stdout.String(); got != "applied 1 job(s)\n" {
		t.Fatalf("stdout = %q", got)
	}
}
