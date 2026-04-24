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

type PathSet struct {
	Agent  Paths
	Daemon Paths
}

type Store struct {
	paths Paths
}

func NewStore(paths Paths) *Store {
	return &Store{paths: paths}
}

func DiscoverPaths() (Paths, error) {
	set, err := DiscoverPathSet()
	if err != nil {
		return Paths{}, err
	}
	return set.Agent, nil
}

func DiscoverPathSet() (PathSet, error) {
	current, err := user.Current()
	if err != nil {
		return PathSet{}, fmt.Errorf("resolve user home: %w", err)
	}

	common := Paths{
		AgentsDir:    filepath.Join(current.HomeDir, "Library", "LaunchAgents"),
		DaemonsDir:   "/Library/LaunchDaemons",
		NewsyslogDir: "/etc/newsyslog.d",
	}
	return PathSet{
		Agent: Paths{
			Home:         filepath.Join(current.HomeDir, "Library", "Application Support", "summond"),
			AgentsDir:    common.AgentsDir,
			DaemonsDir:   common.DaemonsDir,
			NewsyslogDir: common.NewsyslogDir,
		},
		Daemon: Paths{
			Home:         filepath.Join(string(filepath.Separator), "Library", "Application Support", "summond"),
			AgentsDir:    common.AgentsDir,
			DaemonsDir:   common.DaemonsDir,
			NewsyslogDir: common.NewsyslogDir,
		},
	}, nil
}

func (s *Store) Install(spec job.Spec) (job.Spec, error) {
	spec, content, err := s.PrepareInstall(spec)
	if err != nil {
		return job.Spec{}, err
	}
	if err := s.ensureDirs(spec); err != nil {
		return job.Spec{}, err
	}
	if err := s.ensureLogFiles(spec); err != nil {
		return job.Spec{}, err
	}
	if err := os.WriteFile(spec.PlistPath, content, 0o644); err != nil {
		return job.Spec{}, fmt.Errorf("write plist: %w", err)
	}
	symlinkPath := filepath.Join(s.jobDir(spec.ManagedKey()), "job.plist")
	_ = os.Remove(symlinkPath)
	_ = os.Symlink(spec.PlistPath, symlinkPath)
	spec, err = s.writeSpec(spec, true)
	if err != nil {
		return job.Spec{}, err
	}
	return spec, nil
}

func (s *Store) PrepareInstall(spec job.Spec) (job.Spec, []byte, error) {
	if err := spec.Normalize(); err != nil {
		return job.Spec{}, nil, err
	}
	spec.StdoutPath = s.logPath(spec, "out")
	spec.StderrPath = s.logPath(spec, "err")
	spec.PlistPath = s.plistInstallPath(spec)
	spec.RuntimeBinaryPath = defaultIfEmpty(spec.RuntimeBinaryPath, s.runtimeBinaryPath())
	checksum, err := spec.SpecChecksum()
	if err != nil {
		return job.Spec{}, nil, fmt.Errorf("compute checksum: %w", err)
	}
	spec.Checksum = checksum
	content, err := plist.Render(spec)
	if err != nil {
		return job.Spec{}, nil, err
	}
	return spec, content, nil
}

func (s *Store) Remove(name string) (job.Spec, error) {
	spec, err := s.Load(name)
	if err != nil {
		return job.Spec{}, err
	}
	for _, path := range s.cleanupPaths(spec) {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return job.Spec{}, fmt.Errorf("remove %s: %w", path, err)
		}
	}
	if err := os.RemoveAll(s.jobDir(spec.ManagedKey())); err != nil && !errors.Is(err, os.ErrNotExist) {
		return job.Spec{}, fmt.Errorf("remove job directory: %w", err)
	}
	return spec, nil
}

func (s *Store) Load(name string) (job.Spec, error) {
	key, err := s.resolveManagedKey(name)
	if err != nil {
		return job.Spec{}, err
	}
	return s.readMetadataByKey(key)
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
		if !entry.IsDir() {
			continue
		}
		key := entry.Name()
		spec, err := s.readMetadataByKey(key)
		if err != nil {
			if errors.Is(err, os.ErrPermission) {
				continue
			}
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

func (s *Store) MetadataPath(name string) string {
	return s.metadataPath(name)
}

func (s *Store) MetadataPathForSpec(spec job.Spec) string {
	return s.metadataPath(spec.ManagedKey())
}

func (s *Store) Paths() Paths {
	return s.paths
}

func (s *Store) JobDir(name string) (string, error) {
	key, err := s.resolveManagedKey(name)
	if err != nil {
		return "", err
	}
	return s.jobDir(key), nil
}

func (s *Store) JobDirForSpec(spec job.Spec) string {
	return s.jobDir(spec.ManagedKey())
}

func (s *Store) RuntimeBinaryPath() string {
	return s.runtimeBinaryPath()
}

func (s *Store) EnvFilePath() string {
	return filepath.Join(s.paths.Home, "env.json")
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

func (s *Store) logPath(spec job.Spec, stream string) string {
	name := map[string]string{"out": "stdout.log", "err": "stderr.log"}[stream]
	return filepath.Join(s.jobDir(spec.ManagedKey()), name)
}

func (s *Store) metadataPath(key string) string {
	return filepath.Join(s.jobDir(key), "state.json")
}

func (s *Store) cleanupPaths(spec job.Spec) []string {
	paths := []string{
		spec.PlistPath,
		s.metadataPath(spec.ManagedKey()) + ".lock",
	}
	seen := make(map[string]struct{}, len(paths))
	unique := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		unique = append(unique, path)
	}
	return unique
}

func (s *Store) jobDir(key string) string {
	return filepath.Join(s.jobsDir(), key)
}

func (s *Store) jobsDir() string {
	return filepath.Join(s.paths.Home, "jobs")
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
		s.jobDir(spec.ManagedKey()),
		filepath.Dir(s.runtimeBinaryPath()),
		filepath.Dir(spec.PlistPath),
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
	key := spec.ManagedKey()
	unlock, err := s.lockMetadata(key)
	if err != nil {
		return job.Spec{}, err
	}
	defer unlock()
	if preserveRuntime {
		existing, err := s.readMetadataUnlocked(key)
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
	key, err := s.resolveManagedKey(name)
	if err != nil {
		return err
	}
	unlock, err := s.lockMetadata(key)
	if err != nil {
		return err
	}
	defer unlock()
	spec, err := s.readMetadataUnlocked(key)
	if err != nil {
		return err
	}
	update(&spec)
	return s.writeMetadataUnlocked(spec)
}

func (s *Store) readMetadataByKey(key string) (job.Spec, error) {
	data, err := os.ReadFile(s.metadataPath(key))
	if err != nil {
		return job.Spec{}, fmt.Errorf("read job metadata: %w", err)
	}
	var spec job.Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		return job.Spec{}, fmt.Errorf("decode job metadata: %w", err)
	}
	return spec, nil
}

func (s *Store) readMetadataUnlocked(key string) (job.Spec, error) {
	data, err := os.ReadFile(s.metadataPath(key))
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
	path := s.metadataPath(spec.ManagedKey())
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

func (s *Store) lockMetadata(key string) (func(), error) {
	lockPath := s.metadataPath(key) + ".lock"
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

func (s *Store) resolveManagedKey(name string) (string, error) {
	if _, err := os.Stat(s.metadataPath(name)); err == nil {
		return name, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read job metadata: %w", err)
	}
	specs, err := s.List()
	if err != nil {
		return "", err
	}
	var matches []string
	for _, spec := range specs {
		if spec.Name == name || spec.ManagedKey() == name {
			matches = append(matches, spec.ManagedKey())
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("read job metadata: %w", os.ErrNotExist)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("duplicate managed job name %q", name)
	}
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
