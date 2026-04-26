package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshgummersall/summond/internal/bootstrap"
	"github.com/joshgummersall/summond/internal/job"
	"github.com/joshgummersall/summond/internal/state"
)

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

	if err := app.run([]string{"remove", "cleanup"}); err != nil {
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

func TestRemoveDryRunPrintsPlanWithoutDeleting(t *testing.T) {
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

	if err := app.run([]string{"remove", "--dry-run", "cleanup"}); err != nil {
		t.Fatalf("remove dry-run error = %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "remove will:\n") ||
		!strings.Contains(got, "- boot out managed job: cleanup ") ||
		!strings.Contains(got, "dry run: no changes made\n") {
		t.Fatalf("stdout = %q", got)
	}
	if len(runner.bootedOut) != 0 {
		t.Fatalf("bootedOut = %#v", runner.bootedOut)
	}
	for _, path := range []string{
		app.store.JobDirForSpec(spec),
		spec.StdoutPath,
		app.store.MetadataPathForSpec(spec),
		spec.PlistPath,
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to remain, stat err = %v", path, err)
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

	if err := app.run([]string{"remove", "cleanup"}); err != nil {
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

	if err := app.run([]string{"remove", "cleanup"}); err != nil {
		t.Fatalf("remove error = %v", err)
	}
	stdout.Reset()

	if err := app.run([]string{"list"}); err != nil {
		t.Fatalf("list error = %v", err)
	}
	if got := stdout.String(); got != "no managed jobs\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestApplyPrunePromptCancelsWithoutFlag(t *testing.T) {
	home := testHome(t)
	store := state.NewStore(state.Paths{
		Home:      filepath.Join(home, "managed"),
		LaunchDir: filepath.Join(home, "LaunchAgents"),
	})
	daemonStore := state.NewStore(state.Paths{
		Home:      filepath.Join(home, "managed-daemon"),
		LaunchDir: filepath.Join(home, "LaunchDaemons"),
	})
	runner := &fakeRunner{}
	app := NewApp(strings.NewReader("n\n"), &bytes.Buffer{}, store, daemonStore, runner, bootstrap.NewManager(state.PathSet{Agent: store.Paths(), Daemon: daemonStore.Paths(), NewsyslogDir: filepath.Join(home, "newsyslog.d")}, &fakeBootstrapInstaller{}))
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
	if err := app.run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	stale := job.Spec{Group: "tests", Name: "stale", Command: "/bin/echo", Schedule: job.Schedule{Kind: "hourly"}, Target: job.TargetAgent}
	if _, err := store.Install(stale); err != nil {
		t.Fatalf("Install(stale) error = %v", err)
	}

	stdout.Reset()
	runner.bootedOut = nil

	app.stdin = strings.NewReader("n\n")
	if err := app.run([]string{"apply", configPath}); err != nil {
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
