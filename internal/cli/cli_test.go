package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshgummersall/summond/internal/bootstrap"
	"github.com/joshgummersall/summond/internal/config"
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
	if f.printErr != nil {
		if err, ok := f.printErr[spec.Name]; ok {
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

func TestRunVersion(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout

	if err := app.Run([]string{"version"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := stdout.String(), "summond 0.4.0\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
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
	if len(runner.bootedOut) != 1 || runner.bootedOut[0] != "cleanup" {
		t.Fatalf("bootedOut = %#v", runner.bootedOut)
	}
	if len(runner.bootstrapped) != 1 || runner.bootstrapped[0] != "cleanup" {
		t.Fatalf("bootstrapped = %#v", runner.bootstrapped)
	}
	if got := stdout.String(); got != "applied 1 job(s)\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestApplyReportsBootstrapWarningsButSucceeds(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout
	runner := app.runner.(*fakeRunner)
	runner.bootstrapErr = map[string]error{"cleanup": errors.New("launchctl bootstrap failed")}

	configPath := filepath.Join(testHome(t), "summond.toml")
	data := strings.Join([]string{
		"[jobs.cleanup]",
		`command = "/bin/echo"`,
		`args = ["clean"]`,
		`target = "agent"`,
		`schedule = "daily"`,
		"hour = 3",
		"minute = 45",
		"enabled = true",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := app.Run([]string{"apply", "-f", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	if len(runner.bootedOut) != 1 || runner.bootedOut[0] != "cleanup" {
		t.Fatalf("bootedOut = %#v", runner.bootedOut)
	}
	if len(runner.bootstrapped) != 1 || runner.bootstrapped[0] != "cleanup" {
		t.Fatalf("bootstrapped = %#v", runner.bootstrapped)
	}
	if got := stdout.String(); !strings.Contains(got, "applied 1 job(s)\n") || !strings.Contains(got, "warning: cleanup: bootstrap failed: launchctl bootstrap failed\n") {
		t.Fatalf("stdout = %q", got)
	}
	if _, err := app.store.Load("cleanup"); err != nil {
		t.Fatalf("Load(cleanup) error = %v", err)
	}
}

func TestApplySkipsBootoutWhenServiceIsMissing(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout
	runner := app.runner.(*fakeRunner)
	runner.printErr = map[string]error{"cleanup": errors.New("Could not find service \"com.joshgummersall.summond.cleanup\" in domain")}

	configPath := filepath.Join(testHome(t), "summond.toml")
	data := strings.Join([]string{
		"[jobs.cleanup]",
		`command = "/bin/echo"`,
		`args = ["clean"]`,
		`target = "agent"`,
		`schedule = "daily"`,
		"hour = 3",
		"minute = 45",
		"enabled = true",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := app.Run([]string{"apply", "-f", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	if len(runner.bootedOut) != 0 {
		t.Fatalf("bootedOut = %#v", runner.bootedOut)
	}
	if len(runner.bootstrapped) != 1 || runner.bootstrapped[0] != "cleanup" {
		t.Fatalf("bootstrapped = %#v", runner.bootstrapped)
	}
	if got := stdout.String(); strings.Contains(got, "bootout before bootstrap failed") || strings.Contains(got, "load-state check failed") {
		t.Fatalf("stdout = %q", got)
	}
}

func TestApplyReportsBootoutWarningsButSucceeds(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout
	runner := app.runner.(*fakeRunner)
	runner.bootoutErr = map[string]error{"cleanup": errors.New("launchctl bootout failed")}

	configPath := filepath.Join(testHome(t), "summond.toml")
	data := strings.Join([]string{
		"[jobs.cleanup]",
		`command = "/bin/echo"`,
		`args = ["clean"]`,
		`target = "agent"`,
		`schedule = "daily"`,
		"hour = 3",
		"minute = 45",
		"enabled = false",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := app.Run([]string{"apply", "-f", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	if len(runner.bootedOut) != 1 || runner.bootedOut[0] != "cleanup" {
		t.Fatalf("bootedOut = %#v", runner.bootedOut)
	}
	if got := stdout.String(); !strings.Contains(got, "applied 1 job(s)\n") || !strings.Contains(got, "warning: cleanup: bootout failed: launchctl bootout failed\n") {
		t.Fatalf("stdout = %q", got)
	}
	if _, err := app.store.Load("cleanup"); err != nil {
		t.Fatalf("Load(cleanup) error = %v", err)
	}
}

func TestApplyReportsVerificationWarningsButSucceeds(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout
	runner := app.runner.(*fakeRunner)
	runner.printText = map[string]string{"cleanup": "com.joshgummersall.summond.cleanup\n/bin/echo\n"}

	configPath := filepath.Join(testHome(t), "summond.toml")
	data := strings.Join([]string{
		"[jobs.cleanup]",
		`command = "/bin/echo"`,
		`args = ["clean"]`,
		`target = "agent"`,
		`schedule = "daily"`,
		"hour = 3",
		"minute = 45",
		"enabled = true",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := app.Run([]string{"apply", "-f", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "warning: cleanup: loaded job verification failed:") {
		t.Fatalf("stdout = %q", got)
	}
}

func TestApplyUsesChecksumForVerificationWhenAvailable(t *testing.T) {
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
		"enabled = true",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	specs, err := config.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	installed, err := app.store.Install(specs[0])
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	runner.printText = map[string]string{"cleanup": "loaded checksum " + installed.Checksum}
	stdout.Reset()

	if err := app.Run([]string{"apply", "-f", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	if got := stdout.String(); strings.Contains(got, "warning: cleanup: loaded job verification failed:") {
		t.Fatalf("stdout = %q", got)
	}
}

func TestInspectOnChangeJobShowsWatchPaths(t *testing.T) {
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
		"enabled = true",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := app.Run([]string{"apply", "-f", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	stdout.Reset()
	if err := app.Run([]string{"inspect", "watcher"}); err != nil {
		t.Fatalf("inspect error = %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "trigger: on_change") || !strings.Contains(got, "watch_paths: /tmp/watch.txt") {
		t.Fatalf("stdout = %q", got)
	}
}

func TestInspectShowsShellScriptOnNewLine(t *testing.T) {
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
		"enabled = true",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := app.Run([]string{"apply", "-f", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	stdout.Reset()
	if err := app.Run([]string{"inspect", "scripted"}); err != nil {
		t.Fatalf("inspect error = %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "shell:\n  echo hello\n  echo world\n") {
		t.Fatalf("stdout = %q", got)
	}
}

func TestListOutputsTSV(t *testing.T) {
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
		"enabled = true",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := app.Run([]string{"apply", "-f", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	stdout.Reset()
	if err := app.Run([]string{"list"}); err != nil {
		t.Fatalf("list error = %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "NAME") || !strings.Contains(got, "TARGET") || !strings.Contains(got, "SCHEDULE") || !strings.Contains(got, "ENABLED") || !strings.Contains(got, "STATUS") {
		t.Fatalf("stdout missing headers: %q", got)
	}
	if !strings.Contains(got, "cleanup") || !strings.Contains(got, "agent") || !strings.Contains(got, "daily at 03:45") || !strings.Contains(got, "true") || !strings.Contains(got, "never") {
		t.Fatalf("stdout = %q", got)
	}
	if strings.Contains(got, "\t") {
		t.Fatalf("stdout still contains raw tabs: %q", got)
	}
}

func TestExecRecordsSuccessfulRun(t *testing.T) {
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
		"enabled = true",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := app.Run([]string{"apply", "-f", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	stdout.Reset()
	if err := app.Run([]string{"exec", "cleanup"}); err != nil {
		t.Fatalf("exec error = %v", err)
	}
	if got := stdout.String(); got != "clean\n" {
		t.Fatalf("stdout = %q", got)
	}
	spec, err := app.store.Load("cleanup")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if spec.RunCount != 1 || spec.SuccessCount != 1 || spec.FailureCount != 0 {
		t.Fatalf("unexpected counters: %+v", spec)
	}
	if spec.LastExitCode == nil || *spec.LastExitCode != 0 {
		t.Fatalf("LastExitCode = %v", spec.LastExitCode)
	}
}

func TestExecRecordsFailingRun(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout

	configPath := filepath.Join(testHome(t), "summond.toml")
	data := strings.Join([]string{
		"[jobs.failer]",
		`command = "/bin/sh"`,
		`args = ["-c", "exit 7"]`,
		`target = "agent"`,
		`schedule = "daily"`,
		"hour = 3",
		"minute = 45",
		"enabled = true",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := app.Run([]string{"apply", "-f", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	err := app.Run([]string{"exec", "failer"})
	var exitErr ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 7 {
		t.Fatalf("exec error = %#v", err)
	}
	spec, loadErr := app.store.Load("failer")
	if loadErr != nil {
		t.Fatalf("Load() error = %v", loadErr)
	}
	if spec.RunCount != 1 || spec.SuccessCount != 0 || spec.FailureCount != 1 {
		t.Fatalf("unexpected counters: %+v", spec)
	}
	if spec.LastExitCode == nil || *spec.LastExitCode != 7 {
		t.Fatalf("LastExitCode = %v", spec.LastExitCode)
	}
	if spec.LastError == "" {
		t.Fatal("expected LastError")
	}
}

func TestInstallCreatesStarterFiles(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout

	if err := app.Run([]string{"install", "--install-newsyslog=false"}); err != nil {
		t.Fatalf("install error = %v", err)
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("unexpected output: %q", got)
	}
}

func TestInstallPermissionDeniedCanUseSudoRetry(t *testing.T) {
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

	if err := app.Run([]string{"install"}); err != nil {
		t.Fatalf("install error = %v", err)
	}
	if len(installer.sudoCalls) != 1 {
		t.Fatalf("sudoCalls = %#v", installer.sudoCalls)
	}
	if got := stdout.String(); !strings.Contains(got, "newsyslog install requires sudo. Retry with sudo? [Y/n]: ") {
		t.Fatalf("unexpected output: %q", got)
	}
}

func TestInstallPermissionDeniedWithoutPromptPrintsManualCommand(t *testing.T) {
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

	if err := app.Run([]string{"install", "--no-prompt"}); err != nil {
		t.Fatalf("install error = %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "install manually with: sudo install -m 0644") {
		t.Fatalf("unexpected output: %q", got)
	}
}

func TestUninstallRemovesManagedArtifacts(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout

	configPath := filepath.Join(testHome(t), "summond.toml")
	data := strings.Join([]string{
		"[jobs.cleanup]",
		`command = "/bin/echo"`,
		`args = ["cleanup"]`,
		`target = "agent"`,
		`schedule = "hourly"`,
		"minute = 5",
		"enabled = true",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := app.Run([]string{"apply", "-f", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	stdout.Reset()
	if err := app.Run([]string{"install", "--install-newsyslog=false"}); err != nil {
		t.Fatalf("install error = %v", err)
	}
	stdout.Reset()

	if err := app.Run([]string{"uninstall", "--yes"}); err != nil {
		t.Fatalf("uninstall error = %v", err)
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("unexpected output: %q", got)
	}
	if _, err := os.Stat(app.store.Paths().Home); !os.IsNotExist(err) {
		t.Fatalf("expected managed state to be removed, stat err = %v", err)
	}
}

func TestUninstallPermissionDeniedPrintsManualCleanup(t *testing.T) {
	home := testHome(t)
	store := state.NewStore(state.Paths{
		Home:         filepath.Join(home, "managed"),
		ConfigDir:    filepath.Join(home, "config"),
		AgentsDir:    filepath.Join(home, "LaunchAgents"),
		DaemonsDir:   filepath.Join(home, "LaunchDaemons"),
		NewsyslogDir: filepath.Join(home, "newsyslog.d"),
	})
	installer := &fakeBootstrapInstaller{}
	app := NewApp(strings.NewReader(""), &bytes.Buffer{}, store, &fakeRunner{}, bootstrap.NewManager(store.Paths(), installer))
	app.priv = &fakePrivilegedOperator{}
	var stdout bytes.Buffer
	app.stdout = &stdout

	if err := app.Run([]string{"install", "--install-newsyslog=false"}); err != nil {
		t.Fatalf("install error = %v", err)
	}
	installer.removeErr = &bootstrap.PermissionError{Err: os.ErrPermission}
	stdout.Reset()

	if err := app.Run([]string{"uninstall", "--yes", "--no-prompt"}); err != nil {
		t.Fatalf("uninstall error = %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "manual cleanup: sudo rm -f") {
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
	app := NewApp(strings.NewReader(""), ioDiscard{}, store, &fakeRunner{}, bootstrap.NewManager(store.Paths(), &fakeBootstrapInstaller{}))
	app.priv = &fakePrivilegedOperator{}
	return app
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

type fakePrivilegedOperator struct {
	removed []string
	bootout []string
	err     error
}

func (f *fakePrivilegedOperator) RemoveFileWithSudo(path string) error {
	f.removed = append(f.removed, path)
	return f.err
}

func (f *fakePrivilegedOperator) BootoutDaemonWithSudo(plistPath string) error {
	f.bootout = append(f.bootout, plistPath)
	return f.err
}
