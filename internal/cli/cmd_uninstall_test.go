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
	if err := app.run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	stdout.Reset()
	app.stdin = strings.NewReader("y\n")
	if err := app.run([]string{"install", "--skip-newsyslog"}); err != nil {
		t.Fatalf("install error = %v", err)
	}
	stdout.Reset()

	app.stdin = strings.NewReader("y\n")
	if err := app.run([]string{"uninstall"}); err != nil {
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
		Home:      filepath.Join(home, "managed"),
		LaunchDir: filepath.Join(home, "LaunchAgents"),
	})
	daemonStore := state.NewStore(state.Paths{
		Home:      filepath.Join(home, "managed-daemon"),
		LaunchDir: filepath.Join(home, "LaunchDaemons"),
	})
	installer := &fakeBootstrapInstaller{}
	app := NewApp(strings.NewReader("y\ny\ny\n"), &bytes.Buffer{}, store, daemonStore, &fakeRunner{}, bootstrap.NewManager(state.PathSet{Agent: store.Paths(), Daemon: daemonStore.Paths(), NewsyslogDir: filepath.Join(home, "newsyslog.d")}, installer))
	app.priv = &fakePrivilegedOperator{}
	var stdout bytes.Buffer
	app.stdout = &stdout

	if err := app.run([]string{"install", "--skip-newsyslog"}); err != nil {
		t.Fatalf("install error = %v", err)
	}
	installer.removeErr = &bootstrap.PermissionError{Err: os.ErrPermission}
	stdout.Reset()
	app.stdin = strings.NewReader("y\ny\n")

	if err := app.run([]string{"uninstall"}); err != nil {
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
		Home:      filepath.Join(home, "managed"),
		LaunchDir: filepath.Join(home, "LaunchAgents"),
	})
	daemonStore := state.NewStore(state.Paths{
		Home:      filepath.Join(home, "managed-daemon"),
		LaunchDir: filepath.Join(home, "LaunchDaemons"),
	})
	installer := &fakeBootstrapInstaller{}
	app := NewApp(strings.NewReader("n\n"), &bytes.Buffer{}, store, daemonStore, &fakeRunner{}, bootstrap.NewManager(state.PathSet{Agent: store.Paths(), Daemon: daemonStore.Paths(), NewsyslogDir: filepath.Join(home, "newsyslog.d")}, installer))
	app.priv = &fakePrivilegedOperator{}
	var stdout bytes.Buffer
	app.stdout = &stdout

	app.stdin = strings.NewReader("y\n")
	if err := app.run([]string{"install", "--skip-newsyslog"}); err != nil {
		t.Fatalf("install error = %v", err)
	}
	installer.removeErr = &bootstrap.PermissionError{Err: os.ErrPermission}
	stdout.Reset()

	app.stdin = strings.NewReader("n\n")
	if err := app.run([]string{"uninstall"}); err != nil {
		t.Fatalf("uninstall error = %v", err)
	}
	if len(installer.sudoCalls) != 0 {
		t.Fatalf("sudoCalls = %#v", installer.sudoCalls)
	}
	if got := stdout.String(); !strings.Contains(got, "Proceed with uninstall? [y/N]: ") || !strings.Contains(got, "uninstall cancelled\n") {
		t.Fatalf("unexpected prompt output: %q", got)
	}
}
