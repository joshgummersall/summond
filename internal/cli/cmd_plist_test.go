package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlistPrintsJobPlist(t *testing.T) {
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
	if err := app.run([]string{"plist", "cleanup"}); err != nil {
		t.Fatalf("plist error = %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "<plist") || !strings.Contains(got, "<key>Label</key>") || !strings.Contains(got, "<string>com.joshgummersall.summond.") || !strings.Contains(got, "<key>ProgramArguments</key>") {
		t.Fatalf("stdout = %q", got)
	}
}
