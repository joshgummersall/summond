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
