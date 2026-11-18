package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshgummersall/summond/internal/job"
)

// writeTestConfig writes a config file named summond.toml in its own
// directory, so each call gets its own default group.
func writeTestConfig(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "summond.toml")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

// plistExecArgument returns the argument that the installed plist passes to
// summond exec, as launchd would when it starts the job.
func plistExecArgument(t *testing.T, spec job.Spec) string {
	t.Helper()
	data, err := os.ReadFile(spec.PlistPath)
	if err != nil {
		t.Fatalf("ReadFile(plist) error = %v", err)
	}
	const marker = "<string>exec</string>"
	_, rest, ok := strings.Cut(string(data), marker)
	if !ok {
		t.Fatalf("plist has no exec argument:\n%s", data)
	}
	_, rest, ok = strings.Cut(rest, "<string>")
	if !ok {
		t.Fatalf("plist has no argument after exec:\n%s", data)
	}
	arg, _, _ := strings.Cut(rest, "</string>")
	return arg
}

func specsNamed(t *testing.T, app *App, name string) []job.Spec {
	t.Helper()
	specs, err := app.store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	var matches []job.Spec
	for _, spec := range specs {
		if spec.Name == name {
			matches = append(matches, spec)
		}
	}
	return matches
}

func assertPathsExist(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to exist, stat err = %v", path, err)
		}
	}
}

func assertPathsRemoved(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, stat err = %v", path, err)
		}
	}
}

func addUngroupedBackup(t *testing.T, app *App) job.Spec {
	t.Helper()
	if err := app.run([]string{"add", "backup", "--schedule", "daily", "--", "/bin/echo", "ungrouped"}); err != nil {
		t.Fatalf("add error = %v", err)
	}
	spec, err := app.store.Load("backup")
	if err != nil {
		t.Fatalf("Load(backup) error = %v", err)
	}
	if spec.Group != "" {
		t.Fatalf("added job group = %q, want ungrouped", spec.Group)
	}
	return spec
}

func TestSameJobNameInTwoConfigsAppliesAndRunsFromPlist(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout

	firstConfig := writeTestConfig(t,
		"[jobs.backup]",
		`command = "/bin/echo"`,
		`args = ["first"]`,
		`schedule = "daily"`,
	)
	secondConfig := writeTestConfig(t,
		"[jobs.backup]",
		`command = "/bin/echo"`,
		`args = ["second"]`,
		`schedule = "daily"`,
	)
	for _, path := range []string{firstConfig, secondConfig} {
		if err := app.run([]string{"apply", path}); err != nil {
			t.Fatalf("apply %s error = %v", path, err)
		}
	}

	specs := specsNamed(t, app, "backup")
	if len(specs) != 2 || specs[0].Group == specs[1].Group {
		t.Fatalf("specs = %#v, want backup in two groups", specs)
	}
	for _, spec := range specs {
		arg := plistExecArgument(t, spec)
		if arg != spec.ManagedKey() {
			t.Fatalf("plist exec argument = %q, want managed key %q", arg, spec.ManagedKey())
		}
		stdout.Reset()
		if err := app.run([]string{"exec", arg}); err != nil {
			t.Fatalf("exec %s error = %v", arg, err)
		}
		want := spec.Args[0] + "\n"
		if got := stdout.String(); got != want {
			t.Fatalf("exec %s stdout = %q, want %q", arg, got, want)
		}
		loaded, err := app.store.Load(spec.ManagedKey())
		if err != nil {
			t.Fatalf("Load(%s) error = %v", spec.ManagedKey(), err)
		}
		if loaded.RunCount != 1 {
			t.Fatalf("%s RunCount = %d, want 1", spec.ManagedKey(), loaded.RunCount)
		}
	}

	// Later applies keep working.
	for _, path := range []string{firstConfig, secondConfig} {
		if err := app.run([]string{"apply", path}); err != nil {
			t.Fatalf("reapply %s error = %v", path, err)
		}
	}
}

func TestExecSetsPlainJobNameForGroupedJob(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout

	config := writeTestConfig(t,
		`group = "G"`,
		"[jobs.backup]",
		`shell_command = "echo $SUMMOND_JOB_NAME"`,
		`schedule = "daily"`,
	)
	if err := app.run([]string{"apply", config}); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	stdout.Reset()
	if err := app.run([]string{"exec", "G.backup"}); err != nil {
		t.Fatalf("exec error = %v", err)
	}
	if got := stdout.String(); got != "backup\n" {
		t.Fatalf("stdout = %q, want SUMMOND_JOB_NAME=backup", got)
	}
}

func TestRemoveGroupedJobKeepsUngroupedJobWithSameName(t *testing.T) {
	app := newTestApp(t)
	ungrouped := addUngroupedBackup(t, app)

	config := writeTestConfig(t,
		`group = "G"`,
		"[jobs.backup]",
		`command = "/bin/echo"`,
		`schedule = "daily"`,
	)
	if err := app.run([]string{"apply", config}); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	grouped, err := app.store.Load("G.backup")
	if err != nil {
		t.Fatalf("Load(G.backup) error = %v", err)
	}

	if err := app.run([]string{"remove", "G.backup"}); err != nil {
		t.Fatalf("remove error = %v", err)
	}

	assertPathsRemoved(t,
		grouped.PlistPath,
		app.store.JobDirForSpec(grouped),
		grouped.StdoutPath,
		grouped.StderrPath,
	)
	assertPathsExist(t,
		ungrouped.PlistPath,
		app.store.MetadataPathForSpec(ungrouped),
		ungrouped.StdoutPath,
		ungrouped.StderrPath,
	)
	if _, err := app.store.Load("backup"); err != nil {
		t.Fatalf("Load(backup) error = %v", err)
	}
}

func TestPruneRemovesOnlyOrphanWhenAnotherJobHasSameName(t *testing.T) {
	app := newTestApp(t)
	ungrouped := addUngroupedBackup(t, app)

	config := writeTestConfig(t,
		`group = "G"`,
		"[jobs.backup]",
		`command = "/bin/echo"`,
		`schedule = "daily"`,
		"",
		"[jobs.keep]",
		`command = "/bin/echo"`,
		`schedule = "daily"`,
	)
	if err := app.run([]string{"apply", config}); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	grouped, err := app.store.Load("G.backup")
	if err != nil {
		t.Fatalf("Load(G.backup) error = %v", err)
	}

	pruned := writeTestConfig(t,
		`group = "G"`,
		"[jobs.keep]",
		`command = "/bin/echo"`,
		`schedule = "daily"`,
	)
	if err := app.run([]string{"apply", "--prune", pruned}); err != nil {
		t.Fatalf("apply --prune error = %v", err)
	}

	assertPathsRemoved(t, grouped.PlistPath, app.store.JobDirForSpec(grouped))
	assertPathsExist(t,
		ungrouped.PlistPath,
		app.store.MetadataPathForSpec(ungrouped),
		ungrouped.StdoutPath,
		ungrouped.StderrPath,
	)
}

func TestFailedApplyRestoresSameJobNotSameNameInOtherGroup(t *testing.T) {
	app := newTestApp(t)
	runner := app.runner.(*fakeRunner)
	ungrouped := addUngroupedBackup(t, app)

	oldConfig := writeTestConfig(t,
		`group = "G"`,
		"[jobs.backup]",
		`command = "/bin/echo"`,
		`args = ["old"]`,
		`schedule = "daily"`,
	)
	if err := app.run([]string{"apply", oldConfig}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	runner.bootstrapErr = map[string]error{"backup": errors.New("launchctl bootstrap failed")}
	newConfig := writeTestConfig(t,
		`group = "G"`,
		"[jobs.backup]",
		`command = "/bin/echo"`,
		`args = ["new"]`,
		`schedule = "daily"`,
	)
	err := app.run([]string{"apply", newConfig})
	var exitErr ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 1 {
		t.Fatalf("apply error = %v, want exit 1", err)
	}

	grouped, err := app.store.Load("G.backup")
	if err != nil {
		t.Fatalf("Load(G.backup) error = %v", err)
	}
	if len(grouped.Args) != 1 || grouped.Args[0] != "old" {
		t.Fatalf("G.backup Args = %#v, want [old]", grouped.Args)
	}
	restored, err := app.store.Load("backup")
	if err != nil {
		t.Fatalf("Load(backup) error = %v", err)
	}
	if restored.Group != "" || len(restored.Args) != 1 || restored.Args[0] != ungrouped.Args[0] {
		t.Fatalf("ungrouped backup changed: %#v", restored)
	}
}

func TestFailedFirstApplyRemovesNewJobNotSameNameInOtherGroup(t *testing.T) {
	app := newTestApp(t)
	runner := app.runner.(*fakeRunner)
	ungrouped := addUngroupedBackup(t, app)

	runner.bootstrapErr = map[string]error{"backup": errors.New("launchctl bootstrap failed")}
	config := writeTestConfig(t,
		`group = "G"`,
		"[jobs.backup]",
		`command = "/bin/echo"`,
		`schedule = "daily"`,
	)
	err := app.run([]string{"apply", config})
	var exitErr ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 1 {
		t.Fatalf("apply error = %v, want exit 1", err)
	}

	if _, err := app.store.Load("G.backup"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load(G.backup) error = %v, want not exist", err)
	}
	assertPathsExist(t, ungrouped.PlistPath, app.store.MetadataPathForSpec(ungrouped))
}

func TestAmbiguousBareNameFailsAndListsManagedKeys(t *testing.T) {
	app := newTestApp(t)
	for _, group := range []string{"a", "b"} {
		config := writeTestConfig(t,
			`group = "`+group+`"`,
			"[jobs.backup]",
			`command = "/bin/echo"`,
			`schedule = "daily"`,
		)
		if err := app.run([]string{"apply", config}); err != nil {
			t.Fatalf("apply group %s error = %v", group, err)
		}
	}

	for _, command := range [][]string{
		{"exec", "backup"},
		{"remove", "backup"},
		{"state", "backup"},
		{"plist", "backup"},
		{"logs", "backup"},
		{"kill", "backup"},
		{"restart", "backup"},
		{"cd", "backup"},
	} {
		err := app.run(command)
		if err == nil {
			t.Fatalf("%v error = nil, want ambiguity error", command)
		}
		for _, key := range []string{"a.backup", "b.backup"} {
			if !strings.Contains(err.Error(), key) {
				t.Fatalf("%v error = %q, want it to list %s", command, err, key)
			}
		}
	}
	if got := len(specsNamed(t, app, "backup")); got != 2 {
		t.Fatalf("backup jobs = %d after failed commands, want 2", got)
	}
}

func TestManagedKeyArgumentSelectsUngroupedJobOverGroupedName(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout
	ungrouped := addUngroupedBackup(t, app)

	config := writeTestConfig(t,
		`group = "G"`,
		"[jobs.backup]",
		`command = "/bin/echo"`,
		`args = ["grouped"]`,
		`schedule = "daily"`,
	)
	if err := app.run([]string{"apply", config}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	// The ungrouped job's managed key is its name, and its plist passes that
	// key to exec, so the exact key must still select it.
	arg := plistExecArgument(t, ungrouped)
	if arg != "backup" {
		t.Fatalf("ungrouped plist exec argument = %q, want backup", arg)
	}
	stdout.Reset()
	if err := app.run([]string{"exec", arg}); err != nil {
		t.Fatalf("exec %s error = %v", arg, err)
	}
	if got := stdout.String(); got != "ungrouped\n" {
		t.Fatalf("stdout = %q, want ungrouped job output", got)
	}
}

func TestJobInstalledBeforeManagedKeyPlistKeepsRunning(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout

	config := writeTestConfig(t,
		`group = "G"`,
		"[jobs.backup]",
		`command = "/bin/echo"`,
		`args = ["ran"]`,
		`schedule = "daily"`,
	)
	if err := app.run([]string{"apply", config}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	// A plist written before this change runs "exec <name>". That must keep
	// working while the name matches one job.
	stdout.Reset()
	if err := app.run([]string{"exec", "backup"}); err != nil {
		t.Fatalf("exec by bare name error = %v", err)
	}
	if got := stdout.String(); got != "ran\n" {
		t.Fatalf("stdout = %q", got)
	}

	// The next apply rewrites the plist to run the managed key.
	if err := app.run([]string{"apply", config}); err != nil {
		t.Fatalf("reapply error = %v", err)
	}
	spec, err := app.store.Load("G.backup")
	if err != nil {
		t.Fatalf("Load(G.backup) error = %v", err)
	}
	arg := plistExecArgument(t, spec)
	if arg != "G.backup" {
		t.Fatalf("plist exec argument = %q, want G.backup", arg)
	}
	stdout.Reset()
	if err := app.run([]string{"exec", arg}); err != nil {
		t.Fatalf("exec %s error = %v", arg, err)
	}
	if got := stdout.String(); got != "ran\n" {
		t.Fatalf("stdout = %q", got)
	}
	spec, err = app.store.Load("G.backup")
	if err != nil {
		t.Fatalf("Load(G.backup) error = %v", err)
	}
	if spec.RunCount != 2 {
		t.Fatalf("RunCount = %d, want 2", spec.RunCount)
	}
}

func TestAddAllowsUngroupedJobWhenGroupedJobHasSameName(t *testing.T) {
	app := newTestApp(t)
	config := writeTestConfig(t,
		`group = "G"`,
		"[jobs.backup]",
		`command = "/bin/echo"`,
		`schedule = "daily"`,
	)
	if err := app.run([]string{"apply", config}); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	addUngroupedBackup(t, app)
	if err := app.run([]string{"add", "backup", "--schedule", "daily", "--", "/bin/echo"}); err == nil {
		t.Fatal("second add of backup error = nil, want already exists")
	}
}
