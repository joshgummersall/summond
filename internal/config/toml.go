package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joshgummersall/summond/internal/job"
)

func LoadFile(path string) ([]job.Spec, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var specs []job.Spec
	index := map[string]int{}
	section := []string{}
	scanner := bufio.NewScanner(file)
	for lineNum := 1; scanner.Scan(); lineNum++ {
		line := stripComment(scanner.Text())
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = parseSection(line[1 : len(line)-1])
			if len(section) < 2 || section[0] != "jobs" {
				return nil, fmt.Errorf("line %d: unsupported section [%s]", lineNum, strings.Join(section, "."))
			}
			name := section[1]
			if _, ok := index[name]; !ok {
				index[name] = len(specs)
				specs = append(specs, job.Spec{Name: name, Enabled: true})
			}
			continue
		}

		if len(section) < 2 {
			return nil, fmt.Errorf("line %d: key outside [jobs.<name>] section", lineNum)
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("line %d: expected key = value", lineNum)
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		spec := &specs[index[section[1]]]
		if len(section) == 3 && section[2] == "env" {
			if spec.Environment == nil {
				spec.Environment = map[string]string{}
			}
			parsed, err := parseString(value)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNum, err)
			}
			spec.Environment[key] = parsed
			continue
		}
		if err := applyField(spec, key, value); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNum, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	baseDir := filepath.Dir(absPath)
	for i := range specs {
		resolvePaths(&specs[i], baseDir)
		if err := specs[i].Normalize(); err != nil {
			return nil, fmt.Errorf("job %s: %w", specs[i].Name, err)
		}
	}
	return specs, nil
}

func parseSection(value string) []string {
	parts := strings.Split(value, ".")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func applyField(spec *job.Spec, key, raw string) error {
	switch key {
	case "label":
		value, err := parseString(raw)
		if err != nil {
			return err
		}
		spec.Label = value
	case "target":
		value, err := parseString(raw)
		if err != nil {
			return err
		}
		spec.Target = job.Target(value)
	case "command":
		value, err := parseString(raw)
		if err != nil {
			return err
		}
		spec.Command = value
	case "shell_command":
		value, err := parseString(raw)
		if err != nil {
			return err
		}
		spec.ShellCommand = value
	case "working_dir":
		value, err := parseString(raw)
		if err != nil {
			return err
		}
		spec.WorkingDir = value
	case "schedule":
		value, err := parseString(raw)
		if err != nil {
			return err
		}
		spec.Schedule.Kind = job.ScheduleKind(value)
	case "trigger":
		value, err := parseString(raw)
		if err != nil {
			return err
		}
		spec.Trigger = job.TriggerKind(value)
	case "args":
		value, err := parseStringArray(raw)
		if err != nil {
			return err
		}
		spec.Args = value
	case "enabled":
		value, err := parseBool(raw)
		if err != nil {
			return err
		}
		spec.Enabled = value
	case "interval_minutes":
		value, err := parseInt(raw)
		if err != nil {
			return err
		}
		spec.Schedule.IntervalMinutes = value
	case "minute":
		value, err := parseInt(raw)
		if err != nil {
			return err
		}
		spec.Schedule.Minute = value
	case "hour":
		value, err := parseInt(raw)
		if err != nil {
			return err
		}
		spec.Schedule.Hour = value
	case "weekday":
		value, err := parseInt(raw)
		if err != nil {
			return err
		}
		spec.Schedule.Weekday = value
	case "day":
		value, err := parseInt(raw)
		if err != nil {
			return err
		}
		spec.Schedule.Day = value
	case "month":
		value, err := parseInt(raw)
		if err != nil {
			return err
		}
		spec.Schedule.Month = value
	case "stdout_path":
		value, err := parseString(raw)
		if err != nil {
			return err
		}
		spec.StdoutPath = value
	case "stderr_path":
		value, err := parseString(raw)
		if err != nil {
			return err
		}
		spec.StderrPath = value
	case "watch_paths":
		value, err := parseStringArray(raw)
		if err != nil {
			return err
		}
		spec.WatchPaths = value
	case "throttle_interval_seconds":
		value, err := parseInt(raw)
		if err != nil {
			return err
		}
		spec.ThrottleIntervalSeconds = value
	default:
		return fmt.Errorf("unsupported key %q", key)
	}
	return nil
}

func resolvePaths(spec *job.Spec, baseDir string) {
	if spec.WorkingDir == "" {
		spec.WorkingDir = filepath.Clean(baseDir)
	}
	for i, path := range spec.WatchPaths {
		if filepath.IsAbs(path) {
			spec.WatchPaths[i] = filepath.Clean(path)
			continue
		}
		spec.WatchPaths[i] = filepath.Clean(filepath.Join(baseDir, path))
	}
}

func parseString(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return "", fmt.Errorf("expected quoted string, got %q", value)
	}
	parsed, err := strconv.Unquote(value)
	if err != nil {
		return "", fmt.Errorf("parse string: %w", err)
	}
	return parsed, nil
}

func parseStringArray(value string) ([]string, error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "[") || !strings.HasSuffix(value, "]") {
		return nil, fmt.Errorf("expected array, got %q", value)
	}
	body := strings.TrimSpace(value[1 : len(value)-1])
	if body == "" {
		return nil, nil
	}
	parts := splitCSV(body)
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		item, err := parseString(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func parseBool(value string) (bool, error) {
	switch strings.TrimSpace(value) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("expected true or false, got %q", value)
	}
}

func parseInt(value string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("expected integer, got %q", value)
	}
	return parsed, nil
}

func splitCSV(value string) []string {
	var (
		parts    []string
		start    int
		inString bool
		escaped  bool
	)
	for i, r := range value {
		switch {
		case escaped:
			escaped = false
		case r == '\\':
			escaped = true
		case r == '"':
			inString = !inString
		case r == ',' && !inString:
			parts = append(parts, strings.TrimSpace(value[start:i]))
			start = i + 1
		}
	}
	parts = append(parts, strings.TrimSpace(value[start:]))
	return parts
}

func stripComment(value string) string {
	var (
		builder  strings.Builder
		inString bool
		escaped  bool
	)
	for _, r := range value {
		switch {
		case escaped:
			builder.WriteRune(r)
			escaped = false
		case r == '\\':
			builder.WriteRune(r)
			escaped = true
		case r == '"':
			builder.WriteRune(r)
			inString = !inString
		case r == '#' && !inString:
			return builder.String()
		default:
			builder.WriteRune(r)
		}
	}
	return builder.String()
}
