package cli

import (
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
	bootstrapErr map[string]error
	bootoutErr   map[string]error
	printErr     map[string]error
	printText    map[string]string
	printCalls   map[string]int
}

func (f *fakeRunner) Bootstrap(spec job.Spec) error {
	f.bootstrapped = append(f.bootstrapped, spec.Name)
	if f.bootstrapErr != nil {
		if err, ok := f.bootstrapErr[spec.Name]; ok {
			return err
		}
	}
	return nil
}

func (f *fakeRunner) Bootout(spec job.Spec) error {
	f.bootedOut = append(f.bootedOut, spec.Name)
	if f.bootoutErr != nil {
		if err, ok := f.bootoutErr[spec.Name]; ok {
			return err
		}
	}
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
	if f.printCalls == nil {
		f.printCalls = map[string]int{}
	}
	f.printCalls[spec.Name]++
	if f.printErr != nil {
		if err, ok := f.printErr[spec.Name]; ok && f.printCalls[spec.Name] == 1 {
			return "", err
		}
	}
	if f.printText != nil {
		if text, ok := f.printText[spec.Name]; ok {
			return text, nil
		}
	}
	var b strings.Builder
	b.WriteString(spec.Label)
	b.WriteString("\n")
	b.WriteString(spec.StdoutPath)
	b.WriteString("\n")
	b.WriteString(spec.StderrPath)
	b.WriteString("\n")
	for _, path := range spec.WatchPaths {
		b.WriteString(path)
		b.WriteString("\n")
	}
	b.WriteString(spec.RuntimeBinaryPath)
	b.WriteString("\n")
	b.WriteString("exec\n")
	b.WriteString(spec.Name)
	b.WriteString("\n")
	return b.String(), nil
}

type fakeBootstrapInstaller struct {
	err       error
	sudoErr   error
	removeErr error
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

func (f *fakeBootstrapInstaller) Remove(path string) error {
	return f.removeErr
}

func (f *fakeBootstrapInstaller) RemoveWithSudo(path string) error {
	f.sudoCalls = append(f.sudoCalls, [2]string{"rm", path})
	return f.sudoErr
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) {
	return len(p), nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func testHome(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func newTestApp(t *testing.T) *App {
	t.Helper()
	return newTestAppAt(t, testHome(t))
}

// newDaemonTestApp returns an app in daemon scope, as if run under sudo.
func newDaemonTestApp(t *testing.T) *App {
	t.Helper()
	app := newTestApp(t)
	app.geteuid = func() int { return 0 }
	return app
}

func newTestAppAt(t *testing.T, home string) *App {
	t.Helper()
	app := newRawTestAppAt(t, home)
	if err := os.MkdirAll(app.store.Paths().Home, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(app.store.Paths().Home, ".installed"), []byte("installed\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return app
}

func newRawTestApp(t *testing.T) *App {
	t.Helper()
	return newRawTestAppAt(t, testHome(t))
}

// newRawTestAppAt returns an app in agent scope rooted at home, without the
// install marker. Tests set geteuid to 0 for daemon scope.
func newRawTestAppAt(t *testing.T, home string) *App {
	t.Helper()
	store := state.NewStore(state.Paths{
		Home:      filepath.Join(home, "managed"),
		LaunchDir: filepath.Join(home, "LaunchAgents"),
	})
	app := NewApp(strings.NewReader(""), ioDiscard{}, store, &fakeRunner{}, bootstrap.NewManager(store.Paths().Home, filepath.Join(home, "newsyslog.d"), &fakeBootstrapInstaller{}))
	app.geteuid = func() int { return 501 }
	return app
}
