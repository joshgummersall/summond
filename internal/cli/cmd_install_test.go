package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshgummersall/summond/internal/bootstrap"
	"github.com/joshgummersall/summond/internal/state"
)

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

	if err := app.run([]string{"install", "--skip-newsyslog"}); err != nil {
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
		Home:      filepath.Join(home, "managed"),
		LaunchDir: filepath.Join(home, "LaunchAgents"),
	})
	daemonStore := state.NewStore(state.Paths{
		Home:      filepath.Join(home, "managed-daemon"),
		LaunchDir: filepath.Join(home, "LaunchDaemons"),
	})
	installer := &fakeBootstrapInstaller{err: &bootstrap.PermissionError{Err: os.ErrPermission}}
	app := NewApp(strings.NewReader("y\ny\ny\n"), &bytes.Buffer{}, store, daemonStore, &fakeRunner{}, bootstrap.NewManager(state.PathSet{Agent: store.Paths(), Daemon: daemonStore.Paths(), NewsyslogDir: filepath.Join(home, "newsyslog.d")}, installer))
	var stdout bytes.Buffer
	app.stdout = &stdout

	if err := app.run([]string{"install"}); err != nil {
		t.Fatalf("install error = %v", err)
	}
	if len(installer.sudoCalls) != 1 {
		t.Fatalf("sudoCalls = %#v", installer.sudoCalls)
	}
	if got := stdout.String(); !strings.Contains(got, "install will:\n") || !strings.Contains(got, "Proceed with install? [y/N]: ") || !strings.Contains(got, "system setup requires sudo") {
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
		Home:      filepath.Join(home, "managed"),
		LaunchDir: filepath.Join(home, "LaunchAgents"),
	})
	daemonStore := state.NewStore(state.Paths{
		Home:      filepath.Join(home, "managed-daemon"),
		LaunchDir: filepath.Join(home, "LaunchDaemons"),
	})
	installer := &fakeBootstrapInstaller{err: &bootstrap.PermissionError{Err: os.ErrPermission}}
	app := NewApp(strings.NewReader("n\n"), &bytes.Buffer{}, store, daemonStore, &fakeRunner{}, bootstrap.NewManager(state.PathSet{Agent: store.Paths(), Daemon: daemonStore.Paths(), NewsyslogDir: filepath.Join(home, "newsyslog.d")}, installer))
	var stdout bytes.Buffer
	app.stdout = &stdout

	if err := app.run([]string{"install"}); err != nil {
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
		Home:      filepath.Join(home, "managed"),
		LaunchDir: filepath.Join(home, "LaunchAgents"),
	})
	daemonStore := state.NewStore(state.Paths{
		Home:      filepath.Join(home, "managed-daemon"),
		LaunchDir: filepath.Join(home, "LaunchDaemons"),
	})
	installer := &fakeBootstrapInstaller{err: &bootstrap.PermissionError{Err: os.ErrPermission}}
	app := NewApp(strings.NewReader("y\n"), &bytes.Buffer{}, store, daemonStore, &fakeRunner{}, bootstrap.NewManager(state.PathSet{Agent: store.Paths(), Daemon: daemonStore.Paths(), NewsyslogDir: filepath.Join(home, "newsyslog.d")}, installer))
	var stdout bytes.Buffer
	app.stdout = &stdout

	if err := app.run([]string{"install", "--skip-newsyslog"}); err != nil {
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

	if err := app.run([]string{"install", "--skip-newsyslog"}); err != nil {
		t.Fatalf("install error = %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "- leave unchanged starter config: ") {
		t.Fatalf("unexpected output: %q", got)
	}
}
