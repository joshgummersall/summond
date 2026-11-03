package cli

import (
	"os"
	"testing"
)

func TestRefreshRuntimeWithoutStateIsNoop(t *testing.T) {
	app := newRawTestApp(t)
	if err := app.run([]string{"refresh-runtime"}); err != nil {
		t.Fatalf("refresh-runtime error = %v", err)
	}
	if _, err := os.Stat(app.store.Paths().Home); !os.IsNotExist(err) {
		t.Fatalf("expected no state directory, stat err = %v", err)
	}
}

func TestRefreshRuntimeReplacesRuntimeCopy(t *testing.T) {
	for _, tc := range []struct {
		name string
		app  func(*testing.T) *App
	}{
		{"agent", newTestApp},
		{"daemon", newDaemonTestApp},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := tc.app(t)
			runtimePath := app.store.RuntimeBinaryPath()
			if err := os.MkdirAll(app.store.Paths().Home, 0o755); err != nil {
				t.Fatalf("MkdirAll() error = %v", err)
			}
			if err := app.run([]string{"refresh-runtime"}); err != nil {
				t.Fatalf("refresh-runtime error = %v", err)
			}
			got, err := os.ReadFile(runtimePath)
			if err != nil {
				t.Fatalf("ReadFile(runtime) error = %v", err)
			}
			self, err := os.Executable()
			if err != nil {
				t.Fatalf("Executable() error = %v", err)
			}
			want, err := os.ReadFile(self)
			if err != nil {
				t.Fatalf("ReadFile(self) error = %v", err)
			}
			if string(got) != string(want) {
				t.Fatalf("runtime copy does not match current executable")
			}
		})
	}
}
