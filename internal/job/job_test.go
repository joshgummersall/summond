package job

import "testing"

func TestNormalizeLoginRequiresAgent(t *testing.T) {
	spec := Spec{
		Name:    "job",
		Target:  TargetDaemon,
		Command: "/bin/echo",
		Schedule: Schedule{
			Kind: ScheduleLogin,
		},
		Enabled: true,
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
		Enabled: true,
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
		Enabled: true,
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
		Enabled: true,
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
		Enabled:    true,
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
		Enabled:    true,
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
		Enabled:                 true,
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
			Kind:      ScheduleWeekly,
			Weekday:   5,
			WeekdaySet: true,
			Hour:      9,
			HourSet:   true,
			Minute:    30,
			MinuteSet: true,
		},
		Enabled: true,
	}
	if err := spec.Normalize(); err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if spec.Schedule.Weekday != 5 || spec.Schedule.Hour != 9 || spec.Schedule.Minute != 30 {
		t.Fatalf("unexpected schedule: %+v", spec.Schedule)
	}
}
