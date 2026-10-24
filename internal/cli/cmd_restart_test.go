package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func applyLoginJob(t *testing.T, app *App) {
	t.Helper()
	configPath := filepath.Join(testHome(t), "summond.toml")
	data := strings.Join([]string{
		"[jobs.watcher]",
		`command = "/bin/echo"`,
		`args = ["watch"]`,
		`target = "agent"`,
		`schedule = "login"`,
	}, "\n")
	if err := os.WriteFile(configPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := app.run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}
}

func TestRestartKickstartsJob(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout
	applyLoginJob(t, app)

	if err := app.run([]string{"restart", "watcher"}); err != nil {
		t.Fatalf("restart error = %v", err)
	}
	runner := app.runner.(*fakeRunner)
	if !containsString(runner.kickstarted, "watcher") {
		t.Fatalf("kickstarted = %v", runner.kickstarted)
	}
	if !strings.Contains(stdout.String(), "restarted watcher") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRestartRecordsInterruptedRunAsKilled(t *testing.T) {
	app := newTestApp(t)
	applyLoginJob(t, app)
	if err := app.store.RecordExecutionStart("watcher", time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("RecordExecutionStart() error = %v", err)
	}
	runner := app.runner.(*fakeRunner)
	runner.printText = map[string]string{"watcher": "state = running\n"}

	if err := app.run([]string{"restart", "watcher"}); err != nil {
		t.Fatalf("restart error = %v", err)
	}
	managed, err := app.loadManagedSpec("watcher")
	if err != nil {
		t.Fatalf("loadManagedSpec() error = %v", err)
	}
	if managed.spec.LastFinishedAt == nil || managed.spec.LastError != "killed" {
		t.Fatalf("spec = %+v", managed.spec)
	}
	if !containsString(runner.kickstarted, "watcher") {
		t.Fatalf("kickstarted = %v", runner.kickstarted)
	}
}

func TestRestartFailsWhenJobIsNotLoaded(t *testing.T) {
	app := newTestApp(t)
	applyLoginJob(t, app)
	runner := app.runner.(*fakeRunner)
	runner.printCalls = nil
	runner.printErr = map[string]error{"watcher": errors.New("Could not find service")}

	err := app.run([]string{"restart", "watcher"})
	if err == nil || !strings.Contains(err.Error(), "not loaded") {
		t.Fatalf("restart error = %v", err)
	}
	if containsString(runner.kickstarted, "watcher") {
		t.Fatalf("kickstarted = %v", runner.kickstarted)
	}
}
