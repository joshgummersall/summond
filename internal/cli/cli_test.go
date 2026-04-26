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

func (f *fakeBootstrapInstaller) MkdirAllWithSudo(path string) error {
	return f.sudoErr
}

func (f *fakeBootstrapInstaller) Remove(path string) error {
	return f.removeErr
}

func (f *fakeBootstrapInstaller) RemoveWithSudo(path string) error {
	f.sudoCalls = append(f.sudoCalls, [2]string{"rm", path})
	return f.sudoErr
}

type fakePrivilegedOperator struct {
	removed      []string
	created      []string
	installed    [][2]string
	bootstrapped []string
	bootout      []string
	err          error
}

func (f *fakePrivilegedOperator) InstallDaemonSpecWithSudo(dirs []string, runtimeSource string, runtimeDest string, plistSource string, plistDest string, metadataSource string, metadataDest string) error {
	f.created = append(f.created, dirs...)
	f.installed = append(f.installed,
		[2]string{runtimeSource, runtimeDest},
		[2]string{plistSource, plistDest},
		[2]string{metadataSource, metadataDest},
	)
	f.bootstrapped = append(f.bootstrapped, plistDest)
	return f.err
}

func (f *fakePrivilegedOperator) RemoveDaemonArtifactsWithSudo(plistPaths []string, cleanupPaths []string, extraPaths []string) error {
	f.bootout = append(f.bootout, plistPaths...)
	f.removed = append(f.removed, plistPaths...)
	f.removed = append(f.removed, cleanupPaths...)
	f.removed = append(f.removed, extraPaths...)
	for _, path := range append(append([]string{}, plistPaths...), cleanupPaths...) {
		if path == "" {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	for _, path := range extraPaths {
		if path == "" {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return f.err
}

func (f *fakePrivilegedOperator) RemovePathWithSudo(path string) error {
	f.removed = append(f.removed, path)
	return f.err
}

func (f *fakePrivilegedOperator) BootoutDaemonWithSudo(plistPath string) error {
	f.bootout = append(f.bootout, plistPath)
	return f.err
}

func (f *fakePrivilegedOperator) WriteFileWithSudo(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
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
	home := testHome(t)
	agentStore := state.NewStore(state.Paths{
		Home:      filepath.Join(home, "managed"),
		LaunchDir: filepath.Join(home, "LaunchAgents"),
	})
	daemonStore := state.NewStore(state.Paths{
		Home:      filepath.Join(home, "managed-daemon"),
		LaunchDir: filepath.Join(home, "LaunchDaemons"),
	})
	app := NewApp(strings.NewReader(""), ioDiscard{}, agentStore, daemonStore, &fakeRunner{}, bootstrap.NewManager(state.PathSet{Agent: agentStore.Paths(), Daemon: daemonStore.Paths(), NewsyslogDir: filepath.Join(home, "newsyslog.d")}, &fakeBootstrapInstaller{}))
	app.priv = &fakePrivilegedOperator{}
	if err := os.MkdirAll(agentStore.Paths().Home, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentStore.Paths().Home, ".installed"), []byte("installed\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return app
}

func newRawTestApp(t *testing.T) *App {
	t.Helper()
	home := testHome(t)
	agentStore := state.NewStore(state.Paths{
		Home:      filepath.Join(home, "managed"),
		LaunchDir: filepath.Join(home, "LaunchAgents"),
	})
	daemonStore := state.NewStore(state.Paths{
		Home:      filepath.Join(home, "managed-daemon"),
		LaunchDir: filepath.Join(home, "LaunchDaemons"),
	})
	app := NewApp(strings.NewReader(""), ioDiscard{}, agentStore, daemonStore, &fakeRunner{}, bootstrap.NewManager(state.PathSet{Agent: agentStore.Paths(), Daemon: daemonStore.Paths(), NewsyslogDir: filepath.Join(home, "newsyslog.d")}, &fakeBootstrapInstaller{}))
	app.priv = &fakePrivilegedOperator{}
	return app
}

