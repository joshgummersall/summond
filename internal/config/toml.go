package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/joshgummersall/summond/internal/job"
)

type fileConfig struct {
	Group   string               `toml:"group"`
	Windows map[string]rawWindow `toml:"windows"`
	Jobs    map[string]rawJob    `toml:"jobs"`
}

type rawWindow struct {
	StartHour *int `toml:"start_hour"`
	EndHour   *int `toml:"end_hour"`
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

// rawUSB accepts vendor/product ids as TOML integers (including 0x hex
// literals) or as strings like "0x046d" copied from ioreg/system_profiler.
type rawUSB struct {
	VendorID  any `toml:"vendor_id"`
	ProductID any `toml:"product_id"`
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
	Window                  string            `toml:"window"`
	WatchPaths              []string          `toml:"watch_paths"`
	ThrottleIntervalSeconds *int              `toml:"throttle_interval_seconds"`
	Concurrency             bool              `toml:"concurrency"`
	AbandonProcessGroup     bool              `toml:"abandon_process_group"`
	Retry                   *rawRetry         `toml:"retry"`
	Watch                   *rawWatch         `toml:"watch"`
	USB                     *rawUSB           `toml:"usb"`
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

	windows, err := resolveWindows(cfg.Windows)
	if err != nil {
		return nil, err
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
		spec, err := cfg.Jobs[name].toSpec(name, group)
		if err != nil {
			return nil, fmt.Errorf("job %s: %w", name, err)
		}
		if window, ok := windows[spec.Schedule.Window]; ok {
			spec.Schedule.WindowStartHour = window.start
			spec.Schedule.WindowEndHour = window.end
			spec.Schedule.WindowSet = true
		}
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

var windowKinds = map[string]job.WindowKind{
	"morning":   job.WindowMorning,
	"afternoon": job.WindowAfternoon,
	"evening":   job.WindowEvening,
}

type windowBounds struct {
	start int
	end   int
}

func resolveWindows(raw map[string]rawWindow) (map[job.WindowKind]windowBounds, error) {
	resolved := make(map[job.WindowKind]windowBounds, len(raw))
	for name, w := range raw {
		kind, ok := windowKinds[name]
		if !ok {
			return nil, fmt.Errorf("unknown window %q (must be one of morning, afternoon, evening)", name)
		}
		if w.StartHour == nil || w.EndHour == nil {
			return nil, fmt.Errorf("window %q requires both start_hour and end_hour", name)
		}
		start, end := *w.StartHour, *w.EndHour
		if start < 0 || start > 23 {
			return nil, fmt.Errorf("window %q start_hour must be between 0 and 23, got %d", name, start)
		}
		if end < 0 || end > 23 {
			return nil, fmt.Errorf("window %q end_hour must be between 0 and 23, got %d", name, end)
		}
		if start > end {
			return nil, fmt.Errorf("window %q start_hour must be <= end_hour, got %d > %d", name, start, end)
		}
		resolved[kind] = windowBounds{start: start, end: end}
	}
	return resolved, nil
}

func (r rawJob) toSpec(name string, group string) (job.Spec, error) {
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
			Kind:   job.ScheduleKind(r.Schedule),
			Window: job.WindowKind(r.Window),
		},
		Trigger:    job.TriggerKind(r.Trigger),
		WatchPaths: r.WatchPaths,
	}
	spec.Concurrency = r.Concurrency
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
	if r.USB != nil {
		var err error
		if spec.USBVendorID, err = parseUSBID("usb.vendor_id", r.USB.VendorID); err != nil {
			return job.Spec{}, err
		}
		if spec.USBProductID, err = parseUSBID("usb.product_id", r.USB.ProductID); err != nil {
			return job.Spec{}, err
		}
	}
	return spec, nil
}

// parseUSBID converts a [jobs.X.usb] id to an int. TOML integers (including
// 0x hex literals) pass through; strings are parsed as hex with an optional
// 0x prefix or as decimal, matching how ioreg/system_profiler print ids.
func parseUSBID(key string, value any) (int, error) {
	switch v := value.(type) {
	case nil:
		return 0, fmt.Errorf("%s is required", key)
	case int64:
		return int(v), nil
	case string:
		text := strings.TrimSpace(v)
		base := 10
		if rest, ok := strings.CutPrefix(strings.ToLower(text), "0x"); ok {
			text, base = rest, 16
		}
		id, err := strconv.ParseUint(text, base, 16)
		if err != nil {
			return 0, fmt.Errorf("%s: cannot parse %q as a USB id (use hex like \"0x046d\" or decimal)", key, v)
		}
		return int(id), nil
	default:
		return 0, fmt.Errorf("%s must be an integer or string, got %T", key, value)
	}
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
