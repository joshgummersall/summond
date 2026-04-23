package job

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Target string

const (
	TargetAgent  Target = "agent"
	TargetDaemon Target = "daemon"
)

type ScheduleKind string

const (
	ScheduleHourly   ScheduleKind = "hourly"
	ScheduleDaily    ScheduleKind = "daily"
	ScheduleWeekly   ScheduleKind = "weekly"
	ScheduleLogin    ScheduleKind = "login"
	ScheduleBoot     ScheduleKind = "boot"
	ScheduleInterval ScheduleKind = "interval"
	ScheduleCalendar ScheduleKind = "calendar"
)

type TriggerKind string

const (
	TriggerOnChange TriggerKind = "on_change"
)

type Schedule struct {
	Kind            ScheduleKind `json:"kind"`
	IntervalMinutes int          `json:"interval_minutes,omitempty"`
	Minute          int          `json:"minute,omitempty"`
	Hour            int          `json:"hour,omitempty"`
	Weekday         int          `json:"weekday,omitempty"`
	Day             int          `json:"day,omitempty"`
	Month           int          `json:"month,omitempty"`
}

type Spec struct {
	Name         string            `json:"name"`
	Label        string            `json:"label"`
	Target       Target            `json:"target"`
	Command      string            `json:"command,omitempty"`
	Args         []string          `json:"args,omitempty"`
	ShellCommand string            `json:"shell_command,omitempty"`
	WorkingDir   string            `json:"working_dir,omitempty"`
	Environment  map[string]string `json:"environment,omitempty"`
	Schedule     Schedule          `json:"schedule"`
	Trigger      TriggerKind       `json:"trigger,omitempty"`
	WatchPaths   []string          `json:"watch_paths,omitempty"`
	Enabled      bool              `json:"enabled"`
	StdoutPath   string            `json:"stdout_path,omitempty"`
	StderrPath   string            `json:"stderr_path,omitempty"`
	PlistPath    string            `json:"plist_path,omitempty"`
	Checksum     string            `json:"checksum,omitempty"`
}

var invalidNameChars = regexp.MustCompile(`[^a-z0-9.-]+`)

func (s *Spec) Normalize() error {
	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" {
		return errors.New("job name is required")
	}
	if s.Label == "" {
		s.Label = DefaultLabel(s.Name)
	}
	if s.Target == "" {
		s.Target = TargetAgent
	}
	if s.Target != TargetAgent && s.Target != TargetDaemon {
		return fmt.Errorf("invalid target %q", s.Target)
	}
	if s.Command == "" && s.ShellCommand == "" {
		return errors.New("either command or shell command is required")
	}
	if s.Command != "" && s.ShellCommand != "" {
		return errors.New("command and shell command are mutually exclusive")
	}
	if s.Command != "" && !filepath.IsAbs(s.Command) {
		return fmt.Errorf("command must be an absolute path: %q", s.Command)
	}
	if s.WorkingDir != "" && !filepath.IsAbs(s.WorkingDir) {
		return fmt.Errorf("working directory must be an absolute path: %q", s.WorkingDir)
	}
	if s.Environment == nil {
		s.Environment = map[string]string{}
	}
	if err := s.normalizeTrigger(); err != nil {
		return err
	}
	if s.Trigger == "" {
		if err := s.Schedule.NormalizeForTarget(s.Target); err != nil {
			return err
		}
	} else if s.Schedule.Kind != "" {
		return errors.New("schedule and trigger are mutually exclusive")
	}
	for _, path := range s.WatchPaths {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("watch path must be an absolute path: %q", path)
		}
	}
	if s.Trigger == "" && len(s.WatchPaths) > 0 {
		return errors.New("watch_paths requires a trigger")
	}
	if s.Trigger != "" && len(s.WatchPaths) == 0 {
		return errors.New("trigger requires watch_paths")
	}
	if s.Trigger == "" && s.Schedule.Kind == "" {
		return errors.New("either schedule or trigger is required")
	}
	return nil
}

func (s *Spec) normalizeTrigger() error {
	switch s.Trigger {
	case "":
		return nil
	case TriggerOnChange:
		return nil
	default:
		return fmt.Errorf("invalid trigger %q", s.Trigger)
	}
}

func DefaultLabel(name string) string {
	safe := strings.ToLower(strings.TrimSpace(name))
	safe = invalidNameChars.ReplaceAllString(safe, "-")
	safe = strings.Trim(safe, "-.")
	if safe == "" {
		safe = "job"
	}
	return "com.joshgummersall.summond." + safe
}

func (s Schedule) NormalizeForTarget(target Target) error {
	switch s.Kind {
	case ScheduleHourly:
		if s.Hour != 0 || s.Weekday != 0 || s.Day != 0 || s.Month != 0 || s.IntervalMinutes != 0 {
			return errors.New("hourly schedule only supports minute")
		}
		if s.Minute < 0 || s.Minute > 59 {
			return fmt.Errorf("minute must be between 0 and 59, got %d", s.Minute)
		}
	case ScheduleDaily:
		if s.Weekday != 0 || s.Day != 0 || s.Month != 0 || s.IntervalMinutes != 0 {
			return errors.New("daily schedule only supports hour and minute")
		}
		if err := validateClock(s.Hour, s.Minute); err != nil {
			return err
		}
	case ScheduleWeekly:
		if s.Day != 0 || s.Month != 0 || s.IntervalMinutes != 0 {
			return errors.New("weekly schedule only supports weekday, hour, and minute")
		}
		if err := validateClock(s.Hour, s.Minute); err != nil {
			return err
		}
		if s.Weekday < 0 || s.Weekday > 7 {
			return fmt.Errorf("weekday must be between 0 and 7, got %d", s.Weekday)
		}
	case ScheduleLogin:
		if target != TargetAgent {
			return errors.New("login schedule is only valid for agent jobs")
		}
	case ScheduleBoot:
		if target != TargetDaemon {
			return errors.New("boot schedule is only valid for daemon jobs")
		}
	case ScheduleInterval:
		if s.IntervalMinutes <= 0 {
			return errors.New("interval schedule requires interval_minutes > 0")
		}
	case ScheduleCalendar:
		if s.IntervalMinutes != 0 {
			return errors.New("calendar schedule does not support interval_minutes")
		}
		if s.Minute != 0 || s.Hour != 0 {
			if err := validateClock(s.Hour, s.Minute); err != nil {
				return err
			}
		}
		if s.Weekday < 0 || s.Weekday > 7 {
			return fmt.Errorf("weekday must be between 0 and 7, got %d", s.Weekday)
		}
		if s.Day < 0 || s.Day > 31 {
			return fmt.Errorf("day must be between 0 and 31, got %d", s.Day)
		}
		if s.Month < 0 || s.Month > 12 {
			return fmt.Errorf("month must be between 0 and 12, got %d", s.Month)
		}
	case "":
		return errors.New("schedule is required")
	default:
		return fmt.Errorf("invalid schedule kind %q", s.Kind)
	}
	return nil
}

func validateClock(hour, minute int) error {
	if hour < 0 || hour > 23 {
		return fmt.Errorf("hour must be between 0 and 23, got %d", hour)
	}
	if minute < 0 || minute > 59 {
		return fmt.Errorf("minute must be between 0 and 59, got %d", minute)
	}
	return nil
}

func (s Spec) SpecChecksum() (string, error) {
	type envPair struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	type checksumSpec struct {
		Name         string      `json:"name"`
		Label        string      `json:"label"`
		Target       Target      `json:"target"`
		Command      string      `json:"command,omitempty"`
		Args         []string    `json:"args,omitempty"`
		ShellCommand string      `json:"shell_command,omitempty"`
		WorkingDir   string      `json:"working_dir,omitempty"`
		Environment  []envPair   `json:"environment,omitempty"`
		Schedule     Schedule    `json:"schedule"`
		Trigger      TriggerKind `json:"trigger,omitempty"`
		WatchPaths   []string    `json:"watch_paths,omitempty"`
		Enabled      bool        `json:"enabled"`
		StdoutPath   string      `json:"stdout_path,omitempty"`
		StderrPath   string      `json:"stderr_path,omitempty"`
		PlistPath    string      `json:"plist_path,omitempty"`
	}

	keys := make([]string, 0, len(s.Environment))
	for key := range s.Environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]envPair, 0, len(keys))
	for _, key := range keys {
		env = append(env, envPair{Key: key, Value: s.Environment[key]})
	}

	payload, err := json.Marshal(checksumSpec{
		Name:         s.Name,
		Label:        s.Label,
		Target:       s.Target,
		Command:      s.Command,
		Args:         s.Args,
		ShellCommand: s.ShellCommand,
		WorkingDir:   s.WorkingDir,
		Environment:  env,
		Schedule:     s.Schedule,
		Trigger:      s.Trigger,
		WatchPaths:   s.WatchPaths,
		Enabled:      s.Enabled,
		StdoutPath:   s.StdoutPath,
		StderrPath:   s.StderrPath,
		PlistPath:    s.PlistPath,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}
