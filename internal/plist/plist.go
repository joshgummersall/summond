package plist

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"sort"

	"github.com/joshgummersall/summond/internal/job"
)

const checksumKey = "SummondSpecChecksum"
const shellPreamble = "set -euo pipefail\n"

func Render(spec job.Spec) ([]byte, error) {
	if err := spec.Normalize(); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	buf.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	buf.WriteString(`<plist version="1.0">` + "\n")
	buf.WriteString("<dict>\n")
	writeString(&buf, "Label", spec.Label)
	if spec.Checksum != "" {
		writeString(&buf, checksumKey, spec.Checksum)
	}
	if spec.Target == job.TargetAgent {
		writeString(&buf, "LimitLoadToSessionType", "Aqua")
	}
	writeBool(&buf, "RunAtLoad", runAtLoad(spec.Schedule))
	if spec.WorkingDir != "" {
		writeString(&buf, "WorkingDirectory", spec.WorkingDir)
	}
	writeProgram(&buf, spec)
	if len(spec.Environment) > 0 {
		buf.WriteString("  <key>EnvironmentVariables</key>\n")
		buf.WriteString("  <dict>\n")
		keys := make([]string, 0, len(spec.Environment))
		for key := range spec.Environment {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			writeStringIndented(&buf, 2, key, spec.Environment[key])
		}
		buf.WriteString("  </dict>\n")
	}
	writeSchedule(&buf, spec.Schedule)
	if len(spec.WatchPaths) > 0 {
		writeStringArray(&buf, "WatchPaths", spec.WatchPaths)
	}
	if spec.ThrottleIntervalSeconds > 0 {
		writeInteger(&buf, "ThrottleInterval", spec.ThrottleIntervalSeconds)
	}
	if spec.StdoutPath != "" {
		writeString(&buf, "StandardOutPath", spec.StdoutPath)
	}
	if spec.StderrPath != "" {
		writeString(&buf, "StandardErrorPath", spec.StderrPath)
	}
	if !spec.Enabled {
		writeBool(&buf, "Disabled", true)
	}
	buf.WriteString("</dict>\n")
	buf.WriteString("</plist>\n")
	return buf.Bytes(), nil
}

func runAtLoad(schedule job.Schedule) bool {
	return schedule.Kind == job.ScheduleLogin || schedule.Kind == job.ScheduleBoot
}

func writeProgram(buf *bytes.Buffer, spec job.Spec) {
	buf.WriteString("  <key>ProgramArguments</key>\n")
	buf.WriteString("  <array>\n")
	if spec.ShellCommand != "" {
		writeArrayString(buf, "/bin/bash")
		writeArrayString(buf, "-lc")
		writeArrayString(buf, shellPreamble+spec.ShellCommand)
	} else {
		writeArrayString(buf, spec.Command)
		for _, arg := range spec.Args {
			writeArrayString(buf, arg)
		}
	}
	buf.WriteString("  </array>\n")
}

func writeSchedule(buf *bytes.Buffer, schedule job.Schedule) {
	switch schedule.Kind {
	case job.ScheduleHourly:
		writeCalendarInterval(buf, map[string]int{
			"Minute": schedule.Minute,
		})
	case job.ScheduleDaily:
		writeCalendarInterval(buf, map[string]int{
			"Hour":   schedule.Hour,
			"Minute": schedule.Minute,
		})
	case job.ScheduleWeekly:
		writeCalendarInterval(buf, map[string]int{
			"Weekday": schedule.Weekday,
			"Hour":    schedule.Hour,
			"Minute":  schedule.Minute,
		})
	case job.ScheduleInterval:
		writeInteger(buf, "StartInterval", schedule.IntervalMinutes*60)
	case job.ScheduleCalendar:
		fields := map[string]int{}
		if schedule.Month > 0 {
			fields["Month"] = schedule.Month
		}
		if schedule.Day > 0 {
			fields["Day"] = schedule.Day
		}
		if schedule.Weekday > 0 {
			fields["Weekday"] = schedule.Weekday
		}
		if schedule.Hour > 0 || schedule.Minute > 0 {
			fields["Hour"] = schedule.Hour
			fields["Minute"] = schedule.Minute
		}
		writeCalendarInterval(buf, fields)
	}
}

func writeCalendarInterval(buf *bytes.Buffer, fields map[string]int) {
	buf.WriteString("  <key>StartCalendarInterval</key>\n")
	buf.WriteString("  <dict>\n")
	keys := make([]string, 0, len(fields))
	for key, value := range fields {
		if value == 0 && key != "Hour" && key != "Minute" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		writeIntegerIndented(buf, 2, key, fields[key])
	}
	buf.WriteString("  </dict>\n")
}

func writeString(buf *bytes.Buffer, key, value string) {
	writeStringIndented(buf, 1, key, value)
}

func writeStringIndented(buf *bytes.Buffer, indent int, key, value string) {
	padding := indentString(indent)
	buf.WriteString(fmt.Sprintf("%s<key>%s</key>\n", padding, xmlEscape(key)))
	buf.WriteString(fmt.Sprintf("%s<string>%s</string>\n", padding, xmlEscape(value)))
}

func writeInteger(buf *bytes.Buffer, key string, value int) {
	writeIntegerIndented(buf, 1, key, value)
}

func writeIntegerIndented(buf *bytes.Buffer, indent int, key string, value int) {
	padding := indentString(indent)
	buf.WriteString(fmt.Sprintf("%s<key>%s</key>\n", padding, xmlEscape(key)))
	buf.WriteString(fmt.Sprintf("%s<integer>%d</integer>\n", padding, value))
}

func writeBool(buf *bytes.Buffer, key string, value bool) {
	buf.WriteString(fmt.Sprintf("  <key>%s</key>\n", xmlEscape(key)))
	if value {
		buf.WriteString("  <true/>\n")
		return
	}
	buf.WriteString("  <false/>\n")
}

func writeArrayString(buf *bytes.Buffer, value string) {
	buf.WriteString(fmt.Sprintf("    <string>%s</string>\n", xmlEscape(value)))
}

func writeStringArray(buf *bytes.Buffer, key string, values []string) {
	buf.WriteString(fmt.Sprintf("  <key>%s</key>\n", xmlEscape(key)))
	buf.WriteString("  <array>\n")
	for _, value := range values {
		writeArrayString(buf, value)
	}
	buf.WriteString("  </array>\n")
}

func indentString(indent int) string {
	return string(bytes.Repeat([]byte("  "), indent))
}

func xmlEscape(s string) string {
	var buf bytes.Buffer
	if err := xml.EscapeText(&buf, []byte(s)); err != nil {
		return s
	}
	return buf.String()
}
