package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/joshgummersall/summond/internal/job"
)

// unsafePathEntries returns the entries of pathValue that a user other than
// trustedUID could use to plant a command, each with the reason: a directory
// not owned by trustedUID, a directory writable by group or other, or an
// empty or relative entry, which resolves against the working directory.
// Directories that do not exist are skipped.
func unsafePathEntries(pathValue string, trustedUID uint32) []string {
	var problems []string
	for _, dir := range strings.Split(pathValue, ":") {
		switch {
		case dir == "":
			problems = append(problems, `"" (empty entry, resolves to the working directory)`)
		case !filepath.IsAbs(dir):
			problems = append(problems, dir+" (relative entry, resolves against the working directory)")
		default:
			info, err := os.Stat(dir)
			if err != nil {
				continue
			}
			if reason := ownershipProblem(info, trustedUID); reason != "" {
				problems = append(problems, dir+" ("+reason+")")
			}
		}
	}
	return problems
}

// ownershipProblem says why a user other than trustedUID could write to the
// file or directory info describes, or returns "" if no such user can.
func ownershipProblem(info os.FileInfo, trustedUID uint32) string {
	var reasons []string
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Uid != trustedUID {
		reasons = append(reasons, fmt.Sprintf("owned by uid %d", st.Uid))
	}
	mode := info.Mode().Perm()
	if mode&0o020 != 0 {
		reasons = append(reasons, "writable by group")
	}
	if mode&0o002 != 0 {
		reasons = append(reasons, "writable by other")
	}
	return strings.Join(reasons, ", ")
}

// warnUnsafeDaemonPath warns on w when the daemon env file's PATH has an
// entry that a non-root user could use to plant a command that a daemon job
// then runs as root. It only warns; it never fails apply.
func warnUnsafeDaemonPath(w io.Writer, envPath string) {
	env, err := loadSharedEnv(envPath)
	if err != nil {
		fmt.Fprintf(w, "summond: warning: cannot check daemon PATH: %v\n", err)
		return
	}
	pathValue, ok := env["PATH"]
	if !ok {
		return
	}
	problems := unsafePathEntries(pathValue, 0)
	if len(problems) == 0 {
		return
	}
	fmt.Fprintf(w, "summond: warning: PATH in %s has entries that a non-root user can write to. A daemon job that runs a command by bare name may run a file planted there, as root:\n", envPath)
	for _, problem := range problems {
		fmt.Fprintf(w, "  %s\n", problem)
	}
	fmt.Fprintf(w, "Use absolute paths in daemon jobs, or set a root-only PATH: sudo summond env set PATH=%s\n", daemonDefaultPath)
}

// unsafeJobPaths returns the paths in spec that a user other than trustedUID
// could change to alter what the job runs, each with the reason: a bare
// command name, which resolves through PATH at run time, or a command file,
// its directory, or a working directory that is not owned by trustedUID or
// is writable by group or other. Paths that do not exist are skipped.
// shell_command text is not analysed.
func unsafeJobPaths(spec job.Spec, trustedUID uint32) []string {
	var problems []string
	check := func(label, path string) {
		info, err := os.Stat(path)
		if err != nil {
			return
		}
		if reason := ownershipProblem(info, trustedUID); reason != "" {
			problems = append(problems, label+" "+path+" ("+reason+")")
		}
	}
	switch {
	case spec.Command == "":
	case !strings.Contains(spec.Command, "/"):
		problems = append(problems, "command "+spec.Command+" (bare name, found through PATH at run time)")
	default:
		check("command", spec.Command)
		check("command directory", filepath.Dir(spec.Command))
	}
	if spec.WorkingDir != "" {
		check("working directory", spec.WorkingDir)
	}
	return problems
}

// warnUnsafeDaemonJobs warns on w about each daemon job in specs whose
// command or working directory a non-root user could change. That user could
// then run code as root at the job's next run. It only warns; it never fails
// apply or add.
func warnUnsafeDaemonJobs(w io.Writer, specs []job.Spec) {
	for _, spec := range specs {
		problems := unsafeJobPaths(spec, 0)
		if len(problems) == 0 {
			continue
		}
		fmt.Fprintf(w, "summond: warning: daemon job %q runs as root, but a non-root user can change what it runs and so get root at its next run:\n", spec.Name)
		for _, problem := range problems {
			fmt.Fprintf(w, "  %s\n", problem)
		}
		fmt.Fprintln(w, "Use an absolute command path to a root-owned file in a root-owned directory, and a root-owned working directory, none writable by group or other.")
	}
}
