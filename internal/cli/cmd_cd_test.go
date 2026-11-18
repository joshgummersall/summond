package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshgummersall/summond/internal/job"
)

// evalInShell runs `eval "$cmd"; pwd` in /bin/sh and returns the directory
// the shell ends up in.
func evalInShell(t *testing.T, cmd string) string {
	t.Helper()
	out, err := exec.Command("/bin/sh", "-c", `eval "$1" && pwd -P`, "sh", cmd).CombinedOutput()
	if err != nil {
		t.Fatalf("eval %q: %v: %s", cmd, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestCDCommandSurvivesShellEval(t *testing.T) {
	for _, name := range []string{
		"Application Support",
		"it's here",
		`$HOME; echo "x" * \`,
		"-dash",
	} {
		dir := filepath.Join(t.TempDir(), name)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("Mkdir(%q) error = %v", dir, err)
		}
		want, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatalf("EvalSymlinks() error = %v", err)
		}
		if got := evalInShell(t, cdCommand(dir)); got != want {
			t.Fatalf("eval %q: pwd = %q, want %q", cdCommand(dir), got, want)
		}
	}
}

func TestCDPrintsQuotedCommandWhenNotATTY(t *testing.T) {
	if info, err := os.Stdout.Stat(); err != nil || info.Mode()&os.ModeCharDevice != 0 {
		t.Skip("stdout is a terminal; summond cd would spawn a shell")
	}
	home := filepath.Join(t.TempDir(), "Application Support")
	app := newTestAppAt(t, home)
	var stdout bytes.Buffer
	app.stdout = &stdout

	spec, err := app.store.Install(job.Spec{
		Name:     "my-job",
		Command:  "/bin/echo",
		Schedule: job.Schedule{Kind: job.ScheduleDaily, Hour: 3, HourSet: true, Minute: 45, MinuteSet: true},
	})
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}

	if err := app.run([]string{"cd", "my-job"}); err != nil {
		t.Fatalf("cd error = %v", err)
	}
	want, err := filepath.EvalSymlinks(app.store.JobDirForSpec(spec))
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}
	if got := evalInShell(t, stdout.String()); got != want {
		t.Fatalf("eval %q: pwd = %q, want %q", stdout.String(), got, want)
	}
}
