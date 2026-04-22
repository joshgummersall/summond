package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "summond.toml")
	data := []byte(`
[jobs.backup]
command = "/bin/echo"
args = ["backup", "now"]
target = "agent"
schedule = "weekly"
weekday = 2
hour = 1
minute = 30
enabled = true

[jobs.backup.env]
MODE = "fast"
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	specs, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("len(specs) = %d", len(specs))
	}
	if got, want := specs[0].Environment["MODE"], "fast"; got != want {
		t.Fatalf("MODE = %q, want %q", got, want)
	}
}
