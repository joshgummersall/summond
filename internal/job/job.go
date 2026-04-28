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
	"time"
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
	IntervalSet     bool         `json:"interval_set,omitempty"`
	MinuteSet       bool         `json:"minute_set,omitempty"`
	HourSet         bool         `json:"hour_set,omitempty"`
	WeekdaySet      bool         `json:"weekday_set,omitempty"`
	DaySet          bool         `json:"day_set,omitempty"`
	MonthSet        bool         `json:"month_set,omitempty"`
}

type ExecutionRecord struct {
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	ExitCode   *int       `json:"exit_code,omitempty"`
	Error      string     `json:"error,omitempty"`
}

type Spec struct {
	Name                    string            `json:"name"`
	Group                   string            `json:"group,omitempty"`
	Label                   string            `json:"label"`
	Target                  Target            `json:"target"`
	Command                 string            `json:"command,omitempty"`
	Args                    []string          `json:"args,omitempty"`
	ShellCommand            string            `json:"shell_command,omitempty"`
	WorkingDir              string            `json:"working_dir,omitempty"`
	Environment             map[string]string `json:"environment,omitempty"`
	EnvironmentFilePath     string            `json:"environment_file_path,omitempty"`
	Schedule                Schedule          `json:"schedule"`
	Trigger                 TriggerKind       `json:"trigger,omitempty"`
	WatchPaths              []string          `json:"watch_paths,omitempty"`
	ThrottleIntervalSeconds int               `json:"throttle_interval_seconds,omitempty"`
	AbandonProcessGroup     bool              `json:"abandon_process_group,omitempty"`
	StdoutPath              string            `json:"stdout_path,omitempty"`
	StderrPath              string            `json:"stderr_path,omitempty"`
	PlistPath               string            `json:"plist_path,omitempty"`
	RuntimeBinaryPath       string            `json:"runtime_binary_path,omitempty"`
	Checksum                string            `json:"checksum,omitempty"`
	LastStartedAt           *time.Time        `json:"last_started_at,omitempty"`
	LastFinishedAt          *time.Time        `json:"last_finished_at,omitempty"`
	LastExitCode            *int              `json:"last_exit_code,omitempty"`
	LastError               string            `json:"last_error,omitempty"`
	LastStdoutOffset        int64             `json:"last_stdout_offset,omitempty"`
	LastStderrOffset        int64             `json:"last_stderr_offset,omitempty"`
	RunCount                int               `json:"run_count,omitempty"`
	SuccessCount            int               `json:"success_count,omitempty"`
	FailureCount            int               `json:"failure_count,omitempty"`
	RecentRuns              []ExecutionRecord `json:"recent_runs,omitempty"`
	WatchFingerprints       map[string]string `json:"watch_fingerprints,omitempty"`
}

var invalidNameChars = regexp.MustCompile(`[^a-z0-9.-]+`)
var validJobName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
var validGroup = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
var validLabel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func (s *Spec) Normalize() error {
	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" {
		return errors.New("job name is required")
	}
	if !validJobName.MatchString(s.Name) {
		return fmt.Errorf("job name must match %q, got %q", validJobName.String(), s.Name)
	}
	s.Group = strings.TrimSpace(s.Group)
	if s.Group != "" && !validGroup.MatchString(s.Group) {
		return fmt.Errorf("group must match %q, got %q", validGroup.String(), s.Group)
	}
	if s.Label == "" {
		s.Label = DefaultLabel(s.Group, s.Name)
	}
	if !validLabel.MatchString(s.Label) {
		return fmt.Errorf("label must match %q, got %q", validLabel.String(), s.Label)
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
		if err := s.applyScheduleDefaults(); err != nil {
			return err
		}
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
	if s.Trigger == TriggerOnChange && s.ThrottleIntervalSeconds == 0 {
		s.ThrottleIntervalSeconds = 2
	}
	if s.ThrottleIntervalSeconds < 0 {
		return errors.New("throttle_interval_seconds must be >= 0")
	}
	if s.Trigger == "" && s.Schedule.Kind == "" {
		return errors.New("either schedule or trigger is required")
	}
	return nil
}

func (s *Spec) applyScheduleDefaults() error {
	if s.Schedule.Kind == "" {
		return nil
	}
	seed, err := s.scheduleSeed()
	if err != nil {
		return err
	}
	next := 0
	pick := func(mod int) int {
		value := int(seed[next%len(seed)]) % mod
		next++
		return value
	}
	switch s.Schedule.Kind {
	case ScheduleHourly:
		if !s.Schedule.MinuteSet {
			s.Schedule.Minute = pick(60)
			s.Schedule.MinuteSet = true
		}
	case ScheduleDaily:
		if !s.Schedule.HourSet {
			s.Schedule.Hour = pick(24)
			s.Schedule.HourSet = true
		}
		if !s.Schedule.MinuteSet {
			s.Schedule.Minute = pick(60)
			s.Schedule.MinuteSet = true
		}
	case ScheduleWeekly:
		if !s.Schedule.WeekdaySet {
			s.Schedule.Weekday = 1 + pick(7)
			s.Schedule.WeekdaySet = true
		}
		if !s.Schedule.HourSet {
			s.Schedule.Hour = pick(24)
			s.Schedule.HourSet = true
		}
		if !s.Schedule.MinuteSet {
			s.Schedule.Minute = pick(60)
			s.Schedule.MinuteSet = true
		}
	}
	return nil
}

func (s Spec) scheduleSeed() ([32]byte, error) {
	type envPair struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	type scheduleSeed struct {
		Kind            ScheduleKind `json:"kind"`
		IntervalMinutes *int         `json:"interval_minutes,omitempty"`
		Minute          *int         `json:"minute,omitempty"`
		Hour            *int         `json:"hour,omitempty"`
		Weekday         *int         `json:"weekday,omitempty"`
		Day             *int         `json:"day,omitempty"`
		Month           *int         `json:"month,omitempty"`
	}
	type seedSpec struct {
		Name                string       `json:"name"`
		Group               string       `json:"group,omitempty"`
		Label               string       `json:"label"`
		Target              Target       `json:"target"`
		Command             string       `json:"command,omitempty"`
		Args                []string     `json:"args,omitempty"`
		ShellCommand        string       `json:"shell_command,omitempty"`
		WorkingDir          string       `json:"working_dir,omitempty"`
		Environment         []envPair    `json:"environment,omitempty"`
		EnvironmentFilePath string       `json:"environment_file_path,omitempty"`
		Schedule            scheduleSeed `json:"schedule"`
		Trigger             TriggerKind  `json:"trigger,omitempty"`
		WatchPaths          []string     `json:"watch_paths,omitempty"`
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
	seedSchedule := scheduleSeed{Kind: s.Schedule.Kind}
	if s.Schedule.IntervalSet {
		seedSchedule.IntervalMinutes = &s.Schedule.IntervalMinutes
	}
	if s.Schedule.MinuteSet {
		seedSchedule.Minute = &s.Schedule.Minute
	}
	if s.Schedule.HourSet {
		seedSchedule.Hour = &s.Schedule.Hour
	}
	if s.Schedule.WeekdaySet {
		seedSchedule.Weekday = &s.Schedule.Weekday
	}
	if s.Schedule.DaySet {
		seedSchedule.Day = &s.Schedule.Day
	}
	if s.Schedule.MonthSet {
		seedSchedule.Month = &s.Schedule.Month
	}

	payload, err := json.Marshal(seedSpec{
		Name:                s.Name,
		Group:               s.Group,
		Label:               s.Label,
		Target:              s.Target,
		Command:             s.Command,
		Args:                s.Args,
		ShellCommand:        s.ShellCommand,
		WorkingDir:          s.WorkingDir,
		Environment:         env,
		EnvironmentFilePath: s.EnvironmentFilePath,
		Schedule:            seedSchedule,
		Trigger:             s.Trigger,
		WatchPaths:          s.WatchPaths,
	})
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(payload), nil
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

func DefaultLabel(group, name string) string {
	parts := []string{"com", "joshgummersall", "summond"}
	if safe := normalizeLabelPart(group); safe != "" {
		parts = append(parts, safe)
	}
	if safe := normalizeLabelPart(name); safe != "" {
		parts = append(parts, safe)
	} else {
		parts = append(parts, "job")
	}
	return strings.Join(parts, ".")
}

func normalizeLabelPart(value string) string {
	safe := strings.ToLower(strings.TrimSpace(value))
	safe = invalidNameChars.ReplaceAllString(safe, "-")
	return strings.Trim(safe, "-.")
}

func (s Spec) ManagedKey() string {
	if s.Group == "" {
		return s.Name
	}
	return s.Group + "." + s.Name
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
		Name                    string      `json:"name"`
		Group                   string      `json:"group,omitempty"`
		Label                   string      `json:"label"`
		Target                  Target      `json:"target"`
		Command                 string      `json:"command,omitempty"`
		Args                    []string    `json:"args,omitempty"`
		ShellCommand            string      `json:"shell_command,omitempty"`
		WorkingDir              string      `json:"working_dir,omitempty"`
		Environment             []envPair   `json:"environment,omitempty"`
		EnvironmentFilePath     string      `json:"environment_file_path,omitempty"`
		Schedule                Schedule    `json:"schedule"`
		Trigger                 TriggerKind `json:"trigger,omitempty"`
		WatchPaths              []string    `json:"watch_paths,omitempty"`
		ThrottleIntervalSeconds int         `json:"throttle_interval_seconds,omitempty"`
		AbandonProcessGroup     bool        `json:"abandon_process_group,omitempty"`
		StdoutPath              string      `json:"stdout_path,omitempty"`
		StderrPath              string      `json:"stderr_path,omitempty"`
		PlistPath               string      `json:"plist_path,omitempty"`
		RuntimeBinaryPath       string      `json:"runtime_binary_path,omitempty"`
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
		Name:                    s.Name,
		Group:                   s.Group,
		Label:                   s.Label,
		Target:                  s.Target,
		Command:                 s.Command,
		Args:                    s.Args,
		ShellCommand:            s.ShellCommand,
		WorkingDir:              s.WorkingDir,
		Environment:             env,
		EnvironmentFilePath:     s.EnvironmentFilePath,
		Schedule:                s.Schedule,
		Trigger:                 s.Trigger,
		WatchPaths:              s.WatchPaths,
		ThrottleIntervalSeconds: s.ThrottleIntervalSeconds,
		AbandonProcessGroup:     s.AbandonProcessGroup,
		StdoutPath:              s.StdoutPath,
		StderrPath:              s.StderrPath,
		PlistPath:               s.PlistPath,
		RuntimeBinaryPath:       s.RuntimeBinaryPath,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}
