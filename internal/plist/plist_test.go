package plist

import (
	"strings"
	"testing"

	"github.com/standardlabs/summond/internal/job"
)

func TestRenderIntervalPlist(t *testing.T) {
	data, err := Render(job.Spec{
		Name:              "sync",
		Target:            job.TargetAgent,
		Command:           "/bin/echo",
		Args:              []string{"hi"},
		Checksum:          "checksum-123",
		RuntimeBinaryPath: "/tmp/summond",
		Schedule: job.Schedule{
			Kind:            job.ScheduleInterval,
			IntervalMinutes: 30,
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(data)
	for _, needle := range []string{
		"<key>SummondSpecChecksum</key>",
		"<string>checksum-123</string>",
		"<key>LimitLoadToSessionType</key>",
		"<string>Aqua</string>",
		"<key>EnvironmentVariables</key>",
		"<key>PATH</key>",
		"<string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>",
		"<key>StartInterval</key>",
		"<integer>1800</integer>",
		"<string>/tmp/summond</string>",
		"<string>exec</string>",
		"<string>sync</string>",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("plist missing %q: %s", needle, text)
		}
	}
}

func TestRenderOnChangePlist(t *testing.T) {
	data, err := Render(job.Spec{
		Name:              "watcher",
		Target:            job.TargetAgent,
		Command:           "/bin/echo",
		Args:              []string{"watch"},
		Trigger:           job.TriggerOnChange,
		WatchPaths:        []string{"/tmp/watch.txt"},
		RuntimeBinaryPath: "/tmp/summond",
		Enabled:           true,
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(data)
	for _, needle := range []string{
		"<key>LimitLoadToSessionType</key>",
		"<string>Aqua</string>",
		"<key>WatchPaths</key>",
		"<string>/tmp/watch.txt</string>",
		"<key>ThrottleInterval</key>",
		"<integer>2</integer>",
		"<string>/tmp/summond</string>",
		"<string>exec</string>",
		"<string>watcher</string>",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("plist missing %q: %s", needle, text)
		}
	}
}

func TestRenderDaemonPlistDoesNotSetAquaSessionLimit(t *testing.T) {
	data, err := Render(job.Spec{
		Name:              "daemon-job",
		Target:            job.TargetDaemon,
		Command:           "/bin/echo",
		Args:              []string{"daemon"},
		RuntimeBinaryPath: "/tmp/summond",
		Schedule: job.Schedule{
			Kind: job.ScheduleBoot,
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(data)
	if strings.Contains(text, "LimitLoadToSessionType") {
		t.Fatalf("unexpected LimitLoadToSessionType in daemon plist: %s", text)
	}
}

func TestRenderShellCommandUsesBashStrictMode(t *testing.T) {
	data, err := Render(job.Spec{
		Name:              "shell-job",
		Target:            job.TargetAgent,
		ShellCommand:      "echo hello",
		RuntimeBinaryPath: "/tmp/summond",
		Schedule: job.Schedule{
			Kind: job.ScheduleLogin,
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(data)
	for _, needle := range []string{
		"<string>/tmp/summond</string>",
		"<string>exec</string>",
		"<string>shell-job</string>",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("plist missing %q: %s", needle, text)
		}
	}
}
