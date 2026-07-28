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

// A bare `upgrade` is the preview, so it must reach the command without being
// treated as a mutation.
func TestUpgradePreviewsWithoutConfirmationOrMutation(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	upgradePath := ""
	upgradeConfirmed := true
	mutated := false
	exitCode := Run([]string{"upgrade", "/tmp/project"}, Dependencies{
		Stdout: &bytes.Buffer{},
		Stderr: &stderr,
		Upgrade: func(path string, confirmed bool) error {
			upgradePath = path
			upgradeConfirmed = confirmed
			return nil
		},
		OnMutate: func() {
			mutated = true
		},
	})

	if exitCode != 0 {
		t.Fatalf("Run() exit code = %d, want 0; stderr = %q", exitCode, stderr.String())
	}
	if upgradePath != "/tmp/project" {
		t.Errorf("upgrade path = %q, want /tmp/project", upgradePath)
	}
	if upgradeConfirmed {
		t.Error("bare upgrade was confirmed, want a preview")
	}
	if mutated {
		t.Fatal("upgrade preview invoked a mutating dependency")
	}
}

func TestUpgradeRecreatesAfterExplicitConfirmation(t *testing.T) {
	t.Parallel()

	upgradePath := ""
	upgradeConfirmed := false
	mutated := false
	exitCode := Run([]string{"upgrade", "--yes", "/tmp/project"}, Dependencies{
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		Upgrade: func(path string, confirmed bool) error {
			upgradePath = path
			upgradeConfirmed = confirmed
			return nil
		},
		OnMutate: func() {
			mutated = true
		},
	})

	if exitCode != 0 {
		t.Fatalf("Run() exit code = %d, want 0", exitCode)
	}
	if upgradePath != "/tmp/project" || !upgradeConfirmed {
		t.Errorf("upgrade = (%q, %t), want (/tmp/project, true)", upgradePath, upgradeConfirmed)
	}
	if !mutated {
		t.Error("confirmed upgrade did not report a mutation")
	}
}

func TestUpgradeRejectsAnUnknownConfirmationFlag(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	invoked := false
	exitCode := Run([]string{"upgrade", "--force", "/tmp/project"}, Dependencies{
		Stdout: &bytes.Buffer{},
		Stderr: &stderr,
		Upgrade: func(string, bool) error {
			invoked = true
			return nil
		},
	})

	if exitCode == 0 {
		t.Fatal("Run() exit code = 0, want confirmation failure")
	}
	if invoked {
		t.Fatal("upgrade dependency invoked without --yes")
	}
	if !bytes.Contains(stderr.Bytes(), []byte("--yes")) {
		t.Errorf("stderr = %q, want --yes guidance", stderr.String())
	}
}

// A forgotten project path must not be read as a project literally named
// "--yes", which would surface as an unrelated path-resolution failure.
func TestUpgradeRejectsAConfirmationWithoutAProjectPath(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	invoked := false
	mutated := false
	exitCode := Run([]string{"upgrade", "--yes"}, Dependencies{
		Stdout: &bytes.Buffer{},
		Stderr: &stderr,
		Upgrade: func(string, bool) error {
			invoked = true
			return nil
		},
		OnMutate: func() {
			mutated = true
		},
	})

	if exitCode != 2 {
		t.Fatalf("Run() exit code = %d, want 2", exitCode)
	}
	if invoked || mutated {
		t.Fatalf("upgrade ran without a project path: invoked = %t, mutated = %t", invoked, mutated)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("pass the project path")) {
		t.Errorf("stderr = %q, want guidance about the missing project path", stderr.String())
	}
}

func TestUpgradeReportsAnUnavailableCommand(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	exitCode := Run([]string{"upgrade", "/tmp/project"}, Dependencies{
		Stdout: &bytes.Buffer{},
		Stderr: &stderr,
	})

	if exitCode != 1 {
		t.Fatalf("Run() exit code = %d, want 1", exitCode)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("unavailable")) {
		t.Errorf("stderr = %q, want an unavailable-command message", stderr.String())
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
