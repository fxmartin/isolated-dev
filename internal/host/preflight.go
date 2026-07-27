package host

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
)

var containerVersionPattern = regexp.MustCompile(`\b(\d+)\.(\d+)\.(\d+)\b`)

type Result struct {
	ContainerPath    string
	ContainerVersion string
	SSHPath          string
}

type Checker struct {
	LookPath func(string) (string, error)
	Run      func(context.Context, string, ...string) ([]byte, error)
}

func DefaultChecker() Checker {
	return Checker{
		LookPath: exec.LookPath,
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		},
	}
}

func (checker Checker) Check(ctx context.Context) (Result, error) {
	if checker.LookPath == nil || checker.Run == nil {
		return Result{}, errors.New("host prerequisite checker is not configured")
	}

	containerPath, err := checker.LookPath("container")
	if err != nil {
		return Result{}, errors.New("missing prerequisite: install Apple's container CLI 1.x")
	}
	sshPath, err := checker.LookPath("ssh")
	if err != nil {
		return Result{}, errors.New("missing prerequisite: install the OpenSSH client")
	}

	output, err := checker.Run(ctx, containerPath, "--version")
	if err != nil {
		return Result{}, fmt.Errorf("inspect Apple Container CLI: %w", err)
	}
	match := containerVersionPattern.FindSubmatch(output)
	if len(match) == 0 {
		return Result{}, errors.New("could not determine Apple Container CLI version")
	}
	if string(match[1]) != "1" {
		return Result{}, fmt.Errorf(
			"requires Apple Container CLI 1.x; found %s.%s.%s",
			match[1],
			match[2],
			match[3],
		)
	}

	return Result{
		ContainerPath:    containerPath,
		ContainerVersion: string(match[1]) + "." + string(match[2]) + "." + string(match[3]),
		SSHPath:          sshPath,
	}, nil
}
