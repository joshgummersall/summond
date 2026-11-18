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
	"strconv"
	"syscall"
	"time"

	"github.com/joshgummersall/summond/internal/job"
	"github.com/joshgummersall/summond/internal/plist"
)

type Paths struct {
	Home      string
	LaunchDir string
	// Private makes the scope's state, logs, and plists owner-only. Daemon
	// scope sets it: its files live under /Library, which every local user
	// can traverse, and they hold job env values, commands, and output.
	Private bool
}

// DirMode is the mode of the scope's state directory, jobs directory, and job
// directories.
func (p Paths) DirMode() os.FileMode {
	if p.Private {
		return 0o700
	}
	return 0o755
}

// FileMode is the mode of the scope's plists, state.json, lock files, and
// logs.
func (p Paths) FileMode() os.FileMode {
	if p.Private {
		return 0o600
	}
	return 0o644
}

// Tighten clears the permission bits of path that mode does not allow. It
// never adds bits, so it leaves agent state that already has the expected
// mode as it is. It skips a missing path and does not follow symlinks.
func Tighten(path string, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	if perm := info.Mode().Perm(); perm&^mode != 0 {
		return os.Chmod(path, perm&mode)
	}
	return nil
}

type Store struct {
	paths Paths
}

func NewStore(paths Paths) *Store {
	return &Store{paths: paths}
}

// DiscoverPaths returns the paths for the scope that euid selects: root
// manages daemons under /Library, and any other user manages agents under
// their own home.
func DiscoverPaths(euid int) (Paths, error) {
	if euid == 0 {
		return Paths{
			Home:      filepath.Join(string(filepath.Separator), "Library", "Application Support", "summond"),
			LaunchDir: filepath.Join(string(filepath.Separator), "Library", "LaunchDaemons"),
			Private:   true,
		}, nil
	}
	current, err := user.LookupId(strconv.Itoa(euid))
	if err != nil {
		return Paths{}, fmt.Errorf("resolve user home: %w", err)
	}
	return Paths{
		Home:      filepath.Join(current.HomeDir, "Library", "Application Support", "summond"),
		LaunchDir: filepath.Join(current.HomeDir, "Library", "LaunchAgents"),
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
	// The plist holds the job's env values, so it gets the scope's file mode.
	// launchd needs a daemon plist owned by root and not writable by group or
	// other; it does not need others to read it.
	if err := writeFileAtomic(spec.PlistPath, content, s.paths.FileMode()); err != nil {
		return job.Spec{}, fmt.Errorf("write plist: %w", err)
	}
	if err := Tighten(filepath.Join(s.jobDir(spec.ManagedKey()), "exec.lock"), s.paths.FileMode()); err != nil {
		return job.Spec{}, fmt.Errorf("tighten execution lock file: %w", err)
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

// Remove deletes the job with managed key key: its plist, locks, and job
// directory.
func (s *Store) Remove(key string) (job.Spec, error) {
	spec, err := s.Load(key)
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

// Load reads the job with managed key key. It does not match bare job
// names; resolve those with the specs from List.
func (s *Store) Load(key string) (job.Spec, error) {
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

func (s *Store) MetadataPath(key string) string {
	return s.metadataPath(key)
}

func (s *Store) MetadataPathForSpec(spec job.Spec) string {
	return s.metadataPath(spec.ManagedKey())
}

func (s *Store) Paths() Paths {
	return s.paths
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
	// Create the state home with the scope's mode first, so MkdirAll below
	// does not create it with the bin directory's mode. Root can run the
	// binary through a 0700 home, and only root runs a daemon's copy.
	if err := os.MkdirAll(s.paths.Home, s.paths.DirMode()); err != nil {
		return "", fmt.Errorf("create state directory: %w", err)
	}
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

func (s *Store) RecordExecutionStart(key string, startedAt time.Time) error {
	return s.updateRuntimeState(key, func(spec *job.Spec) {
		spec.LastStartedAt = &startedAt
		spec.LastFinishedAt = nil
		spec.LastExitCode = nil
		spec.LastError = ""
		spec.LastStdoutOffset = fileSize(spec.StdoutPath)
		spec.LastStderrOffset = fileSize(spec.StderrPath)
	})
}

func fileSize(path string) int64 {
	if path == "" {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func (s *Store) RecordExecutionFinish(key string, record job.ExecutionRecord) error {
	return s.updateRuntimeState(key, func(spec *job.Spec) {
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
		filepath.Join(s.jobDir(spec.ManagedKey()), "exec.lock"),
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
	return filepath.Join(s.paths.LaunchDir, spec.Label+".plist")
}

// ensureDirs creates the directories a job needs. The state, jobs, and job
// directories get the scope's directory mode, including ones that already
// exist. The bin directory and the plist directory keep 0755: launchd and
// the Homebrew cask run the runtime binary, and /Library/LaunchDaemons is
// the system's.
func (s *Store) ensureDirs(spec job.Spec) error {
	private := []string{
		s.paths.Home,
		s.jobsDir(),
		s.jobDir(spec.ManagedKey()),
	}
	for _, dir := range private {
		if err := os.MkdirAll(dir, s.paths.DirMode()); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
		if err := Tighten(dir, s.paths.DirMode()); err != nil {
			return fmt.Errorf("tighten directory %s: %w", dir, err)
		}
	}
	for _, dir := range []string{filepath.Dir(s.runtimeBinaryPath()), filepath.Dir(spec.PlistPath)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
	}
	return nil
}

// ensureLogFiles creates the job's logs with the scope's file mode, and
// tightens existing logs and their rotated copies to it.
func (s *Store) ensureLogFiles(spec job.Spec) error {
	for _, path := range []string{spec.StdoutPath, spec.StderrPath} {
		if path == "" {
			continue
		}
		for _, p := range []string{path, path + ".1"} {
			if err := Tighten(p, s.paths.FileMode()); err != nil {
				return fmt.Errorf("tighten log file %s: %w", p, err)
			}
		}
	}
	seen := map[string]struct{}{}
	for _, path := range []string{spec.StdoutPath, spec.StderrPath} {
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		// O_EXCL ensures we create a new file rather than following a
		// pre-existing symlink that could redirect writes elsewhere.
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL, s.paths.FileMode())
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
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

func (s *Store) updateRuntimeState(key string, update func(*job.Spec)) error {
	// Check first, so the metadata lock does not create a directory for a
	// job that does not exist.
	if err := s.requireJob(key); err != nil {
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
	if err := writeFileAtomic(s.metadataPath(spec.ManagedKey()), data, s.paths.FileMode()); err != nil {
		return fmt.Errorf("write job metadata: %w", err)
	}
	return nil
}

// writeFileAtomic replaces path with data and mode. It writes a temp file,
// which CreateTemp creates with mode 0600, sets mode, and renames it over
// path, so the old mode of path never applies to the new data.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, closeErr, os.Chmod(tmpPath, mode)); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

// TryLockExecution attempts to acquire an exclusive, non-blocking lock held
// for the duration of a job run, so an overlapping trigger can detect that a
// previous run is still in progress. acquired is false (with a nil error) if
// another process already holds the lock.
func (s *Store) TryLockExecution(key string) (unlock func(), acquired bool, err error) {
	if err := s.requireJob(key); err != nil {
		return nil, false, err
	}
	lockPath := filepath.Join(s.jobDir(key), "exec.lock")
	file, err := s.openLockFile(lockPath)
	if err != nil {
		return nil, false, fmt.Errorf("open execution lock file: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("lock execution lock file: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, true, nil
}

func (s *Store) lockMetadata(key string) (func(), error) {
	lockPath := s.metadataPath(key) + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), s.paths.DirMode()); err != nil {
		return nil, fmt.Errorf("create metadata lock directory: %w", err)
	}
	file, err := s.openLockFile(lockPath)
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

// openLockFile opens or creates a lock file with the scope's file mode, and
// tightens a lock file that an earlier version created.
func (s *Store) openLockFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, s.paths.FileMode())
	if err != nil {
		return nil, err
	}
	if err := Tighten(path, s.paths.FileMode()); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

// requireJob returns an error that wraps os.ErrNotExist if no job has
// managed key key.
func (s *Store) requireJob(key string) error {
	if _, err := os.Stat(s.metadataPath(key)); err != nil {
		return fmt.Errorf("read job metadata: %w", err)
	}
	return nil
}

func (s *Store) UpdateWatchFingerprints(key string, fingerprints map[string]string) error {
	return s.updateRuntimeState(key, func(spec *job.Spec) {
		spec.WatchFingerprints = fingerprints
	})
}

func preserveRuntimeState(dst *job.Spec, src job.Spec) {
	dst.LastStartedAt = src.LastStartedAt
	dst.LastFinishedAt = src.LastFinishedAt
	dst.LastExitCode = src.LastExitCode
	dst.LastError = src.LastError
	dst.LastStdoutOffset = src.LastStdoutOffset
	dst.LastStderrOffset = src.LastStderrOffset
	dst.RunCount = src.RunCount
	dst.SuccessCount = src.SuccessCount
	dst.FailureCount = src.FailureCount
	dst.RecentRuns = append([]job.ExecutionRecord(nil), src.RecentRuns...)
	if src.WatchFingerprints != nil {
		dst.WatchFingerprints = make(map[string]string, len(src.WatchFingerprints))
		for k, v := range src.WatchFingerprints {
			dst.WatchFingerprints[k] = v
		}
	}
}
