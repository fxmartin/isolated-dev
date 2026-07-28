package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveCanonicalizesPathAndBuildsStableMachineName(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	repository := filepath.Join(parent, "My Web_App")
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	link := filepath.Join(parent, "linked-repository")
	if err := os.Symlink(repository, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	fromRealPath, err := Resolve(repository)
	if err != nil {
		t.Fatalf("Resolve(real) error = %v", err)
	}
	fromLink, err := Resolve(link)
	if err != nil {
		t.Fatalf("Resolve(link) error = %v", err)
	}

	if fromRealPath.Path != fromLink.Path {
		t.Errorf("canonical paths differ: %q != %q", fromRealPath.Path, fromLink.Path)
	}
	if fromRealPath.MachineName != fromLink.MachineName {
		t.Errorf("machine names differ: %q != %q", fromRealPath.MachineName, fromLink.MachineName)
	}
	if got, wantPrefix := fromRealPath.MachineName, "isolated-dev-my-web-app-"; len(got) <= len(wantPrefix) || got[:len(wantPrefix)] != wantPrefix {
		t.Errorf("MachineName = %q, want prefix %q and hash", got, wantPrefix)
	}
	if suffix := strings.TrimPrefix(
		fromRealPath.MachineName,
		"isolated-dev-my-web-app-",
	); len(suffix) != 16 {
		t.Errorf("MachineName hash = %q, want 16 hexadecimal characters", suffix)
	}
}

func TestResolveAvoidsSameBasenameCollision(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	first := makeRepository(t, filepath.Join(parent, "one", "app"))
	second := makeRepository(t, filepath.Join(parent, "two", "app"))

	firstProject, err := Resolve(first)
	if err != nil {
		t.Fatalf("Resolve(first) error = %v", err)
	}
	secondProject, err := Resolve(second)
	if err != nil {
		t.Fatalf("Resolve(second) error = %v", err)
	}

	if firstProject.MachineName == secondProject.MachineName {
		t.Fatalf("MachineName collision = %q", firstProject.MachineName)
	}
}

func TestResolveRejectsNonRepository(t *testing.T) {
	t.Parallel()

	_, err := Resolve(t.TempDir())
	if err == nil {
		t.Fatal("Resolve() error = nil, want repository validation error")
	}
}

func makeRepository(t *testing.T, path string) string {
	t.Helper()

	if err := os.MkdirAll(filepath.Join(path, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	return path
}
