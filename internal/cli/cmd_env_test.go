package cli

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestRunEnvSetGetList(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout

	if err := app.run([]string{"env", "set", "MY_KEY=hello"}); err != nil {
		t.Fatalf("env set error = %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "set MY_KEY" {
		t.Fatalf("stdout = %q", got)
	}

	stdout.Reset()
	if err := app.run([]string{"env", "get", "MY_KEY"}); err != nil {
		t.Fatalf("env get error = %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "hello" {
		t.Fatalf("stdout = %q", got)
	}

	stdout.Reset()
	if err := app.run([]string{"env", "set", "MY_KEY=world"}); err != nil {
		t.Fatalf("env set update error = %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "updated MY_KEY" {
		t.Fatalf("stdout = %q", got)
	}

	stdout.Reset()
	if err := app.run([]string{"env", "list"}); err != nil {
		t.Fatalf("env list error = %v", err)
	}
	if !strings.Contains(stdout.String(), "MY_KEY=world") {
		t.Fatalf("list output = %q", stdout.String())
	}

	if err := app.run([]string{"env", "get", "DOES_NOT_EXIST"}); err == nil {
		t.Fatal("expected error for missing key")
	}

	data, err := os.ReadFile(app.store.EnvFilePath())
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	env, err := readEnvJSON(data)
	if err != nil {
		t.Fatalf("readEnvJSON() error = %v", err)
	}
	if env["MY_KEY"] != "world" {
		t.Fatalf("MY_KEY = %q", env["MY_KEY"])
	}
}

func TestEnvWritesAreMode0600(t *testing.T) {
	app := newTestApp(t)
	path := app.store.EnvFilePath()
	assertMode := func(step string) {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("%s: Stat() error = %v", step, err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("%s: mode = %o, want 600", step, mode)
		}
	}

	if err := app.run([]string{"env", "list"}); err != nil {
		t.Fatalf("env list error = %v", err)
	}
	assertMode("first write")

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	if err := app.run([]string{"env", "set", "MY_KEY=hello"}); err != nil {
		t.Fatalf("env set error = %v", err)
	}
	assertMode("set")

	if err := app.run([]string{"env", "remove", "MY_KEY"}); err != nil {
		t.Fatalf("env remove error = %v", err)
	}
	assertMode("remove")
}

func TestEnvRejectsDaemonFlag(t *testing.T) {
	app := newTestApp(t)
	err := app.run([]string{"env", "set", "--daemon", "MY_KEY=hello"})
	if err == nil || !strings.Contains(err.Error(), "unknown flag: --daemon") {
		t.Fatalf("env set error = %v", err)
	}
}

func readTestEnv(t *testing.T, app *App) map[string]string {
	t.Helper()
	data, err := os.ReadFile(app.store.EnvFilePath())
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	env, err := readEnvJSON(data)
	if err != nil {
		t.Fatalf("readEnvJSON() error = %v", err)
	}
	return env
}

func TestEnvSetReadsValueFromStdin(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "no newline", input: "v", want: "v"},
		{name: "one trailing newline stripped", input: "secret\n", want: "secret"},
		{name: "only one trailing newline stripped", input: "secret\n\n", want: "secret\n"},
		{name: "trailing CRLF stripped", input: "secret\r\n", want: "secret"},
		{name: "inner newlines kept", input: "line1\nline2\n", want: "line1\nline2"},
		{name: "surrounding spaces kept", input: " s p \n", want: " s p "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := newTestApp(t)
			var stdout, stderr bytes.Buffer
			app.stdout = &stdout
			app.stderr = &stderr
			app.stdin = strings.NewReader(tt.input)

			if err := app.run([]string{"env", "set", "MY_KEY"}); err != nil {
				t.Fatalf("env set error = %v", err)
			}
			if got := strings.TrimSpace(stdout.String()); got != "set MY_KEY" {
				t.Fatalf("stdout = %q", got)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want no prompt for piped stdin", stderr.String())
			}
			if got := readTestEnv(t, app)["MY_KEY"]; got != tt.want {
				t.Fatalf("MY_KEY = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEnvSetRejectsEmptyStdinValue(t *testing.T) {
	for _, input := range []string{"", "\n", "\r\n"} {
		app := newTestApp(t)
		if err := app.run([]string{"env", "set", "MY_KEY=old"}); err != nil {
			t.Fatalf("env set error = %v", err)
		}
		before, err := os.ReadFile(app.store.EnvFilePath())
		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}

		app.stdin = strings.NewReader(input)
		err = app.run([]string{"env", "set", "MY_KEY"})
		if err == nil || !strings.Contains(err.Error(), "empty") {
			t.Fatalf("input %q: env set error = %v, want empty-value error", input, err)
		}
		after, err := os.ReadFile(app.store.EnvFilePath())
		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("input %q: env file changed:\n%s\nto\n%s", input, before, after)
		}
	}
}

// trackingReader records whether anything read from it.
type trackingReader struct{ read bool }

func (r *trackingReader) Read([]byte) (int, error) {
	r.read = true
	return 0, io.EOF
}

func TestEnvSetValidatesKeyBeforeReadingStdin(t *testing.T) {
	for _, arg := range []string{"1BAD", "BAD-KEY", ""} {
		app := newTestApp(t)
		stdin := &trackingReader{}
		app.stdin = stdin
		err := app.run([]string{"env", "set", arg})
		if err == nil || !strings.Contains(err.Error(), "key") {
			t.Fatalf("env set %q: error = %v, want key error", arg, err)
		}
		if stdin.read {
			t.Fatalf("env set %q: stdin was read before the key was validated", arg)
		}
		if _, err := os.Stat(app.store.EnvFilePath()); err == nil {
			t.Fatalf("env set %q: env file was written", arg)
		}
	}
}

func TestEnvSetExplicitEmptyValue(t *testing.T) {
	app := newTestApp(t)
	stdin := &trackingReader{}
	app.stdin = stdin
	if err := app.run([]string{"env", "set", "MY_KEY="}); err != nil {
		t.Fatalf("env set error = %v", err)
	}
	if stdin.read {
		t.Fatal("stdin was read for KEY=")
	}
	if got, ok := readTestEnv(t, app)["MY_KEY"]; !ok || got != "" {
		t.Fatalf("MY_KEY = %q (present %v), want empty", got, ok)
	}
}

func TestEnvSetKeyValueIgnoresStdin(t *testing.T) {
	app := newTestApp(t)
	stdin := &trackingReader{}
	app.stdin = stdin
	if err := app.run([]string{"env", "set", "MY_KEY=a=b"}); err != nil {
		t.Fatalf("env set error = %v", err)
	}
	if stdin.read {
		t.Fatal("stdin was read for KEY=VALUE")
	}
	if got := readTestEnv(t, app)["MY_KEY"]; got != "a=b" {
		t.Fatalf("MY_KEY = %q", got)
	}
}

func TestEnvSetFromStdinIsMode0600(t *testing.T) {
	app := newTestApp(t)
	if err := app.run([]string{"env", "list"}); err != nil {
		t.Fatalf("env list error = %v", err)
	}
	path := app.store.EnvFilePath()
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	app.stdin = strings.NewReader("secret\n")
	if err := app.run([]string{"env", "set", "MY_KEY"}); err != nil {
		t.Fatalf("env set error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode = %o, want 600", mode)
	}
}
