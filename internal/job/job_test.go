package job

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeLoginRequiresAgent(t *testing.T) {
	spec := Spec{
		Name:    "job",
		Target:  TargetDaemon,
		Command: "/bin/echo",
		Schedule: Schedule{
			Kind: ScheduleLogin,
		},
	}
	if err := spec.Normalize(); err == nil {
		t.Fatal("expected error")
	}
}

func TestNormalizeHourly(t *testing.T) {
	spec := Spec{
		Name:    "job",
		Target:  TargetAgent,
		Command: "/bin/echo",
		Schedule: Schedule{
			Kind:   ScheduleHourly,
			Minute: 10,
		},
	}
	if err := spec.Normalize(); err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if spec.Label == "" {
		t.Fatal("expected label")
	}
	if got, want := spec.Environment["PATH"], DefaultPath; got != want {
		t.Fatalf("PATH = %q, want %q", got, want)
	}
}

func TestNormalizeDerivesStableHourlyMinute(t *testing.T) {
	specA := Spec{
		Name:    "job",
		Target:  TargetAgent,
		Command: "/bin/echo",
		Schedule: Schedule{
			Kind: ScheduleHourly,
		},
	}
	specB := specA

	if err := specA.Normalize(); err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if err := specB.Normalize(); err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if specA.Schedule.Minute != specB.Schedule.Minute {
		t.Fatalf("minute mismatch: %d != %d", specA.Schedule.Minute, specB.Schedule.Minute)
	}
	if specA.Schedule.Minute < 0 || specA.Schedule.Minute > 59 {
		t.Fatalf("minute out of range: %d", specA.Schedule.Minute)
	}
}

func TestNormalizeDerivesStableWeeklyFields(t *testing.T) {
	spec := Spec{
		Name:    "job",
		Target:  TargetAgent,
		Command: "/bin/echo",
		Schedule: Schedule{
			Kind: ScheduleWeekly,
		},
	}
	if err := spec.Normalize(); err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if spec.Schedule.Weekday < 1 || spec.Schedule.Weekday > 7 {
		t.Fatalf("weekday out of range: %d", spec.Schedule.Weekday)
	}
	if spec.Schedule.Hour < 0 || spec.Schedule.Hour > 23 {
		t.Fatalf("hour out of range: %d", spec.Schedule.Hour)
	}
	if spec.Schedule.Minute < 0 || spec.Schedule.Minute > 59 {
		t.Fatalf("minute out of range: %d", spec.Schedule.Minute)
	}
}

func TestSpecChecksumStableAcrossMapOrder(t *testing.T) {
	specA := Spec{
		Name:    "job",
		Target:  TargetAgent,
		Command: "/bin/echo",
		Args:    []string{"hello"},
		Environment: map[string]string{
			"B": "two",
			"A": "one",
		},
		Schedule: Schedule{
			Kind:   ScheduleHourly,
			Minute: 10,
		},
		StdoutPath: "/tmp/job.out.log",
		StderrPath: "/tmp/job.err.log",
		PlistPath:  "/tmp/job.plist",
	}
	specB := specA
	specB.Environment = map[string]string{
		"A": "one",
		"B": "two",
	}

	sumA, err := specA.SpecChecksum()
	if err != nil {
		t.Fatalf("SpecChecksum() error = %v", err)
	}
	sumB, err := specB.SpecChecksum()
	if err != nil {
		t.Fatalf("SpecChecksum() error = %v", err)
	}
	if sumA != sumB {
		t.Fatalf("checksums differ: %q != %q", sumA, sumB)
	}
}

func TestNormalizeOnChangeTriggerWithoutSchedule(t *testing.T) {
	spec := Spec{
		Name:       "watcher",
		Target:     TargetAgent,
		Command:    "/bin/echo",
		Trigger:    TriggerOnChange,
		WatchPaths: []string{"/tmp/watch.txt"},
	}
	if err := spec.Normalize(); err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if got, want := spec.ThrottleIntervalSeconds, 2; got != want {
		t.Fatalf("ThrottleIntervalSeconds = %d, want %d", got, want)
	}
}

func TestNormalizeRespectsExplicitPathAndThrottle(t *testing.T) {
	spec := Spec{
		Name:    "watcher",
		Target:  TargetAgent,
		Command: "/bin/echo",
		Environment: map[string]string{
			"PATH": "/custom/bin",
		},
		Trigger:                 TriggerOnChange,
		WatchPaths:              []string{"/tmp/watch.txt"},
		ThrottleIntervalSeconds: 10,
	}
	if err := spec.Normalize(); err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if got, want := spec.Environment["PATH"], "/custom/bin"; got != want {
		t.Fatalf("PATH = %q, want %q", got, want)
	}
	if got, want := spec.ThrottleIntervalSeconds, 10; got != want {
		t.Fatalf("ThrottleIntervalSeconds = %d, want %d", got, want)
	}
}

func TestNormalizePreservesExplicitScheduleFields(t *testing.T) {
	spec := Spec{
		Name:    "job",
		Target:  TargetAgent,
		Command: "/bin/echo",
		Schedule: Schedule{
			Kind:       ScheduleWeekly,
			Weekday:    5,
			WeekdaySet: true,
			Hour:       9,
			HourSet:    true,
			Minute:     30,
			MinuteSet:  true,
		},
	}
	if err := spec.Normalize(); err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if spec.Schedule.Weekday != 5 || spec.Schedule.Hour != 9 || spec.Schedule.Minute != 30 {
		t.Fatalf("unexpected schedule: %+v", spec.Schedule)
	}
}

func TestNormalizeResolvesCommandFromPath(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	commandPath := filepath.Join(binDir, "chezmoi")
	if err := os.WriteFile(commandPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	spec := Spec{
		Name:       "job",
		Target:     TargetAgent,
		Command:    "chezmoi",
		WorkingDir: dir,
		Environment: map[string]string{
			"PATH": binDir,
		},
		Schedule: Schedule{
			Kind: ScheduleDaily,
		},
	}

	if err := spec.Normalize(); err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if got, want := spec.Command, commandPath; got != want {
		t.Fatalf("Command = %q, want %q", got, want)
	}
}

func TestNormalizeResolvesRelativeCommandFromWorkingDir(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "script.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	spec := Spec{
		Name:       "job",
		Target:     TargetAgent,
		Command:    "./script.sh",
		WorkingDir: dir,
		Schedule: Schedule{
			Kind: ScheduleDaily,
		},
	}

	if err := spec.Normalize(); err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if got, want := spec.Command, scriptPath; got != want {
		t.Fatalf("Command = %q, want %q", got, want)
	}
}

func TestNormalizeRejectsPathLikeJobName(t *testing.T) {
	spec := Spec{
		Name:    "../escape",
		Target:  TargetAgent,
		Command: "/bin/echo",
		Schedule: Schedule{
			Kind: ScheduleDaily,
		},
	}

	if err := spec.Normalize(); err == nil {
		t.Fatal("expected error")
	}
}

func TestNormalizeRejectsPathLikeLabel(t *testing.T) {
	spec := Spec{
		Name:    "job",
		Label:   "../escape",
		Target:  TargetAgent,
		Command: "/bin/echo",
		Schedule: Schedule{
			Kind: ScheduleDaily,
		},
	}

	if err := spec.Normalize(); err == nil {
		t.Fatal("expected error")
	}
}
