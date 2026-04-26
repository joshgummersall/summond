package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/joshgummersall/summond/internal/job"
)

func TestLogsTailArgsDefault(t *testing.T) {
	got := logsTailArgs(40, false, "/tmp/job.log")
	want := []string{"-n", "40", "/tmp/job.log"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("logsTailArgs() = %#v, want %#v", got, want)
	}
}

func TestLogsTailArgsFollow(t *testing.T) {
	got := logsTailArgs(12, true, "/tmp/job.log")
	want := []string{"-n", "12", "-f", "/tmp/job.log"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("logsTailArgs() = %#v, want %#v", got, want)
	}
}

func TestLogsRejectsNegativeLineCount(t *testing.T) {
	app := newTestApp(t)

	err := app.run([]string{"logs", "-n", "-1", "cleanup"})
	if err == nil || err.Error() != "logs requires -n >= 0" {
		t.Fatalf("logs error = %v", err)
	}
}

func TestLogsWritesStdoutAndStderrSeparately(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app.stdout = &stdout
	app.stderr = &stderr

	spec, err := app.store.Install(job.Spec{
		Name:     "cleanup",
		Command:  "/bin/echo",
		Schedule: job.Schedule{Kind: job.ScheduleDaily, Hour: 3, HourSet: true, Minute: 45, MinuteSet: true},
	})
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if err := os.WriteFile(spec.StdoutPath, []byte("out-1\nout-2\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(stdout) error = %v", err)
	}
	if err := os.WriteFile(spec.StderrPath, []byte("err-1\nerr-2\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(stderr) error = %v", err)
	}

	if err := app.run([]string{"logs", "-n", "1", "cleanup"}); err != nil {
		t.Fatalf("logs error = %v", err)
	}
	if got := stdout.String(); got != "out-2\n" {
		t.Fatalf("stdout = %q", got)
	}
	if got := stderr.String(); got != "err-2\n" {
		t.Fatalf("stderr = %q", got)
	}
}

func TestLogsDefaultShowsLastRunOnly(t *testing.T) {
	app := newTestApp(t)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app.stdout = &stdout
	app.stderr = &stderr

	spec, err := app.store.Install(job.Spec{
		Name:     "cleanup",
		Command:  "/bin/echo",
		Schedule: job.Schedule{Kind: job.ScheduleDaily, Hour: 3, HourSet: true, Minute: 45, MinuteSet: true},
	})
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}

	if err := os.WriteFile(spec.StdoutPath, []byte("prev-out\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(stdout) error = %v", err)
	}
	if err := os.WriteFile(spec.StderrPath, []byte("prev-err\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(stderr) error = %v", err)
	}

	if err := app.store.RecordExecutionStart(spec.Name, time.Now()); err != nil {
		t.Fatalf("RecordExecutionStart() error = %v", err)
	}

	stdoutFile, err := os.OpenFile(spec.StdoutPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile(stdout) error = %v", err)
	}
	if _, err := stdoutFile.WriteString("new-out\n"); err != nil {
		t.Fatalf("Write(stdout) error = %v", err)
	}
	stdoutFile.Close()

	stderrFile, err := os.OpenFile(spec.StderrPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile(stderr) error = %v", err)
	}
	if _, err := stderrFile.WriteString("new-err\n"); err != nil {
		t.Fatalf("Write(stderr) error = %v", err)
	}
	stderrFile.Close()

	if err := app.run([]string{"logs", "cleanup"}); err != nil {
		t.Fatalf("logs error = %v", err)
	}
	if got := stdout.String(); got != "new-out\n" {
		t.Fatalf("stdout = %q, want %q", got, "new-out\n")
	}
	if got := stderr.String(); got != "new-err\n" {
		t.Fatalf("stderr = %q, want %q", got, "new-err\n")
	}
}
