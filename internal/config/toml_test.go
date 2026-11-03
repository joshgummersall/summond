package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshgummersall/summond/internal/job"
)

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "summond.toml")
	data := []byte(`
[jobs.backup]
command = "/bin/echo"
args = ["backup", "now"]
schedule = "weekly"
weekday = 2
hour = 1
minute = 30

[jobs.backup.env]
MODE = "fast"
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	specs, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("len(specs) = %d", len(specs))
	}
	if got, want := specs[0].Environment["MODE"], "fast"; got != want {
		t.Fatalf("MODE = %q, want %q", got, want)
	}
	if got, want := specs[0].WorkingDir, dir; got != want {
		t.Fatalf("WorkingDir = %q, want %q", got, want)
	}
}

func TestLoadFileDefaultsTargetToAgentWhenOmitted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "summond.toml")
	data := []byte(`
[jobs.cleanup]
command = "/bin/echo"
args = ["cleanup"]
schedule = "daily"
hour = 3
minute = 45
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	specs, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("len(specs) = %d", len(specs))
	}
	if got, want := specs[0].Target, "agent"; string(got) != want {
		t.Fatalf("Target = %q, want %q", got, want)
	}
}

func TestLoadFileResolvesRelativeWatchPaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "configs", "summond.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	data := []byte(`
[jobs.watcher]
command = "/bin/echo"
args = ["watch"]
trigger = "on_change"
watch_paths = ["../data/input.txt", "/tmp/absolute.txt"]
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	specs, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	got := specs[0].WatchPaths
	want0 := filepath.Clean(filepath.Join(filepath.Dir(path), "../data/input.txt"))
	if got[0] != want0 {
		t.Fatalf("WatchPaths[0] = %q, want %q", got[0], want0)
	}
	if got[1] != "/tmp/absolute.txt" {
		t.Fatalf("WatchPaths[1] = %q", got[1])
	}
	if got, want := specs[0].WorkingDir, filepath.Dir(path); got != want {
		t.Fatalf("WorkingDir = %q, want %q", got, want)
	}
}

func TestLoadFileRespectsExplicitWorkingDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "configs", "summond.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	data := []byte(`
[jobs.worker]
command = "/bin/echo"
args = ["work"]
schedule = "hourly"
minute = 5
working_dir = "/tmp/custom-working-dir"
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	specs, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if got, want := specs[0].WorkingDir, "/tmp/custom-working-dir"; got != want {
		t.Fatalf("WorkingDir = %q, want %q", got, want)
	}
}

func TestLoadFileParsesMultilineShellCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "summond.toml")
	data := []byte(`
[jobs.brew-maintenance]
shell_command = """
brew update
brew upgrade
brew cleanup
"""
schedule = "weekly"
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	specs, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	got := specs[0].ShellCommand
	want := "brew update\nbrew upgrade\nbrew cleanup\n"
	if got != want {
		t.Fatalf("ShellCommand = %q, want %q", got, want)
	}
}

func TestLoadFilePreservesCommandForRuntimeResolution(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "summond.toml")
	data := []byte(`
[jobs.chezmoi-update]
command = "chezmoi"
args = ["update"]
schedule = "daily"

[jobs.chezmoi-update.env]
PATH = "/custom/bin"
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	specs, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if got, want := specs[0].Command, "chezmoi"; got != want {
		t.Fatalf("Command = %q, want %q", got, want)
	}
}

func TestLoadFileParsesAbandonProcessGroup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "summond.toml")
	data := []byte(`
[jobs.launcher]
command = "/bin/echo"
args = ["launch"]
schedule = "login"
abandon_process_group = true
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	specs, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if !specs[0].AbandonProcessGroup {
		t.Fatal("AbandonProcessGroup = false, want true")
	}
}

func TestLoadFileParsesNestedRetry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "summond.toml")
	data := []byte(`
[jobs.flaky]
command = "/bin/echo"
args = ["retry"]
schedule = "login"

[jobs.flaky.retry]
attempts = 3
delay_seconds = 2
max_delay_seconds = 60
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	specs, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if got, want := specs[0].RetryAttempts, 3; got != want {
		t.Fatalf("RetryAttempts = %d, want %d", got, want)
	}
	if got, want := specs[0].RetryDelaySeconds, 2; got != want {
		t.Fatalf("RetryDelaySeconds = %d, want %d", got, want)
	}
	if got, want := specs[0].RetryMaxDelaySeconds, 60; got != want {
		t.Fatalf("RetryMaxDelaySeconds = %d, want %d", got, want)
	}
}

func TestLoadFileParsesNestedWatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "summond.toml")
	data := []byte(`
[jobs.watcher2]
command = "/bin/echo"
args = ["watch"]
trigger = "on_change"

[jobs.watcher2.watch]
paths = ["/tmp"]
throttle_seconds = 5
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	specs, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if got, want := len(specs[0].WatchPaths), 1; got != want {
		t.Fatalf("len(WatchPaths) = %d, want %d", got, want)
	}
	if got, want := specs[0].WatchPaths[0], "/tmp"; got != want {
		t.Fatalf("WatchPaths[0] = %q, want %q", got, want)
	}
	if got, want := specs[0].ThrottleIntervalSeconds, 5; got != want {
		t.Fatalf("ThrottleIntervalSeconds = %d, want %d", got, want)
	}
}

func TestLoadFileRejectsManagedLogOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "summond.toml")
	data := []byte(`
[jobs.cleanup]
command = "/bin/echo"
schedule = "daily"
stdout_path = "/tmp/custom.out"
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := LoadFile(path)
	if err == nil || err.Error() != "unknown config key(s): jobs.cleanup.stdout_path" {
		t.Fatalf("LoadFile() error = %v", err)
	}
}

func TestLoadFileAppliesCustomWindow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "summond.toml")
	data := []byte(`
[windows]
morning = { start_hour = 6, end_hour = 6 }

[jobs.report]
command = "/bin/echo"
schedule = "daily"
window = "morning"
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	specs, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("len(specs) = %d", len(specs))
	}
	if got, want := specs[0].Schedule.Hour, 6; got != want {
		t.Fatalf("Hour = %d, want %d", got, want)
	}
}

func TestLoadFileAppliesCustomWindowToWeeklySchedule(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "summond.toml")
	data := []byte(`
[windows]
morning = { start_hour = 6, end_hour = 6 }

[jobs.report]
command = "/bin/echo"
schedule = "weekly"
weekday = 3
window = "morning"
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	specs, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("len(specs) = %d", len(specs))
	}
	if got, want := specs[0].Schedule.Weekday, 3; got != want {
		t.Fatalf("Weekday = %d, want %d", got, want)
	}
	if got, want := specs[0].Schedule.Hour, 6; got != want {
		t.Fatalf("Hour = %d, want %d", got, want)
	}
}

func TestLoadFileRejectsWindowOnHourlySchedule(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "summond.toml")
	data := []byte(`
[jobs.report]
command = "/bin/echo"
schedule = "hourly"
window = "morning"
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := LoadFile(path); err == nil {
		t.Fatal("expected error for window on hourly schedule")
	}
}

func TestLoadFileRejectsUnknownWindowName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "summond.toml")
	data := []byte(`
[windows]
night = { start_hour = 22, end_hour = 23 }

[jobs.report]
command = "/bin/echo"
schedule = "daily"
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := LoadFile(path); err == nil {
		t.Fatal("expected error for unknown window name")
	}
}

func TestLoadFileRejectsInvertedWindowBounds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "summond.toml")
	data := []byte(`
[windows]
morning = { start_hour = 10, end_hour = 5 }

[jobs.report]
command = "/bin/echo"
schedule = "daily"
window = "morning"
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := LoadFile(path); err == nil {
		t.Fatal("expected error for inverted window bounds")
	}
}

func TestLoadFileParsesUSBTriggerHexStrings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "summond.toml")
	data := []byte(`
[jobs.brio-zoom]
command = "/usr/local/bin/uvc-util"
args = ["-I", "0", "-s", "zoom-abs=150"]
trigger = "on_usb_attach"

[jobs.brio-zoom.usb]
vendor_id  = "0x046d"
product_id = "0x085e"
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	specs, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if got, want := specs[0].USBVendorID, 0x046d; got != want {
		t.Fatalf("USBVendorID = %#x, want %#x", got, want)
	}
	if got, want := specs[0].USBProductID, 0x085e; got != want {
		t.Fatalf("USBProductID = %#x, want %#x", got, want)
	}
}

func TestLoadFileParsesUSBTriggerIntegerIDs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "summond.toml")
	data := []byte(`
[jobs.brio-zoom]
command = "/usr/local/bin/uvc-util"
trigger = "on_usb_attach"

[jobs.brio-zoom.usb]
vendor_id  = 0x046d
product_id = 2142
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	specs, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if got, want := specs[0].USBVendorID, 0x046d; got != want {
		t.Fatalf("USBVendorID = %#x, want %#x", got, want)
	}
	if got, want := specs[0].USBProductID, 0x085e; got != want {
		t.Fatalf("USBProductID = %#x, want %#x", got, want)
	}
}

func TestLoadFileRejectsInvalidUSBID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "summond.toml")
	data := []byte(`
[jobs.brio-zoom]
command = "/usr/local/bin/uvc-util"
trigger = "on_usb_attach"

[jobs.brio-zoom.usb]
vendor_id  = "046d"
product_id = "0x085e"
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := LoadFile(path); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadFileRejectsUSBTriggerWithoutIDs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "summond.toml")
	data := []byte(`
[jobs.brio-zoom]
command = "/usr/local/bin/uvc-util"
trigger = "on_usb_attach"
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := LoadFile(path); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadTopLevelTarget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
		want   job.Target
	}{
		{"default", "", job.TargetAgent},
		{"agent", `target = "agent"`, job.TargetAgent},
		{"daemon", `target = "daemon"`, job.TargetDaemon},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "summond.toml")
			data := tc.header + "\n[jobs.a]\ncommand = \"/bin/echo\"\nschedule = \"daily\"\n\n[jobs.b]\ncommand = \"/bin/echo\"\nschedule = \"hourly\"\n"
			if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			file, err := Load(path)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if file.Target != tc.want {
				t.Fatalf("Target = %q, want %q", file.Target, tc.want)
			}
			if len(file.Specs) != 2 {
				t.Fatalf("len(Specs) = %d, want 2", len(file.Specs))
			}
			for _, spec := range file.Specs {
				if spec.Target != tc.want {
					t.Fatalf("%s Target = %q, want %q", spec.Name, spec.Target, tc.want)
				}
			}
		})
	}
}

func TestLoadRejectsInvalidTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summond.toml")
	if err := os.WriteFile(path, []byte("target = \"system\"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), `invalid target "system"`) {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadRejectsPerJobTargetWithHint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summond.toml")
	data := "[jobs.a]\ncommand = \"/bin/echo\"\ntarget = \"daemon\"\nschedule = \"daily\"\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil")
	}
	for _, want := range []string{"unknown config key(s): jobs.a.target", "move it to the top"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Load() error = %v, want %q", err, want)
		}
	}
}

func TestLoadUnknownKeyWithoutTargetHasNoHint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summond.toml")
	data := "[jobs.a]\ncommand = \"/bin/echo\"\nschedule = \"daily\"\nbogus = 1\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	_, err := Load(path)
	if err == nil || strings.Contains(err.Error(), "move it to the top") {
		t.Fatalf("Load() error = %v", err)
	}
}
