package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListOutputsTSV(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	app.stdout = &stdout

	configPath := filepath.Join(testHome(t), "summond.toml")
	data := strings.Join([]string{
		"[jobs.cleanup]",
		`command = "/bin/echo"`,
		`args = ["clean"]`,
		`target = "agent"`,
		`schedule = "daily"`,
		"hour = 3",
		"minute = 45",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := app.run([]string{"apply", configPath}); err != nil {
		t.Fatalf("apply error = %v", err)
	}

	stdout.Reset()
	if err := app.run([]string{"list"}); err != nil {
		t.Fatalf("list error = %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "NAME") || !strings.Contains(got, "TARGET") || !strings.Contains(got, "SCHEDULE") || !strings.Contains(got, "STATUS") {
		t.Fatalf("stdout missing headers: %q", got)
	}
	if !strings.Contains(got, "cleanup") || !strings.Contains(got, "agent") || !strings.Contains(got, "daily at 03:45") || !strings.Contains(got, "never") {
		t.Fatalf("stdout = %q", got)
	}
	if strings.Contains(got, "\t") {
		t.Fatalf("stdout still contains raw tabs: %q", got)
	}
}
