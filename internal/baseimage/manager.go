package baseimage

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"
)

const DefaultVersion = "1"

var versionPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

type Manager struct {
	Runner         Runner
	ReadinessTries int
	FallbackTries  int
	RetryDelay     time.Duration
	Sleep          func(time.Duration)
}

type EnsureResult struct {
	Reference string
	Built     bool
}

func Reference(version string) (string, error) {
	if !versionPattern.MatchString(version) {
		return "", fmt.Errorf("invalid base-image version %q", version)
	}
	return "local/isolated-dev-base:" + version, nil
}

func (manager Manager) Ensure(
	ctx context.Context,
	version string,
	contextDir string,
) (EnsureResult, error) {
	if manager.Runner == nil {
		return EnsureResult{}, errors.New("base-image runner is not configured")
	}
	reference, err := Reference(version)
	if err != nil {
		return EnsureResult{}, err
	}
	if _, err := manager.Runner.Run(ctx, "container", "image", "inspect", reference); err == nil {
		return EnsureResult{Reference: reference}, nil
	}

	dockerfile := filepath.Join(contextDir, "Dockerfile")
	output, err := manager.Runner.Run(
		ctx,
		"container",
		"build",
		"--tag", reference,
		"--label", "dev.isolated.base-version="+version,
		"--build-arg", "BASE_VERSION="+version,
		"--file", dockerfile,
		contextDir,
	)
	if err != nil {
		return EnsureResult{}, fmt.Errorf("build base image %s: %w\n%s", reference, err, output)
	}
	return EnsureResult{Reference: reference, Built: true}, nil
}

func (manager Manager) WaitDocker(ctx context.Context, machineName string) error {
	if manager.Runner == nil {
		return errors.New("base-image runner is not configured")
	}
	readinessTries := manager.ReadinessTries
	if readinessTries <= 0 {
		readinessTries = 30
	}
	fallbackTries := manager.FallbackTries
	if fallbackTries <= 0 {
		fallbackTries = 30
	}
	delay := manager.RetryDelay
	if delay <= 0 {
		delay = time.Second
	}
	sleep := manager.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}

	if manager.waitForDocker(ctx, machineName, readinessTries, delay, sleep) == nil {
		return nil
	}
	output, err := manager.Runner.Run(
		ctx,
		"container",
		"machine", "run",
		"--name", machineName,
		"--root",
		"--detach",
		"--",
		"/usr/local/sbin/isolated-dev-dockerd",
	)
	if err != nil {
		return fmt.Errorf("start Docker fallback: %w\n%s", err, output)
	}
	if err := manager.waitForDocker(ctx, machineName, fallbackTries, delay, sleep); err != nil {
		return fmt.Errorf("Docker did not become ready after fallback: %w", err)
	}
	return nil
}

func (manager Manager) waitForDocker(
	ctx context.Context,
	machineName string,
	attempts int,
	delay time.Duration,
	sleep func(time.Duration),
) error {
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		output, err := manager.Runner.Run(
			ctx,
			"container",
			"machine", "run",
			"--name", machineName,
			"--root",
			"--",
			"/usr/bin/docker", "info",
		)
		if err == nil {
			return nil
		}
		lastErr = fmt.Errorf("%w: %s", err, output)
		if attempt+1 < attempts {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				sleep(delay)
			}
		}
	}
	return lastErr
}
