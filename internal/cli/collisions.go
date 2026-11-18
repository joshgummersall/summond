package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/joshgummersall/summond/internal/job"
)

// checkJobCollisions returns an error that lists every pair of different jobs
// that would share a managed key or a launchd label once desired is applied
// over existing. Two specs are the same job only when their group and name
// are equal; a desired spec replaces the existing spec of the same job, so
// applying an unchanged config again, or updating a job in place, is not a
// collision.
//
// Managed keys name state directories and labels name plist files, so both
// compare without regard to case: on a case-insensitive volume, keys or
// labels that differ only in case share one path.
func checkJobCollisions(desired, existing []job.Spec) error {
	var lines []string
	for i, spec := range desired {
		for _, other := range desired[i+1:] {
			if shared := sharedIdentity(spec, other); shared != "" {
				lines = append(lines, fmt.Sprintf("job %s and job %s share %s", describeJob(spec, other), describeJob(other, spec), shared))
			}
		}
		for _, other := range existing {
			if isReplacedBy(other, desired) {
				continue
			}
			if shared := sharedIdentity(spec, other); shared != "" {
				lines = append(lines, fmt.Sprintf("job %s and existing job %s share %s", describeJob(spec, other), describeJob(other, spec), shared))
			}
		}
	}
	if len(lines) == 0 {
		return nil
	}
	return errors.New("jobs collide:\n- " + strings.Join(lines, "\n- "))
}

func sameJob(a, b job.Spec) bool {
	return a.Group == b.Group && a.Name == b.Name
}

func isReplacedBy(spec job.Spec, desired []job.Spec) bool {
	for _, d := range desired {
		if sameJob(spec, d) {
			return true
		}
	}
	return false
}

// sharedIdentity describes what two different jobs share, or returns "" if
// they share nothing.
func sharedIdentity(spec, other job.Spec) string {
	if sameJob(spec, other) {
		return ""
	}
	var shared []string
	if strings.EqualFold(spec.ManagedKey(), other.ManagedKey()) {
		shared = append(shared, fmt.Sprintf("managed key %q", spec.ManagedKey()))
	}
	if spec.Label != "" && strings.EqualFold(spec.Label, other.Label) {
		shared = append(shared, fmt.Sprintf("label %q", spec.Label))
	}
	return strings.Join(shared, " and ")
}

// describeJob names spec by its managed key. When other has the exact same
// key, it adds the group so the two jobs can be told apart.
func describeJob(spec, other job.Spec) string {
	if spec.ManagedKey() != other.ManagedKey() {
		return fmt.Sprintf("%q", spec.ManagedKey())
	}
	if spec.Group == "" {
		return fmt.Sprintf("%q (ungrouped)", spec.ManagedKey())
	}
	return fmt.Sprintf("%q (group %q)", spec.ManagedKey(), spec.Group)
}
