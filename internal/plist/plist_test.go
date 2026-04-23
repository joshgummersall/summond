package plist

import (
	"strings"
	"testing"

	"github.com/joshgummersall/summond/internal/job"
)

func TestRenderIntervalPlist(t *testing.T) {
	data, err := Render(job.Spec{
		Name:     "sync",
		Target:   job.TargetAgent,
		Command:  "/bin/echo",
		Args:     []string{"hi"},
		Checksum: "checksum-123",
		Schedule: job.Schedule{
			Kind:            job.ScheduleInterval,
			IntervalMinutes: 30,
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	text := string(data)
	for _, needle := range []string{
		"<key>SummondSpecChecksum</key>",
		"<string>checksum-123</string>",
		"<key>StartInterval</key>",
		"<integer>1800</integer>",
		"<string>/bin/echo</string>",
		"<string>hi</string>",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("plist missing %q: %s", needle, text)
		}
	}
}
