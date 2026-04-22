package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshgummersall/summond/internal/bootstrap"
	"github.com/joshgummersall/summond/internal/job"
	"github.com/joshgummersall/summond/internal/state"
)

type fakeRunner struct {
	bootstrapped []string
	bootedOut    []string
	kickstarted  []string
	stopped      []string
}

func (f *fakeRunner) Bootstrap(spec job.Spec) error {
	f.bootstrapped = append(f.bootstrapped, spec.Name)
	return nil
}

func (f *fakeRunner) Bootout(spec job.Spec) error {
	f.bootedOut = append(f.bootedOut, spec.Name)
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
	return spec.Label, nil
}

func TestRunVersion(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout

	if err := app.Run([]string{"version"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := stdout.String(), "summond 0.3.0\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestAddAndList(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout
	runner := app.runner.(*fakeRunner)

	err := app.Run([]string{
		"add", "backup",
		"--command", "/bin/echo",
		"--schedule", "hourly",
		"--minute", "15",
		"hello",
		"world",
	})
	if err != nil {
		t.Fatalf("add error = %v", err)
	}
	if len(runner.bootstrapped) != 1 || runner.bootstrapped[0] != "backup" {
		t.Fatalf("bootstrapped = %#v", runner.bootstrapped)
	}

	stdout.Reset()
	if err := app.Run([]string{"list"}); err != nil {
		t.Fatalf("list error = %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "backup\tagent\thourly\tenabled=true") {
		t.Fatalf("unexpected list output: %q", got)
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

	if err := app.Run([]string{"apply", "-f", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	if len(runner.bootstrapped) != 1 || runner.bootstrapped[0] != "cleanup" {
		t.Fatalf("bootstrapped = %#v", runner.bootstrapped)
	}
	if got := stdout.String(); got != "applied 1 job(s)\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestEnableDisable(t *testing.T) {
	app := newTestApp(t)
	runner := app.runner.(*fakeRunner)
	if err := app.Run([]string{
		"add", "archive",
		"--command", "/bin/echo",
		"--schedule", "daily",
		"--hour", "1",
		"--minute", "5",
	}); err != nil {
		t.Fatalf("add error = %v", err)
	}
	if err := app.Run([]string{"disable", "archive"}); err != nil {
		t.Fatalf("disable error = %v", err)
	}
	if err := app.Run([]string{"enable", "archive"}); err != nil {
		t.Fatalf("enable error = %v", err)
	}
	if len(runner.bootedOut) == 0 || runner.bootedOut[len(runner.bootedOut)-1] != "archive" {
		t.Fatalf("bootedOut = %#v", runner.bootedOut)
	}
	if len(runner.bootstrapped) < 2 || runner.bootstrapped[len(runner.bootstrapped)-1] != "archive" {
		t.Fatalf("bootstrapped = %#v", runner.bootstrapped)
	}
}

func TestInitCreatesStarterFiles(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout

	if err := app.Run([]string{"init", "--install-newsyslog=false"}); err != nil {
		t.Fatalf("init error = %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "config: ") || !strings.Contains(got, "newsyslog source: ") {
		t.Fatalf("unexpected output: %q", got)
	}
}

func TestInitPermissionDeniedCanUseSudoRetry(t *testing.T) {
	home := testHome(t)
	store := state.NewStore(state.Paths{
		Home:         filepath.Join(home, "managed"),
		ConfigDir:    filepath.Join(home, "config"),
		AgentsDir:    filepath.Join(home, "LaunchAgents"),
		DaemonsDir:   filepath.Join(home, "LaunchDaemons"),
		NewsyslogDir: filepath.Join(home, "newsyslog.d"),
	})
	installer := &fakeBootstrapInstaller{err: &bootstrap.PermissionError{Err: os.ErrPermission}}
	app := NewApp(strings.NewReader("y\n"), &bytes.Buffer{}, store, &fakeRunner{}, bootstrap.NewManager(store.Paths(), installer))
	var stdout bytes.Buffer
	app.stdout = &stdout

	if err := app.Run([]string{"init"}); err != nil {
		t.Fatalf("init error = %v", err)
	}
	if len(installer.sudoCalls) != 1 {
		t.Fatalf("sudoCalls = %#v", installer.sudoCalls)
	}
	if got := stdout.String(); !strings.Contains(got, "installed via sudo") {
		t.Fatalf("unexpected output: %q", got)
	}
}

func TestInitPermissionDeniedWithoutPromptPrintsManualCommand(t *testing.T) {
	home := testHome(t)
	store := state.NewStore(state.Paths{
		Home:         filepath.Join(home, "managed"),
		ConfigDir:    filepath.Join(home, "config"),
		AgentsDir:    filepath.Join(home, "LaunchAgents"),
		DaemonsDir:   filepath.Join(home, "LaunchDaemons"),
		NewsyslogDir: filepath.Join(home, "newsyslog.d"),
	})
	installer := &fakeBootstrapInstaller{err: &bootstrap.PermissionError{Err: os.ErrPermission}}
	app := NewApp(strings.NewReader(""), &bytes.Buffer{}, store, &fakeRunner{}, bootstrap.NewManager(store.Paths(), installer))
	var stdout bytes.Buffer
	app.stdout = &stdout

	if err := app.Run([]string{"init", "--no-prompt"}); err != nil {
		t.Fatalf("init error = %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "install manually with: sudo install -m 0644") {
		t.Fatalf("unexpected output: %q", got)
	}
}

func newTestApp(t *testing.T) *App {
	t.Helper()
	home := testHome(t)
	store := state.NewStore(state.Paths{
		Home:         filepath.Join(home, "managed"),
		ConfigDir:    filepath.Join(home, "config"),
		AgentsDir:    filepath.Join(home, "LaunchAgents"),
		DaemonsDir:   filepath.Join(home, "LaunchDaemons"),
		NewsyslogDir: filepath.Join(home, "newsyslog.d"),
	})
	return NewApp(strings.NewReader(""), ioDiscard{}, store, &fakeRunner{}, bootstrap.NewManager(store.Paths(), &fakeBootstrapInstaller{}))
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) {
	return len(p), nil
}

func testHome(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

type fakeBootstrapInstaller struct {
	err       error
	sudoErr   error
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
