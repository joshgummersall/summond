package launchd

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"

	"github.com/standardlabs/summond/internal/job"
)

type Runner interface {
	Bootstrap(spec job.Spec) error
	Bootout(spec job.Spec) error
	Kickstart(spec job.Spec) error
	Stop(spec job.Spec) error
	Print(spec job.Spec) (string, error)
}

type LaunchCtl struct{}

func (LaunchCtl) Bootstrap(spec job.Spec) error {
	return run("launchctl", "bootstrap", domain(spec.Target), spec.PlistPath)
}

func (LaunchCtl) Bootout(spec job.Spec) error {
	return run("launchctl", "bootout", domain(spec.Target), spec.PlistPath)
}

func (LaunchCtl) Kickstart(spec job.Spec) error {
	return run("launchctl", "kickstart", "-k", serviceTarget(spec))
}

func (LaunchCtl) Stop(spec job.Spec) error {
	return run("launchctl", "kill", "TERM", serviceTarget(spec))
}

func (LaunchCtl) Print(spec job.Spec) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("launchctl", "print", serviceTarget(spec))
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, stderr.String())
	}
	return stdout.String(), nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func domain(target job.Target) string {
	if target == job.TargetDaemon {
		return "system"
	}
	return fmt.Sprintf("gui/%d", os.Getuid())
}

func serviceTarget(spec job.Spec) string {
	return domain(spec.Target) + "/" + spec.Label
}
