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
	if strings.Contains(text, "<key>EnvironmentVariables</key>") {
		t.Fatalf("unexpected EnvironmentVariables in plist: %s", text)
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
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(data)
	if strings.Contains(text, "LimitLoadToSessionType") {
		t.Fatalf("unexpected LimitLoadToSessionType in daemon plist: %s", text)
	}
}

func TestRenderRunAtLoadDoesNotImplicitlyAbandonProcessGroup(t *testing.T) {
	data, err := Render(job.Spec{
		Name:              "login-job",
		Target:            job.TargetAgent,
		Command:           "/bin/echo",
		Args:              []string{"login"},
		RuntimeBinaryPath: "/tmp/summond",
		Schedule: job.Schedule{
			Kind: job.ScheduleLogin,
		},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "<key>RunAtLoad</key>") {
		t.Fatalf("plist missing RunAtLoad: %s", text)
	}
	if strings.Contains(text, "AbandonProcessGroup") {
		t.Fatalf("unexpected AbandonProcessGroup in plist: %s", text)
	}
}

func TestRenderExplicitAbandonProcessGroup(t *testing.T) {
	data, err := Render(job.Spec{
		Name:                "launcher",
		Target:              job.TargetAgent,
		Command:             "/bin/echo",
		Args:                []string{"launch"},
		RuntimeBinaryPath:   "/tmp/summond",
		AbandonProcessGroup: true,
		Schedule: job.Schedule{
			Kind: job.ScheduleDaily,
			Hour: 3, HourSet: true,
			Minute: 45, MinuteSet: true,
		},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(data)
	for _, needle := range []string{
		"<key>AbandonProcessGroup</key>",
		"<true/>",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("plist missing %q: %s", needle, text)
		}
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
