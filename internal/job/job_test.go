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
