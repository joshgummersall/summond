package cli

import (
	"bytes"
	"testing"
)

func TestRunHello(t *testing.T) {
	var stdout bytes.Buffer

	if err := Run([]string{"hello", "tester"}, &stdout); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := stdout.String(), "hello, tester\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestRunVersion(t *testing.T) {
	var stdout bytes.Buffer

	if err := Run([]string{"version"}, &stdout); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := stdout.String(), "summond 0.1.0\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}
