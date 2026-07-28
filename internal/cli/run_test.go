package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestRunCommandInvokesTheProjectCommandDependency(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	gotPath, gotName := "", ""
	exitCode := Run([]string{"run", "/tmp/project", "dev"}, Dependencies{
		Stdout: &bytes.Buffer{},
		Stderr: &stderr,
		Run: func(path string, name string) (int, error) {
			gotPath, gotName = path, name
			return 0, nil
		},
	})

	if exitCode != 0 {
		t.Fatalf("Run() exit code = %d, want 0; stderr = %q", exitCode, stderr.String())
	}
	if gotPath != "/tmp/project" || gotName != "dev" {
		t.Errorf("invocation = (%q, %q), want (/tmp/project, dev)", gotPath, gotName)
	}
}

func TestRunCommandPreservesTheGuestExitStatus(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	exitCode := Run([]string{"run", "/tmp/project", "test"}, Dependencies{
		Stdout: &bytes.Buffer{},
		Stderr: &stderr,
		Run: func(string, string) (int, error) {
			return 42, nil
		},
	})

	if exitCode != 42 {
		t.Fatalf("Run() exit code = %d, want the guest exit status 42", exitCode)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want no CLI error for a failing project command", stderr.String())
	}
}

func TestRunCommandReportsAnExecutionFailure(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	exitCode := Run([]string{"run", "/tmp/project", "deploy"}, Dependencies{
		Stdout: &bytes.Buffer{},
		Stderr: &stderr,
		Run: func(string, string) (int, error) {
			return 0, errors.New("command \"deploy\" is not declared by this project")
		},
	})

	if exitCode != 1 {
		t.Fatalf("Run() exit code = %d, want 1", exitCode)
	}
	if !strings.Contains(stderr.String(), "run: command \"deploy\" is not declared") {
		t.Errorf("stderr = %q, want the rejection reported", stderr.String())
	}
}

func TestRunCommandRequiresACommandName(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	invoked := false
	exitCode := Run([]string{"run", "/tmp/project"}, Dependencies{
		Stdout: &bytes.Buffer{},
		Stderr: &stderr,
		Run: func(string, string) (int, error) {
			invoked = true
			return 0, nil
		},
	})

	if exitCode != 2 {
		t.Fatalf("Run() exit code = %d, want 2", exitCode)
	}
	if invoked {
		t.Error("run invoked the project command dependency without a command name")
	}
	if !strings.Contains(stderr.String(), "isolated-dev run PROJECT COMMAND") {
		t.Errorf("stderr = %q, want usage guidance", stderr.String())
	}
}

func TestRunCommandReportsAnUnavailableDependency(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	exitCode := Run([]string{"run", "/tmp/project", "dev"}, Dependencies{
		Stdout: &bytes.Buffer{},
		Stderr: &stderr,
	})

	if exitCode != 1 {
		t.Fatalf("Run() exit code = %d, want 1", exitCode)
	}
	if !strings.Contains(stderr.String(), "run: command is unavailable") {
		t.Errorf("stderr = %q, want an unavailable-dependency error", stderr.String())
	}
}

func TestUsageDocumentsTheRunVerb(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	exitCode := Run([]string{"bogus"}, Dependencies{Stdout: &bytes.Buffer{}, Stderr: &stderr})

	if exitCode != 2 {
		t.Fatalf("Run() exit code = %d, want 2", exitCode)
	}
	if !strings.Contains(stderr.String(), "run PROJECT COMMAND") {
		t.Errorf("usage = %q, want the run verb documented", stderr.String())
	}
}
