package bootstrap

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshgummersall/summond/internal/state"
)

type fakeInstaller struct {
	installs    [][2]string
	sudoCalls   [][2]string
	removes     []string
	sudoRemoves []string
	err         error
	sudoErr     error
}

func (f *fakeInstaller) Install(src, dst string) error {
	f.installs = append(f.installs, [2]string{src, dst})
	return f.err
}

func (f *fakeInstaller) InstallWithSudo(src, dst string) error {
	f.sudoCalls = append(f.sudoCalls, [2]string{src, dst})
	return f.sudoErr
}

func (f *fakeInstaller) Remove(path string) error {
	f.removes = append(f.removes, path)
	return f.err
}

func (f *fakeInstaller) RemoveWithSudo(path string) error {
	f.sudoRemoves = append(f.sudoRemoves, path)
	return f.sudoErr
}

func TestInitCreatesFilesAndInstalls(t *testing.T) {
	dir := t.TempDir()
	installer := &fakeInstaller{}
	manager := NewManager(state.Paths{
		Home:         filepath.Join(dir, "state"),
		ConfigDir:    filepath.Join(dir, "config"),
		NewsyslogDir: filepath.Join(dir, "newsyslog.d"),
	}, installer)

	result, err := manager.Init(Options{InstallNewsyslog: true})
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if result.ConfigStatus != "created" {
		t.Fatalf("ConfigStatus = %q", result.ConfigStatus)
	}
	if result.NewsyslogGenerateStatus != "created" {
		t.Fatalf("NewsyslogGenerateStatus = %q", result.NewsyslogGenerateStatus)
	}
	if !result.NewsyslogInstalled {
		t.Fatal("expected newsyslog installed")
	}
	if len(installer.installs) != 1 {
		t.Fatalf("installs = %#v", installer.installs)
	}
}

func TestInitReturnsPermissionErrorForRetry(t *testing.T) {
	dir := t.TempDir()
	installer := &fakeInstaller{err: &PermissionError{Err: errors.New("permission denied")}}
	manager := NewManager(state.Paths{
		Home:         filepath.Join(dir, "state"),
		ConfigDir:    filepath.Join(dir, "config"),
		NewsyslogDir: filepath.Join(dir, "newsyslog.d"),
	}, installer)

	result, err := manager.Init(Options{InstallNewsyslog: true})
	if err == nil {
		t.Fatal("expected error")
	}
	var permissionErr *PermissionError
	if !errors.As(err, &permissionErr) {
		t.Fatalf("expected PermissionError, got %T", err)
	}
	if result.ConfigStatus != "created" {
		t.Fatalf("ConfigStatus = %q", result.ConfigStatus)
	}
}

func TestRenderConfigContainsTemplates(t *testing.T) {
	text := renderConfig()
	for _, want := range []string{
		"# Hourly example",
		"# Daily example",
		"# Login example",
		"# Interval example",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("renderConfig missing %q", want)
		}
	}
}

func TestRenderNewsyslogPointsAtManagedLogs(t *testing.T) {
	text := renderNewsyslog(filepath.Join("/tmp", "summond"))
	if !strings.Contains(text, "/tmp/summond/logs/*.log") {
		t.Fatalf("unexpected newsyslog content: %q", text)
	}
}

func TestWriteFileRespectsForce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.txt")
	status, err := writeFile(path, []byte("one"), false)
	if err != nil {
		t.Fatalf("writeFile() error = %v", err)
	}
	if status != "created" {
		t.Fatalf("status = %q", status)
	}
	status, err = writeFile(path, []byte("two"), false)
	if err != nil {
		t.Fatalf("writeFile() error = %v", err)
	}
	if status != "exists" {
		t.Fatalf("status = %q", status)
	}
	status, err = writeFile(path, []byte("three"), true)
	if err != nil {
		t.Fatalf("writeFile() error = %v", err)
	}
	if status != "overwritten" {
		t.Fatalf("status = %q", status)
	}
}

func TestPermissionErrorMessage(t *testing.T) {
	var buf bytes.Buffer
	err := (&PermissionError{Err: errors.New("permission denied")}).Error()
	buf.WriteString(err)
	if buf.String() == "" {
		t.Fatal("expected message")
	}
}

func TestUninstallRemovesFilesAndInstalledConfig(t *testing.T) {
	dir := t.TempDir()
	installer := &fakeInstaller{}
	manager := NewManager(state.Paths{
		Home:         filepath.Join(dir, "state"),
		ConfigDir:    filepath.Join(dir, "config"),
		NewsyslogDir: filepath.Join(dir, "newsyslog.d"),
	}, installer)

	if _, err := manager.Install(Options{InstallNewsyslog: false}); err != nil {
		t.Fatalf("Install() error = %v", err)
	}

	result, err := manager.Uninstall("")
	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	if result.ConfigStatus != "removed" {
		t.Fatalf("ConfigStatus = %q", result.ConfigStatus)
	}
	if result.NewsyslogGenerateStatus != "removed" {
		t.Fatalf("NewsyslogGenerateStatus = %q", result.NewsyslogGenerateStatus)
	}
	if result.NewsyslogInstallStatus != "removed" {
		t.Fatalf("NewsyslogInstallStatus = %q", result.NewsyslogInstallStatus)
	}
	if len(installer.removes) != 1 {
		t.Fatalf("removes = %#v", installer.removes)
	}
}
