package launchd

import (
	"bytes"
	"fmt"
	"os/exec"

	"github.com/joshgummersall/summond/internal/job"
)

type Runner interface {
	Bootstrap(spec job.Spec) error
	Bootout(spec job.Spec) error
	Kickstart(spec job.Spec) error
	Stop(spec job.Spec) error
	Print(spec job.Spec) (string, error)
}

// LaunchCtl runs launchctl against one domain: "system" for daemons or
// "gui/<uid>" for agents.
type LaunchCtl struct {
	Domain string
}

// DomainFor returns the launchd domain for the scope that euid selects.
func DomainFor(euid int) string {
	if euid == 0 {
		return "system"
	}
	return fmt.Sprintf("gui/%d", euid)
}

func (l LaunchCtl) Bootstrap(spec job.Spec) error {
	return run("launchctl", "bootstrap", l.Domain, spec.PlistPath)
}

func (l LaunchCtl) Bootout(spec job.Spec) error {
	return run("launchctl", "bootout", l.Domain, spec.PlistPath)
}

func (l LaunchCtl) Kickstart(spec job.Spec) error {
	return run("launchctl", "kickstart", "-k", l.serviceTarget(spec))
}

func (l LaunchCtl) Stop(spec job.Spec) error {
	return run("launchctl", "kill", "TERM", l.serviceTarget(spec))
}

func (l LaunchCtl) Print(spec job.Spec) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("launchctl", "print", l.serviceTarget(spec))
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, stderr.String())
	}
	return stdout.String(), nil
}

func run(name string, args ...string) error {
	var stderr bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("%w: %s", err, stderr.String())
		}
		return err
	}
	return nil
}

func (l LaunchCtl) serviceTarget(spec job.Spec) string {
	return l.Domain + "/" + spec.Label
}
