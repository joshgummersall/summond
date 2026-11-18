package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshgummersall/summond/internal/job"
)

func TestUnsafePathEntries(t *testing.T) {
	me := uint32(os.Getuid())
	dir := t.TempDir()
	safe := filepath.Join(dir, "safe")
	groupWritable := filepath.Join(dir, "group")
	otherWritable := filepath.Join(dir, "other")
	for path, mode := range map[string]os.FileMode{safe: 0o755, groupWritable: 0o775, otherWritable: 0o757} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatalf("Mkdir() error = %v", err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatalf("Chmod() error = %v", err)
		}
	}
	missing := filepath.Join(dir, "missing")

	got := unsafePathEntries(strings.Join([]string{safe, groupWritable, otherWritable, missing, "", ".", "bin"}, ":"), me)
	want := []string{
		groupWritable + " (writable by group)",
		otherWritable + " (writable by other)",
		`"" (empty entry, resolves to the working directory)`,
		`. (relative entry, resolves against the working directory)`,
		`bin (relative entry, resolves against the working directory)`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("unsafePathEntries() =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	got = unsafePathEntries(safe, me+1)
	if len(got) != 1 || !strings.HasPrefix(got[0], safe+" (owned by uid ") {
		t.Fatalf("unsafePathEntries(other owner) = %q, want an owner warning for %s", got, safe)
	}
}

// writeApplyFixture writes an env file with the given PATH into the app's
// state and a one-job config with the given header, and returns the config
// path. The job runs root-owned /bin/echo in root-owned /, so it raises no
// job path warning.
func writeApplyFixture(t *testing.T, app *App, header, pathValue string) string {
	t.Helper()
	return writeJobFixture(t, app, header, pathValue, "/bin/echo", "/")
}

// writeJobFixture is writeApplyFixture with the job's command and working
// directory.
func writeJobFixture(t *testing.T, app *App, header, pathValue, command, workingDir string) string {
	t.Helper()
	if err := app.writeEnvFileContents(app.store.EnvFilePath(), []byte(`{"PATH": "`+pathValue+`"}`+"\n")); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	configPath := filepath.Join(testHome(t), "summond.toml")
	data := header + "\n[jobs.cleanup]\ncommand = \"" + command + "\"\nworking_dir = \"" + workingDir + "\"\nschedule = \"daily\"\nhour = 3\nminute = 45\n"
	if err := os.WriteFile(configPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return configPath
}

func TestApplyWarnsAboutUnsafeDaemonPath(t *testing.T) {
	writable := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(writable, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.Chmod(writable, 0o777); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	for _, args := range [][]string{{"apply", "--dry-run"}, {"apply"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			app := newDaemonTestApp(t)
			var stdout, stderr bytes.Buffer
			app.stdout = &stdout
			app.stderr = &stderr
			configPath := writeApplyFixture(t, app, `target = "daemon"`, writable+":/usr/bin:/bin:/usr/sbin:/sbin")
			if err := app.run(append(args, configPath)); err != nil {
				t.Fatalf("apply error = %v", err)
			}
			got := stderr.String()
			for _, want := range []string{"warning:", app.store.EnvFilePath(), writable + " (", "writable by other", "sudo summond env set PATH="} {
				if !strings.Contains(got, want) {
					t.Errorf("stderr = %q, want it to contain %q", got, want)
				}
			}
			for _, dir := range []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"} {
				if strings.Contains(got, "  "+dir+" (") {
					t.Errorf("stderr = %q, names root-owned %s", got, dir)
				}
			}
			if strings.Contains(stdout.String(), "warning") {
				t.Errorf("stdout = %q, want the warning on stderr only", stdout.String())
			}
		})
	}
}

func TestApplyDoesNotWarnForSafeOrAgentPath(t *testing.T) {
	writable := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(writable, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.Chmod(writable, 0o777); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	for name, tc := range map[string]struct {
		app       func(*testing.T) *App
		header    string
		pathValue string
	}{
		"daemon default": {newDaemonTestApp, `target = "daemon"`, daemonDefaultPath},
		"agent":          {newTestApp, "", writable + ":.:" + agentDefaultPath},
	} {
		t.Run(name, func(t *testing.T) {
			app := tc.app(t)
			var stderr bytes.Buffer
			app.stdout = &bytes.Buffer{}
			app.stderr = &stderr
			configPath := writeApplyFixture(t, app, tc.header, tc.pathValue)
			if err := app.run([]string{"apply", configPath}); err != nil {
				t.Fatalf("apply error = %v", err)
			}
			if got := stderr.String(); got != "" {
				t.Fatalf("stderr = %q, want no warning", got)
			}
		})
	}
}

func TestApplyKeepsExistingDaemonEnvFile(t *testing.T) {
	app := newDaemonTestApp(t)
	app.stdout = &bytes.Buffer{}
	app.stderr = &bytes.Buffer{}
	configPath := writeApplyFixture(t, app, `target = "daemon"`, agentDefaultPath)
	before, err := os.ReadFile(app.store.EnvFilePath())
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if err := app.run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	after, err := os.ReadFile(app.store.EnvFilePath())
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("env file = %q, want unchanged %q", after, before)
	}
}

// makeWithMode creates a directory (isDir) or an empty file at path and sets
// its mode, past the umask.
func makeWithMode(t *testing.T, path string, isDir bool, mode os.FileMode) {
	t.Helper()
	var err error
	if isDir {
		err = os.Mkdir(path, 0o700)
	} else {
		err = os.WriteFile(path, nil, 0o600)
	}
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
}

func TestUnsafeJobPaths(t *testing.T) {
	me := uint32(os.Getuid())
	dir := t.TempDir()
	safeDir := filepath.Join(dir, "safe")
	writableDir := filepath.Join(dir, "writable")
	makeWithMode(t, safeDir, true, 0o755)
	makeWithMode(t, writableDir, true, 0o757)
	safeCmd := filepath.Join(safeDir, "task")
	groupCmd := filepath.Join(safeDir, "group-task")
	cmdInWritableDir := filepath.Join(writableDir, "task")
	makeWithMode(t, safeCmd, false, 0o755)
	makeWithMode(t, groupCmd, false, 0o775)
	makeWithMode(t, cmdInWritableDir, false, 0o755)

	for name, tc := range map[string]struct {
		spec job.Spec
		want []string
	}{
		"safe":          {job.Spec{Command: safeCmd, WorkingDir: safeDir}, nil},
		"shell command": {job.Spec{ShellCommand: "jq . " + cmdInWritableDir, WorkingDir: safeDir}, nil},
		"missing":       {job.Spec{Command: filepath.Join(safeDir, "missing"), WorkingDir: filepath.Join(dir, "missing")}, nil},
		"bare name": {job.Spec{Command: "jq", WorkingDir: safeDir}, []string{
			"command jq (bare name, found through PATH at run time)",
		}},
		"group-writable command": {job.Spec{Command: groupCmd, WorkingDir: safeDir}, []string{
			"command " + groupCmd + " (writable by group)",
		}},
		"writable directories": {job.Spec{Command: cmdInWritableDir, WorkingDir: writableDir}, []string{
			"command directory " + writableDir + " (writable by other)",
			"working directory " + writableDir + " (writable by other)",
		}},
	} {
		t.Run(name, func(t *testing.T) {
			got := unsafeJobPaths(tc.spec, me)
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Fatalf("unsafeJobPaths() =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}

	got := unsafeJobPaths(job.Spec{Command: safeCmd, WorkingDir: safeDir}, me+1)
	want := []string{"command " + safeCmd, "command directory " + safeDir, "working directory " + safeDir}
	if len(got) != len(want) {
		t.Fatalf("unsafeJobPaths(other owner) = %q, want owner warnings for %q", got, want)
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]+" (owned by uid ") {
			t.Fatalf("unsafeJobPaths(other owner)[%d] = %q, want an owner warning for %s", i, got[i], want[i])
		}
	}
}

// userOwnedScript returns a script and its directory, both owned by the test
// user, who is not root.
func userOwnedScript(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "task.sh")
	makeWithMode(t, script, false, 0o755)
	return script, dir
}

func TestApplyWarnsAboutUnsafeDaemonJob(t *testing.T) {
	script, dir := userOwnedScript(t)
	for name, tc := range map[string]struct {
		command    string
		workingDir string
		want       []string
	}{
		"user-owned command": {script, "/", []string{
			"  command " + script + " (owned by uid ",
			"  command directory " + dir + " (owned by uid ",
		}},
		"bare name": {"echo", "/", []string{"  command echo (bare name, found through PATH at run time)"}},
		"user-owned working directory": {"/bin/echo", dir, []string{
			"  working directory " + dir + " (owned by uid ",
		}},
	} {
		for _, args := range [][]string{{"apply", "--dry-run"}, {"apply"}} {
			t.Run(name+"/"+strings.Join(args, " "), func(t *testing.T) {
				app := newDaemonTestApp(t)
				var stdout, stderr bytes.Buffer
				app.stdout = &stdout
				app.stderr = &stderr
				configPath := writeJobFixture(t, app, `target = "daemon"`, daemonDefaultPath, tc.command, tc.workingDir)
				// A nil error is exit code 0: the warning does not fail apply.
				if err := app.run(append(args, configPath)); err != nil {
					t.Fatalf("apply error = %v", err)
				}
				got := stderr.String()
				for _, want := range append(tc.want, `summond: warning: daemon job "cleanup" runs as root`, "root-owned") {
					if !strings.Contains(got, want) {
						t.Errorf("stderr = %q, want it to contain %q", got, want)
					}
				}
				if strings.Contains(stdout.String(), "warning") {
					t.Errorf("stdout = %q, want the warning on stderr only", stdout.String())
				}
			})
		}
	}
}

func TestApplyDoesNotWarnAboutAgentJob(t *testing.T) {
	script, dir := userOwnedScript(t)
	app := newTestApp(t)
	var stderr bytes.Buffer
	app.stdout = &bytes.Buffer{}
	app.stderr = &stderr
	configPath := writeJobFixture(t, app, "", agentDefaultPath, script, dir)
	if err := app.run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr = %q, want no warning", got)
	}
}

func TestAddWarnsAboutUnsafeDaemonJob(t *testing.T) {
	script, dir := userOwnedScript(t)
	for name, tc := range map[string]struct {
		app  func(*testing.T) *App
		args []string
		want string
	}{
		"daemon":            {newDaemonTestApp, []string{"--schedule", "boot", "--working-dir", dir, "--", script}, "  command " + script + " (owned by uid "},
		"daemon dry run":    {newDaemonTestApp, []string{"--dry-run", "--schedule", "boot", "--working-dir", dir, "--", script}, "  working directory " + dir + " (owned by uid "},
		"daemon bare name":  {newDaemonTestApp, []string{"--schedule", "boot", "--working-dir", "/", "--", "echo"}, "  command echo (bare name"},
		"daemon root-owned": {newDaemonTestApp, []string{"--schedule", "boot", "--working-dir", "/", "--", "/bin/echo"}, ""},
		"agent":             {newTestApp, []string{"--schedule", "daily", "--working-dir", dir, "--", script}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			app := tc.app(t)
			var stderr bytes.Buffer
			app.stdout = &bytes.Buffer{}
			app.stderr = &stderr
			if err := app.run(append([]string{"add", "task"}, tc.args...)); err != nil {
				t.Fatalf("add error = %v", err)
			}
			got := stderr.String()
			if tc.want == "" {
				if got != "" {
					t.Fatalf("stderr = %q, want no warning", got)
				}
				return
			}
			for _, want := range []string{`summond: warning: daemon job "task" runs as root`, tc.want} {
				if !strings.Contains(got, want) {
					t.Errorf("stderr = %q, want it to contain %q", got, want)
				}
			}
		})
	}
}

// A job whose env sources set no PATH must still run with an explicit PATH,
// the scope's default, so bash never falls back to its built-in PATH.
func TestJobUsesScopeDefaultPathWhenUnset(t *testing.T) {
	for name, tc := range map[string]struct {
		app  func(*testing.T) *App
		want string
	}{
		"agent":  {newTestApp, agentDefaultPath},
		"daemon": {newDaemonTestApp, daemonDefaultPath},
	} {
		t.Run(name, func(t *testing.T) {
			for envName, envPath := range map[string]string{
				"no PATH in env file": writeEnvFile(t, `{"TOKEN": "x"}`),
				"no env file":         filepath.Join(t.TempDir(), "missing.json"),
			} {
				t.Run(envName, func(t *testing.T) {
					app := tc.app(t)
					var stdout bytes.Buffer
					app.stdout = &stdout
					exitCode, _, err := app.executeSpec(job.Spec{
						Name:                "path",
						ShellCommand:        `printf '%s' "$PATH"`,
						EnvironmentFilePath: envPath,
					}, nil, 0, nil)
					if err != nil || exitCode != 0 {
						t.Fatalf("executeSpec() = %d, %v", exitCode, err)
					}
					if got := stdout.String(); got != tc.want {
						t.Fatalf("PATH = %q, want %q", got, tc.want)
					}
				})
			}
		})
	}
}
