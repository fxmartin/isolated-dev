package cli

import (
	"bytes"
	"testing"
)

func TestVersionDoesNotInvokeMutatingDependencies(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	mutated := false
	exitCode := Run([]string{"--version"}, Dependencies{
		Stdout:  &stdout,
		Version: "1.2.3",
		OnMutate: func() {
			mutated = true
		},
	})

	if exitCode != 0 {
		t.Fatalf("Run() exit code = %d, want 0", exitCode)
	}
	if got, want := stdout.String(), "isolated-dev 1.2.3\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if mutated {
		t.Fatal("version command invoked a mutating dependency")
	}
}

func TestStatusInvokesReadOnlyStatusDependency(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	statusPath := ""
	mutated := false
	exitCode := Run([]string{"status", "/tmp/project"}, Dependencies{
		Stdout:  &bytes.Buffer{},
		Stderr:  &stderr,
		Version: "1.2.3",
		Status: func(path string) error {
			statusPath = path
			return nil
		},
		OnMutate: func() {
			mutated = true
		},
	})

	if exitCode != 0 {
		t.Fatalf("Run() exit code = %d, want 0; stderr = %q", exitCode, stderr.String())
	}
	if statusPath != "/tmp/project" {
		t.Errorf("status path = %q, want /tmp/project", statusPath)
	}
	if mutated {
		t.Fatal("status command invoked a mutating dependency")
	}
}
