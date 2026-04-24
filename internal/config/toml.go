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
	AbandonProcessGroup     bool              `toml:"abandon_process_group"`
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
		resolvePaths(&spec, baseDir)
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
	return spec
}

func defaultGroup(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:6])
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
