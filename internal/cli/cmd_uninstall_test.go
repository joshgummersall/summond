package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	app := NewApp(strings.NewReader("n\n"), &bytes.Buffer{}, store, &fakeRunner{})
	app.geteuid = func() int { return 501 }
	var stdout bytes.Buffer
	app.stdout = &stdout
	if err := os.MkdirAll(store.Paths().Home, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	app.stdin = strings.NewReader("n\n")
	if err := app.run([]string{"uninstall"}); err != nil {
		t.Fatalf("uninstall error = %v", err)
	}
	if _, err := os.Stat(store.Paths().Home); err != nil {
		t.Fatalf("state home removed after cancel: %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "Proceed with uninstall? [y/N]: ") || !strings.Contains(got, "uninstall cancelled\n") {
		t.Fatalf("unexpected prompt output: %q", got)
	}
}
