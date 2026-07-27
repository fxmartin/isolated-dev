package host

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCheckReportsMissingPrerequisiteBeforeRunningCommands(t *testing.T) {
	t.Parallel()

	runCalled := false
	checker := Checker{
		LookPath: func(name string) (string, error) {
			if name == "container" {
				return "", errors.New("not found")
			}
			return "/usr/bin/" + name, nil
		},
		Run: func(context.Context, string, ...string) ([]byte, error) {
			runCalled = true
			return nil, nil
		},
	}

	_, err := checker.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "container") {
		t.Fatalf("Check() error = %v, want missing container message", err)
	}
	if runCalled {
		t.Fatal("Check() ran commands after missing prerequisite")
	}
}

func TestCheckRejectsUnsupportedContainerMajorVersion(t *testing.T) {
	t.Parallel()

	checker := Checker{
		LookPath: func(name string) (string, error) {
			return "/usr/bin/" + name, nil
		},
		Run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "/usr/bin/container" {
				return []byte("container CLI version 2.0.0"), nil
			}
			return nil, nil
		},
	}

	_, err := checker.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "requires Apple Container CLI 1.x") {
		t.Fatalf("Check() error = %v, want compatibility message", err)
	}
}

func TestCheckReturnsDetectedContainerVersion(t *testing.T) {
	t.Parallel()

	checker := Checker{
		LookPath: func(name string) (string, error) {
			return "/usr/bin/" + name, nil
		},
		Run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "/usr/bin/container" {
				return []byte("container CLI version 1.1.0 (build: release)"), nil
			}
			return nil, nil
		},
	}

	got, err := checker.Check(context.Background())
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if got.ContainerVersion != "1.1.0" {
		t.Errorf("ContainerVersion = %q, want 1.1.0", got.ContainerVersion)
	}
}
