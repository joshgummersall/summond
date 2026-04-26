package cli

import (
	"bytes"
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
