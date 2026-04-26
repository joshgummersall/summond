package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	if err := app.run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	stdout.Reset()
	if err := app.run([]string{"state", "watcher"}); err != nil {
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
	if err := app.run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	stdout.Reset()
	if err := app.run([]string{"state", "scripted"}); err != nil {
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
	if err := app.run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	stdout.Reset()
	if err := app.run([]string{"state", "cleanup"}); err != nil {
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
	if err := app.run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	stdout.Reset()
	if err := app.run([]string{"exec", "cleanup"}); err != nil {
		t.Fatalf("exec error = %v", err)
	}

	stdout.Reset()
	if err := app.run([]string{"state", "cleanup"}); err != nil {
		t.Fatalf("state error = %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, `"run_count": 1`) || !strings.Contains(got, `"success_count": 1`) || !strings.Contains(got, `"recent_runs": [`) || !strings.Contains(got, `"exit_code": 0`) {
		t.Fatalf("stdout = %q", got)
	}
}
