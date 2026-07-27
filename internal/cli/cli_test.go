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

func TestUpInvokesLifecycleDependency(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	upPath := ""
	exitCode := Run([]string{"up", "/tmp/project"}, Dependencies{
		Stdout: &bytes.Buffer{},
		Stderr: &stderr,
		Up: func(path string) error {
			upPath = path
			return nil
		},
	})

	if exitCode != 0 {
		t.Fatalf("Run() exit code = %d, want 0; stderr = %q", exitCode, stderr.String())
	}
	if upPath != "/tmp/project" {
		t.Errorf("up path = %q, want /tmp/project", upPath)
	}
}

func TestStopInvokesLifecycleDependency(t *testing.T) {
	t.Parallel()

	stopPath := ""
	exitCode := Run([]string{"stop", "/tmp/project"}, Dependencies{
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		Stop: func(path string) error {
			stopPath = path
			return nil
		},
	})

	if exitCode != 0 {
		t.Fatalf("Run() exit code = %d, want 0", exitCode)
	}
	if stopPath != "/tmp/project" {
		t.Errorf("stop path = %q, want /tmp/project", stopPath)
	}
}

func TestDestroyRequiresExplicitConfirmation(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	destroyed := false
	exitCode := Run([]string{"destroy", "/tmp/project"}, Dependencies{
		Stdout: &bytes.Buffer{},
		Stderr: &stderr,
		Destroy: func(string) error {
			destroyed = true
			return nil
		},
	})

	if exitCode == 0 {
		t.Fatal("Run() exit code = 0, want confirmation failure")
	}
	if destroyed {
		t.Fatal("destroy dependency invoked without --yes")
	}
	if got := stderr.String(); !bytes.Contains([]byte(got), []byte("--yes")) {
		t.Errorf("stderr = %q, want --yes guidance", got)
	}
}

func TestDestroyRunsAfterExplicitConfirmation(t *testing.T) {
	t.Parallel()

	destroyPath := ""
	exitCode := Run([]string{"destroy", "--yes", "/tmp/project"}, Dependencies{
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		Destroy: func(path string) error {
			destroyPath = path
			return nil
		},
	})

	if exitCode != 0 {
		t.Fatalf("Run() exit code = %d, want 0", exitCode)
	}
	if destroyPath != "/tmp/project" {
		t.Errorf("destroy path = %q, want /tmp/project", destroyPath)
	}
}
