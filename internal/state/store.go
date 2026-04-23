package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"

	"github.com/joshgummersall/summond/internal/job"
	"github.com/joshgummersall/summond/internal/plist"
)

type Paths struct {
	Home         string
	ConfigDir    string
	AgentsDir    string
	DaemonsDir   string
	NewsyslogDir string
}

type Store struct {
	paths Paths
}

func NewStore(paths Paths) *Store {
	return &Store{paths: paths}
}

func DiscoverPaths() (Paths, error) {
	current, err := user.Current()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve user home: %w", err)
	}

	home := os.Getenv("SUMMOND_HOME")
	if home == "" {
		home = filepath.Join(current.HomeDir, "Library", "Application Support", "summond")
	}
	configDir := os.Getenv("SUMMOND_CONFIG_DIR")
	if configDir == "" {
		configDir = filepath.Join(current.HomeDir, ".config", "summond")
	}
	agentsDir := os.Getenv("SUMMOND_LAUNCH_AGENTS_DIR")
	if agentsDir == "" {
		agentsDir = filepath.Join(current.HomeDir, "Library", "LaunchAgents")
	}
	daemonsDir := os.Getenv("SUMMOND_LAUNCH_DAEMONS_DIR")
	if daemonsDir == "" {
		daemonsDir = "/Library/LaunchDaemons"
	}
	newsyslogDir := os.Getenv("SUMMOND_NEWSYSLOG_DIR")
	if newsyslogDir == "" {
		newsyslogDir = "/etc/newsyslog.d"
	}
	return Paths{
		Home:         home,
		ConfigDir:    configDir,
		AgentsDir:    agentsDir,
		DaemonsDir:   daemonsDir,
		NewsyslogDir: newsyslogDir,
	}, nil
}

func (s *Store) Install(spec job.Spec) (job.Spec, error) {
	if err := spec.Normalize(); err != nil {
		return job.Spec{}, err
	}
	spec.StdoutPath = defaultIfEmpty(spec.StdoutPath, s.logPath(spec.Name, "out"))
	spec.StderrPath = defaultIfEmpty(spec.StderrPath, s.logPath(spec.Name, "err"))
	spec.PlistPath = s.plistInstallPath(spec)

	if err := s.ensureDirs(spec); err != nil {
		return job.Spec{}, err
	}
	if err := s.ensureLogFiles(spec); err != nil {
		return job.Spec{}, err
	}
	content, err := plist.Render(spec)
	if err != nil {
		return job.Spec{}, err
	}
	if err := os.WriteFile(spec.PlistPath, content, 0o644); err != nil {
		return job.Spec{}, fmt.Errorf("write plist: %w", err)
	}
	if err := s.writeMetadata(spec); err != nil {
		return job.Spec{}, err
	}
	return spec, nil
}

func (s *Store) Remove(name string) (job.Spec, error) {
	spec, err := s.Load(name)
	if err != nil {
		return job.Spec{}, err
	}
	if err := os.Remove(spec.PlistPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return job.Spec{}, fmt.Errorf("remove plist: %w", err)
	}
	if err := os.Remove(s.metadataPath(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return job.Spec{}, fmt.Errorf("remove metadata: %w", err)
	}
	return spec, nil
}

func (s *Store) Load(name string) (job.Spec, error) {
	data, err := os.ReadFile(s.metadataPath(name))
	if err != nil {
		return job.Spec{}, fmt.Errorf("read job metadata: %w", err)
	}
	var spec job.Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		return job.Spec{}, fmt.Errorf("decode job metadata: %w", err)
	}
	return spec, nil
}

func (s *Store) List() ([]job.Spec, error) {
	dir := s.jobsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read jobs directory: %w", err)
	}
	var specs []job.Spec
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		name := entry.Name()[:len(entry.Name())-len(".json")]
		spec, err := s.Load(name)
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	sort.Slice(specs, func(i, j int) bool {
		return specs[i].Name < specs[j].Name
	})
	return specs, nil
}

func (s *Store) JobsFilePath() string {
	return s.jobsDir()
}

func (s *Store) Paths() Paths {
	return s.paths
}

func (s *Store) LogsDir() string {
	return s.logsDir()
}

func (s *Store) UpdateEnabled(name string, enabled bool) (job.Spec, error) {
	spec, err := s.Load(name)
	if err != nil {
		return job.Spec{}, err
	}
	spec.Enabled = enabled
	return s.Install(spec)
}

func (s *Store) logPath(name, stream string) string {
	return filepath.Join(s.logsDir(), fmt.Sprintf("%s.%s.log", name, stream))
}

func (s *Store) metadataPath(name string) string {
	return filepath.Join(s.jobsDir(), name+".json")
}

func (s *Store) jobsDir() string {
	return filepath.Join(s.paths.Home, "jobs")
}

func (s *Store) logsDir() string {
	return filepath.Join(s.paths.Home, "logs")
}

func (s *Store) plistInstallPath(spec job.Spec) string {
	if spec.Target == job.TargetDaemon {
		return filepath.Join(s.paths.DaemonsDir, spec.Label+".plist")
	}
	return filepath.Join(s.paths.AgentsDir, spec.Label+".plist")
}

func (s *Store) ensureDirs(spec job.Spec) error {
	dirs := []string{
		s.paths.Home,
		s.jobsDir(),
		s.logsDir(),
		filepath.Dir(spec.PlistPath),
	}
	for _, path := range []string{spec.StdoutPath, spec.StderrPath} {
		if path == "" {
			continue
		}
		dirs = append(dirs, filepath.Dir(path))
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
	}
	return nil
}

func (s *Store) ensureLogFiles(spec job.Spec) error {
	seen := map[string]struct{}{}
	for _, path := range []string{spec.StdoutPath, spec.StderrPath} {
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		file, err := os.OpenFile(path, os.O_CREATE, 0o644)
		if err != nil {
			return fmt.Errorf("create log file %s: %w", path, err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("close log file %s: %w", path, err)
		}
	}
	return nil
}

func (s *Store) writeMetadata(spec job.Spec) error {
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return fmt.Errorf("encode job metadata: %w", err)
	}
	if err := os.WriteFile(s.metadataPath(spec.Name), data, 0o644); err != nil {
		return fmt.Errorf("write job metadata: %w", err)
	}
	return nil
}

func defaultIfEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
