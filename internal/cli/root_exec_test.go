package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestTailRunsByAbsolutePath(t *testing.T) {
	cmd := tailCommand(context.Background(), "-n", "1", "/dev/null")
	if cmd.Args[0] != "/usr/bin/tail" || cmd.Path != "/usr/bin/tail" {
		t.Fatalf("tail Args[0], Path = %q, %q, want /usr/bin/tail", cmd.Args[0], cmd.Path)
	}
}

// installFakeBat puts a bat on PATH that records each run in a marker file
// and prints HIGHLIGHTED. It returns the marker path.
func installFakeBat(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	script := "#!/bin/sh\n/usr/bin/touch '" + marker + "'\necho HIGHLIGHTED\n"
	if err := os.WriteFile(filepath.Join(dir, "bat"), []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile(bat) error = %v", err)
	}
	t.Setenv("PATH", dir)
	return marker
}

func withTUIEUID(t *testing.T, euid int) {
	t.Helper()
	old := tuiGeteuid
	tuiGeteuid = func() int { return euid }
	t.Cleanup(func() { tuiGeteuid = old })
}

func TestBatRawSkipsBatAsRoot(t *testing.T) {
	marker := installFakeBat(t)
	withTUIEUID(t, 0)
	if got := batRaw("echo hi\n", "bash"); got != "echo hi\n" {
		t.Fatalf("batRaw() as root = %q, want plain text", got)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("bat ran as root, stat err = %v", err)
	}
}

func TestBatRawUsesBatForNonRoot(t *testing.T) {
	marker := installFakeBat(t)
	withTUIEUID(t, 501)
	if got := batRaw("echo hi\n", "bash"); got != "HIGHLIGHTED\n" {
		t.Fatalf("batRaw() = %q, want bat output", got)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("bat did not run: %v", err)
	}
}

func TestRefreshRuntimeTightensRuntimeDirsInDaemonScope(t *testing.T) {
	app := newDaemonTestApp(t)
	home := app.store.Paths().Home
	bin := filepath.Dir(app.store.RuntimeBinaryPath())
	for _, dir := range []string{home, bin} {
		if err := os.MkdirAll(dir, 0o777); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatalf("Chmod() error = %v", err)
		}
	}
	if err := app.run([]string{"refresh-runtime"}); err != nil {
		t.Fatalf("refresh-runtime error = %v", err)
	}
	for _, path := range []string{home, bin, app.store.RuntimeBinaryPath()} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat(%q) error = %v", path, err)
		}
		if info.Mode().Perm()&0o022 != 0 {
			t.Errorf("mode of %s = %o, writable by group or other", path, info.Mode().Perm())
		}
	}
}
