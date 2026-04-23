package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/standardlabs/summond/internal/job"
	"github.com/standardlabs/summond/internal/plist"
)

type Paths struct {
	Home         string
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
	spec.RuntimeBinaryPath = defaultIfEmpty(spec.RuntimeBinaryPath, s.runtimeBinaryPath())
	checksum, err := spec.SpecChecksum()
	if err != nil {
		return job.Spec{}, fmt.Errorf("compute checksum: %w", err)
	}
	spec.Checksum = checksum

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
	spec, err = s.writeSpec(spec, true)
	if err != nil {
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

func (s *Store) RuntimeBinaryPath() string {
	return s.runtimeBinaryPath()
}

func (s *Store) UpdateEnabled(name string, enabled bool) (job.Spec, error) {
	spec, err := s.Load(name)
	if err != nil {
		return job.Spec{}, err
	}
	spec.Enabled = enabled
	return s.Install(spec)
}

func (s *Store) PrepareRuntimeBinary(sourcePath string) (string, error) {
	if sourcePath == "" {
		return "", errors.New("runtime binary source path is required")
	}
	targetPath := s.runtimeBinaryPath()
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return "", fmt.Errorf("create runtime binary directory: %w", err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return "", fmt.Errorf("open runtime binary source: %w", err)
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return "", fmt.Errorf("stat runtime binary source: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(targetPath), filepath.Base(targetPath)+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("create runtime binary temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := io.Copy(tmp, source); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("copy runtime binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("close runtime binary temp file: %w", err)
	}
	mode := info.Mode() & os.ModePerm
	if mode&0o111 == 0 {
		mode = 0o755
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("chmod runtime binary temp file: %w", err)
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("install runtime binary: %w", err)
	}
	return targetPath, nil
}

func (s *Store) RecordExecutionStart(name string, startedAt time.Time) error {
	return s.updateRuntimeState(name, func(spec *job.Spec) {
		spec.LastStartedAt = &startedAt
		spec.LastFinishedAt = nil
		spec.LastExitCode = nil
		spec.LastError = ""
	})
}

func (s *Store) RecordExecutionFinish(name string, record job.ExecutionRecord) error {
	return s.updateRuntimeState(name, func(spec *job.Spec) {
		spec.LastStartedAt = &record.StartedAt
		spec.LastFinishedAt = record.FinishedAt
		spec.LastExitCode = record.ExitCode
		spec.LastError = record.Error
		spec.RunCount++
		if record.ExitCode != nil && *record.ExitCode == 0 && record.Error == "" {
			spec.SuccessCount++
		} else {
			spec.FailureCount++
		}
		spec.RecentRuns = append([]job.ExecutionRecord{record}, spec.RecentRuns...)
		if len(spec.RecentRuns) > 10 {
			spec.RecentRuns = spec.RecentRuns[:10]
		}
	})
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

func (s *Store) runtimeBinaryPath() string {
	return filepath.Join(s.paths.Home, "bin", "summond")
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
		filepath.Dir(s.runtimeBinaryPath()),
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

func defaultIfEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func (s *Store) writeSpec(spec job.Spec, preserveRuntime bool) (job.Spec, error) {
	unlock, err := s.lockMetadata(spec.Name)
	if err != nil {
		return job.Spec{}, err
	}
	defer unlock()
	if preserveRuntime {
		existing, err := s.readMetadataUnlocked(spec.Name)
		if err == nil {
			preserveRuntimeState(&spec, existing)
		} else if !errors.Is(err, os.ErrNotExist) {
			return job.Spec{}, err
		}
	}
	if err := s.writeMetadataUnlocked(spec); err != nil {
		return job.Spec{}, err
	}
	return spec, nil
}

func (s *Store) updateRuntimeState(name string, update func(*job.Spec)) error {
	unlock, err := s.lockMetadata(name)
	if err != nil {
		return err
	}
	defer unlock()
	spec, err := s.readMetadataUnlocked(name)
	if err != nil {
		return err
	}
	update(&spec)
	return s.writeMetadataUnlocked(spec)
}

func (s *Store) readMetadataUnlocked(name string) (job.Spec, error) {
	data, err := os.ReadFile(s.metadataPath(name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return job.Spec{}, os.ErrNotExist
		}
		return job.Spec{}, fmt.Errorf("read job metadata: %w", err)
	}
	var spec job.Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		return job.Spec{}, fmt.Errorf("decode job metadata: %w", err)
	}
	return spec, nil
}

func (s *Store) writeMetadataUnlocked(spec job.Spec) error {
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return fmt.Errorf("encode job metadata: %w", err)
	}
	path := s.metadataPath(spec.Name)
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create metadata temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write job metadata temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close job metadata temp file: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("chmod job metadata temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write job metadata: %w", err)
	}
	return nil
}

func (s *Store) lockMetadata(name string) (func(), error) {
	lockPath := s.metadataPath(name) + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, fmt.Errorf("create metadata lock directory: %w", err)
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open metadata lock file: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		file.Close()
		return nil, fmt.Errorf("lock metadata file: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}

func preserveRuntimeState(dst *job.Spec, src job.Spec) {
	dst.LastStartedAt = src.LastStartedAt
	dst.LastFinishedAt = src.LastFinishedAt
	dst.LastExitCode = src.LastExitCode
	dst.LastError = src.LastError
	dst.RunCount = src.RunCount
	dst.SuccessCount = src.SuccessCount
	dst.FailureCount = src.FailureCount
	dst.RecentRuns = append([]job.ExecutionRecord(nil), src.RecentRuns...)
}
