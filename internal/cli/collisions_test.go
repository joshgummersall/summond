package cli

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/joshgummersall/summond/internal/job"
)

func collisionSpec(t *testing.T, group, name, label string) job.Spec {
	t.Helper()
	spec := job.Spec{
		Group:    group,
		Name:     name,
		Label:    label,
		Command:  "/bin/echo",
		Schedule: job.Schedule{Kind: job.ScheduleDaily},
	}
	if err := spec.Normalize(); err != nil {
		t.Fatalf("Normalize(%s) error = %v", spec.ManagedKey(), err)
	}
	return spec
}

func TestJobCollisions(t *testing.T) {
	const prefix = "com.joshgummersall.summond."
	for _, tc := range []struct {
		name     string
		desired  [][3]string // group, name, label
		existing [][3]string
		want     []string // one line per collision; empty means none
	}{
		{
			name:    "default labels collide in config",
			desired: [][3]string{{"g", "foo_bar", ""}, {"g", "foo-bar", ""}},
			want:    []string{`job "g.foo_bar" and job "g.foo-bar" share label "` + prefix + `g.foo-bar"`},
		},
		{
			name:    "groups that differ in case",
			desired: [][3]string{{"Work", "backup", ""}, {"work", "backup", ""}},
			want:    []string{`job "Work.backup" and job "work.backup" share managed key "Work.backup" and label "` + prefix + `work.backup"`},
		},
		{
			name:     "dotted name against dotted group",
			desired:  [][3]string{{"a", "b.c", ""}},
			existing: [][3]string{{"a.b", "c", ""}},
			want:     []string{`job "a.b.c" (group "a") and existing job "a.b.c" (group "a.b") share managed key "a.b.c" and label "` + prefix + `a.b.c"`},
		},
		{
			name:     "ungrouped dotted name against grouped job",
			desired:  [][3]string{{"", "g.backup", ""}},
			existing: [][3]string{{"g", "backup", ""}},
			// Same managed key, but a different group and name: a different job.
			want: []string{`job "g.backup" (ungrouped) and existing job "g.backup" (group "g") share managed key "g.backup" and label "` + prefix + `g.backup"`},
		},
		{
			name:     "managed key differs only in case",
			desired:  [][3]string{{"", "Foo", ""}},
			existing: [][3]string{{"", "foo", ""}},
			want:     []string{`job "Foo" and existing job "foo" share managed key "Foo" and label "` + prefix + `foo"`},
		},
		{
			name:     "explicit label equals existing label",
			desired:  [][3]string{{"g", "a", "com.example.x"}},
			existing: [][3]string{{"", "b", "com.example.x"}},
			want:     []string{`job "g.a" and existing job "b" share label "com.example.x"`},
		},
		{
			name:     "labels compare without regard to case",
			desired:  [][3]string{{"g", "a", "com.example.X"}},
			existing: [][3]string{{"", "b", "com.example.x"}},
			want:     []string{`job "g.a" and existing job "b" share label "com.example.X"`},
		},
		{
			name:    "explicit label makes jobs distinct",
			desired: [][3]string{{"g", "foo_bar", "com.example.underscore"}, {"g", "foo-bar", ""}},
		},
		{
			name:     "unchanged job is not a collision",
			desired:  [][3]string{{"g", "foo", ""}},
			existing: [][3]string{{"g", "foo", ""}},
		},
		{
			name:     "updated job is compared with its new label only",
			desired:  [][3]string{{"g", "foo", "com.example.new"}, {"g", "bar", "com.example.old"}},
			existing: [][3]string{{"g", "foo", "com.example.old"}},
		},
		{
			name:     "all collisions are listed",
			desired:  [][3]string{{"g", "x_y", ""}, {"g", "x-y", ""}, {"", "Foo", ""}},
			existing: [][3]string{{"", "foo", ""}},
			want: []string{
				`job "g.x_y" and job "g.x-y" share label`,
				`job "Foo" and existing job "foo" share managed key`,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			toSpecs := func(rows [][3]string) []job.Spec {
				var specs []job.Spec
				for _, row := range rows {
					specs = append(specs, collisionSpec(t, row[0], row[1], row[2]))
				}
				return specs
			}
			err := checkJobCollisions(toSpecs(tc.desired), toSpecs(tc.existing))
			if len(tc.want) == 0 {
				if err != nil {
					t.Fatalf("checkJobCollisions() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("checkJobCollisions() error = nil, want %q", tc.want)
			}
			lines := strings.Split(err.Error(), "\n")
			if len(lines) != len(tc.want)+1 {
				t.Fatalf("checkJobCollisions() error = %q, want %d collision line(s)", err, len(tc.want))
			}
			for i, want := range tc.want {
				if !strings.HasPrefix(lines[i+1], "- "+want) {
					t.Fatalf("line %d = %q, want prefix %q", i+1, lines[i+1], "- "+want)
				}
			}
		})
	}
}

// assertNothingInstalled checks that no job state and no plist exist.
func assertNothingInstalled(t *testing.T, app *App) {
	t.Helper()
	specs, err := app.store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(specs) != 0 {
		t.Fatalf("installed specs = %#v, want none", specs)
	}
	entries, err := os.ReadDir(app.store.Paths().LaunchDir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("ReadDir(LaunchDir) error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("LaunchDir has %d entries, want none", len(entries))
	}
}

func fooBarConfig(t *testing.T, extra ...string) string {
	t.Helper()
	lines := []string{
		`group = "g"`,
		"[jobs.foo_bar]",
		`command = "/bin/echo"`,
		`schedule = "daily"`,
	}
	lines = append(lines, extra...)
	lines = append(lines,
		"[jobs.foo-bar]",
		`command = "/bin/echo"`,
		`schedule = "daily"`,
	)
	return writeTestConfig(t, lines...)
}

func TestApplyRejectsConfigWithCollidingDefaultLabels(t *testing.T) {
	config := fooBarConfig(t)
	for _, dryRun := range []bool{false, true} {
		app := newTestApp(t)
		args := []string{"apply", config}
		if dryRun {
			args = []string{"apply", "--dry-run", config}
		}
		err := app.run(args)
		if err == nil {
			t.Fatalf("%v error = nil, want collision", args)
		}
		for _, want := range []string{`"g.foo_bar"`, `"g.foo-bar"`, `share label "com.joshgummersall.summond.g.foo-bar"`} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("%v error = %q, want it to contain %s", args, err, want)
			}
		}
		assertNothingInstalled(t, app)
		if runner := app.runner.(*fakeRunner); len(runner.bootstrapped) != 0 {
			t.Fatalf("bootstrapped = %v, want none", runner.bootstrapped)
		}
	}
}

func TestApplyAcceptsExplicitLabelThatMakesJobsDistinct(t *testing.T) {
	app := newTestApp(t)
	config := fooBarConfig(t, `label = "com.example.foo-underscore-bar"`)
	if err := app.run([]string{"apply", config}); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	// Applying the unchanged config again is not a collision.
	if err := app.run([]string{"apply", config}); err != nil {
		t.Fatalf("reapply error = %v", err)
	}
	if err := app.run([]string{"apply", "--dry-run", config}); err != nil {
		t.Fatalf("reapply --dry-run error = %v", err)
	}
	specs, err := app.store.List()
	if err != nil || len(specs) != 2 {
		t.Fatalf("List() = %v, %v; want two jobs", specs, err)
	}
}

func TestApplyAndAddRejectLabelOfExistingJob(t *testing.T) {
	t.Run("apply", func(t *testing.T) {
		app := newTestApp(t)
		addUngroupedBackup(t, app)
		config := writeTestConfig(t,
			`group = "g"`,
			"[jobs.copy]",
			`command = "/bin/echo"`,
			`schedule = "daily"`,
			`label = "com.joshgummersall.summond.backup"`,
		)
		for _, args := range [][]string{{"apply", "--dry-run", config}, {"apply", config}} {
			err := app.run(args)
			if err == nil || !strings.Contains(err.Error(), `job "g.copy" and existing job "backup" share label "com.joshgummersall.summond.backup"`) {
				t.Fatalf("%v error = %v, want label collision", args, err)
			}
		}
		if _, err := app.store.Load("g.copy"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Load(g.copy) error = %v, want not exist", err)
		}
	})
	t.Run("add", func(t *testing.T) {
		app := newTestApp(t)
		if err := app.run([]string{"add", "foo_bar", "--schedule", "daily", "--", "/bin/echo"}); err != nil {
			t.Fatalf("add foo_bar error = %v", err)
		}
		before, err := os.ReadFile(app.store.MetadataPath("foo_bar"))
		if err != nil {
			t.Fatalf("ReadFile(foo_bar metadata) error = %v", err)
		}
		for _, args := range [][]string{
			{"add", "foo-bar", "--dry-run", "--schedule", "daily", "--", "/bin/echo"},
			{"add", "foo-bar", "--schedule", "daily", "--", "/bin/echo"},
		} {
			err := app.run(args)
			if err == nil || !strings.Contains(err.Error(), `job "foo-bar" and existing job "foo_bar" share label "com.joshgummersall.summond.foo-bar"`) {
				t.Fatalf("%v error = %v, want label collision", args, err)
			}
		}
		if _, err := app.store.Load("foo-bar"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Load(foo-bar) error = %v, want not exist", err)
		}
		after, err := os.ReadFile(app.store.MetadataPath("foo_bar"))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("foo_bar metadata changed (err = %v)", err)
		}
	})
}

func TestApplyAndAddRejectManagedKeyThatDiffersOnlyInCase(t *testing.T) {
	t.Run("apply", func(t *testing.T) {
		app := newTestApp(t)
		first := writeTestConfig(t, `group = "Work"`, "[jobs.backup]", `command = "/bin/echo"`, `schedule = "daily"`)
		second := writeTestConfig(t, `group = "work"`, "[jobs.backup]", `command = "/bin/echo"`, `schedule = "daily"`)
		if err := app.run([]string{"apply", first}); err != nil {
			t.Fatalf("apply first error = %v", err)
		}
		for _, args := range [][]string{{"apply", "--dry-run", second}, {"apply", second}} {
			err := app.run(args)
			if err == nil || !strings.Contains(err.Error(), `job "work.backup" and existing job "Work.backup" share managed key "work.backup"`) {
				t.Fatalf("%v error = %v, want managed key collision", args, err)
			}
		}
		specs, err := app.store.List()
		if err != nil || len(specs) != 1 || specs[0].ManagedKey() != "Work.backup" {
			t.Fatalf("List() = %v, %v; want only Work.backup", specs, err)
		}
	})
	t.Run("add", func(t *testing.T) {
		app := newTestApp(t)
		// A grouped job whose label the new key does not share, so only
		// the managed key collides.
		config := writeTestConfig(t, `group = "G"`, "[jobs.backup]", `command = "/bin/echo"`, `schedule = "daily"`, `label = "com.example.grouped"`)
		if err := app.run([]string{"apply", config}); err != nil {
			t.Fatalf("apply error = %v", err)
		}
		for _, args := range [][]string{
			{"add", "g.backup", "--dry-run", "--schedule", "daily", "--", "/bin/echo"},
			{"add", "g.backup", "--schedule", "daily", "--", "/bin/echo"},
		} {
			err := app.run(args)
			if err == nil || !strings.Contains(err.Error(), `job "g.backup" and existing job "G.backup" share managed key "g.backup"`) {
				t.Fatalf("%v error = %v, want managed key collision", args, err)
			}
		}
		specs, err := app.store.List()
		if err != nil || len(specs) != 1 {
			t.Fatalf("List() = %v, %v; want one job", specs, err)
		}
	})
}

func TestApplyRejectsGroupedJobWithManagedKeyOfUngroupedJob(t *testing.T) {
	app := newTestApp(t)
	if err := app.run([]string{"add", "g.backup", "--schedule", "daily", "--", "/bin/echo", "ungrouped"}); err != nil {
		t.Fatalf("add error = %v", err)
	}
	before, err := os.ReadFile(app.store.MetadataPath("g.backup"))
	if err != nil {
		t.Fatalf("ReadFile(metadata) error = %v", err)
	}
	config := writeTestConfig(t, `group = "g"`, "[jobs.backup]", `command = "/bin/echo"`, `schedule = "daily"`)
	err = app.run([]string{"apply", config})
	if err == nil || !strings.Contains(err.Error(), `job "g.backup" (group "g") and existing job "g.backup" (ungrouped) share managed key "g.backup"`) {
		t.Fatalf("apply error = %v, want managed key collision", err)
	}
	after, err := os.ReadFile(app.store.MetadataPath("g.backup"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("ungrouped job metadata changed (err = %v)", err)
	}
}
