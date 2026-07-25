package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/joshgummersall/summond/internal/job"
)

type fileConfig struct {
	Group string            `toml:"group"`
	Jobs  map[string]rawJob `toml:"jobs"`
}

type rawRetry struct {
	Attempts        *int `toml:"attempts"`
	DelaySeconds    *int `toml:"delay_seconds"`
	MaxDelaySeconds *int `toml:"max_delay_seconds"`
}

type rawWatch struct {
	Paths           []string `toml:"paths"`
	ThrottleSeconds *int     `toml:"throttle_seconds"`
}

type rawJob struct {
	Label                   string            `toml:"label"`
	Target                  string            `toml:"target"`
	Command                 string            `toml:"command"`
	Args                    []string          `toml:"args"`
	ShellCommand            string            `toml:"shell_command"`
	WorkingDir              string            `toml:"working_dir"`
	Environment             map[string]string `toml:"env"`
	Schedule                string            `toml:"schedule"`
	Trigger                 string            `toml:"trigger"`
	IntervalMinutes         *int              `toml:"interval_minutes"`
	Minute                  *int              `toml:"minute"`
	Hour                    *int              `toml:"hour"`
	Weekday                 *int              `toml:"weekday"`
	Day                     *int              `toml:"day"`
	Month                   *int              `toml:"month"`
	WatchPaths              []string          `toml:"watch_paths"`
	ThrottleIntervalSeconds *int              `toml:"throttle_interval_seconds"`
	RetryAttempts           *int              `toml:"retry_attempts"`
	RetryDelaySeconds       *int              `toml:"retry_delay_seconds"`
	RetryMaxDelaySeconds    *int              `toml:"retry_max_delay_seconds"`
	AbandonProcessGroup     bool              `toml:"abandon_process_group"`
	Retry                   *rawRetry         `toml:"retry"`
	Watch                   *rawWatch         `toml:"watch"`
}

func LoadFile(path string) ([]job.Spec, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg fileConfig
	meta, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return nil, err
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, key := range undecoded {
			keys = append(keys, key.String())
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("unknown config key(s): %s", strings.Join(keys, ", "))
	}

	names := make([]string, 0, len(cfg.Jobs))
	for name := range cfg.Jobs {
		names = append(names, name)
	}
	sort.Strings(names)

	specs := make([]job.Spec, 0, len(names))
	baseDir := filepath.Dir(absPath)
	group := cfg.Group
	if strings.TrimSpace(group) == "" {
		group = defaultGroup(absPath)
	}
	for _, name := range names {
		spec := cfg.Jobs[name].toSpec(name, group)
		if err := resolvePaths(&spec, baseDir); err != nil {
			return nil, fmt.Errorf("job %s: %w", spec.Name, err)
		}
		if err := spec.Normalize(); err != nil {
			return nil, fmt.Errorf("job %s: %w", spec.Name, err)
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

func (r rawJob) toSpec(name string, group string) job.Spec {
	spec := job.Spec{
		Name:         name,
		Group:        group,
		Label:        r.Label,
		Target:       job.Target(r.Target),
		Command:      r.Command,
		Args:         r.Args,
		ShellCommand: r.ShellCommand,
		WorkingDir:   r.WorkingDir,
		Environment:  r.Environment,
		Schedule: job.Schedule{
			Kind: job.ScheduleKind(r.Schedule),
		},
		Trigger:    job.TriggerKind(r.Trigger),
		WatchPaths: r.WatchPaths,
	}
	spec.AbandonProcessGroup = r.AbandonProcessGroup
	if r.IntervalMinutes != nil {
		spec.Schedule.IntervalMinutes = *r.IntervalMinutes
		spec.Schedule.IntervalSet = true
	}
	if r.Minute != nil {
		spec.Schedule.Minute = *r.Minute
		spec.Schedule.MinuteSet = true
	}
	if r.Hour != nil {
		spec.Schedule.Hour = *r.Hour
		spec.Schedule.HourSet = true
	}
	if r.Weekday != nil {
		spec.Schedule.Weekday = *r.Weekday
		spec.Schedule.WeekdaySet = true
	}
	if r.Day != nil {
		spec.Schedule.Day = *r.Day
		spec.Schedule.DaySet = true
	}
	if r.Month != nil {
		spec.Schedule.Month = *r.Month
		spec.Schedule.MonthSet = true
	}
	if r.ThrottleIntervalSeconds != nil {
		spec.ThrottleIntervalSeconds = *r.ThrottleIntervalSeconds
	}
	if r.RetryAttempts != nil {
		spec.RetryAttempts = *r.RetryAttempts
	}
	if r.RetryDelaySeconds != nil {
		spec.RetryDelaySeconds = *r.RetryDelaySeconds
	}
	if r.RetryMaxDelaySeconds != nil {
		spec.RetryMaxDelaySeconds = *r.RetryMaxDelaySeconds
	}
	if r.Retry != nil {
		if r.Retry.Attempts != nil {
			spec.RetryAttempts = *r.Retry.Attempts
		}
		if r.Retry.DelaySeconds != nil {
			spec.RetryDelaySeconds = *r.Retry.DelaySeconds
		}
		if r.Retry.MaxDelaySeconds != nil {
			spec.RetryMaxDelaySeconds = *r.Retry.MaxDelaySeconds
		}
	}
	if r.Watch != nil {
		if len(r.Watch.Paths) > 0 {
			spec.WatchPaths = r.Watch.Paths
		}
		if r.Watch.ThrottleSeconds != nil {
			spec.ThrottleIntervalSeconds = *r.Watch.ThrottleSeconds
		}
	}
	return spec
}

func defaultGroup(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:6])
}

// expandEnv expands $var/${var} references using the current process
// environment, returning an error if any referenced variable is unset so
// that typos don't silently resolve to an empty string.
func expandEnv(s string) (string, error) {
	var missing []string
	expanded := os.Expand(s, func(name string) string {
		value, ok := os.LookupEnv(name)
		if !ok {
			missing = append(missing, name)
		}
		return value
	})
	if len(missing) > 0 {
		sort.Strings(missing)
		return "", fmt.Errorf("unset environment variable(s) referenced: %s", strings.Join(missing, ", "))
	}
	return expanded, nil
}

func resolvePaths(spec *job.Spec, baseDir string) error {
	var err error
	if spec.WorkingDir, err = expandEnv(spec.WorkingDir); err != nil {
		return err
	}
	for i, path := range spec.WatchPaths {
		if spec.WatchPaths[i], err = expandEnv(path); err != nil {
			return err
		}
	}
	if spec.Command, err = expandEnv(spec.Command); err != nil {
		return err
	}
	if spec.ShellCommand, err = expandEnv(spec.ShellCommand); err != nil {
		return err
	}
	for i, arg := range spec.Args {
		if spec.Args[i], err = expandEnv(arg); err != nil {
			return err
		}
	}
	for k, v := range spec.Environment {
		if spec.Environment[k], err = expandEnv(v); err != nil {
			return err
		}
	}

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
	return nil
}
