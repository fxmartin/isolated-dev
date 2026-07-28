package app

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fxmartin/isolated-dev/internal/host"
	"github.com/fxmartin/isolated-dev/internal/machine"
)

type lifecycleStub struct {
	upRequests []machine.Request
	stopped    []machine.Target
	destroyed  []machine.Target
	upErr      error
	stopErr    error
	destroyErr error
	upExisting bool
}

func (lifecycle *lifecycleStub) Up(
	_ context.Context,
	request machine.Request,
) (machine.UpResult, error) {
	lifecycle.upRequests = append(lifecycle.upRequests, request)
	return machine.UpResult{Created: !lifecycle.upExisting}, lifecycle.upErr
}

func (lifecycle *lifecycleStub) Stop(_ context.Context, target machine.Target) error {
	lifecycle.stopped = append(lifecycle.stopped, target)
	return lifecycle.stopErr
}

func (lifecycle *lifecycleStub) Destroy(_ context.Context, target machine.Target) error {
	lifecycle.destroyed = append(lifecycle.destroyed, target)
	return lifecycle.destroyErr
}

func TestUpResolvesProjectAndUsesEffectiveResources(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository := filepath.Join(home, "app")
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(repository, ".isolated-dev.toml"),
		[]byte("version = 1\n[resources]\ncpus = 6\nmemory_gb = 12\n"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	lifecycle := &lifecycleStub{}
	var warnings, summary bytes.Buffer
	application := App{
		HostChecker:    passingHostChecker(),
		MachineManager: lifecycle,
		HomeDir:        home,
		WarningOutput:  &warnings,
	}

	if err := application.Up(context.Background(), repository, &summary); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if len(lifecycle.upRequests) != 1 {
		t.Fatalf("up requests = %+v", lifecycle.upRequests)
	}
	request := lifecycle.upRequests[0]
	canonicalRepository, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}
	if request.ProjectPath != canonicalRepository {
		t.Errorf("ProjectPath = %q, want %q", request.ProjectPath, canonicalRepository)
	}
	if got, want := summary.String(), "created "+canonicalRepository+"\n"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
	if request.CPUs != 6 || request.MemoryGB != 12 {
		t.Errorf("resources = %d CPU/%d GB, want 6 CPU/12 GB", request.CPUs, request.MemoryGB)
	}
	if request.MountScope != "home" {
		t.Errorf("MountScope = %q, want home fallback", request.MountScope)
	}
	if !strings.Contains(warnings.String(), "read-write access to your full home directory") {
		t.Errorf("warning = %q, want full-home exposure warning", warnings.String())
	}
}

func TestUpRejectsRepositoryOutsideHomeBeforeLifecycleMutation(t *testing.T) {
	t.Parallel()

	repository := t.TempDir()
	if err := os.Mkdir(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	lifecycle := &lifecycleStub{}
	application := App{
		HostChecker:    passingHostChecker(),
		MachineManager: lifecycle,
		HomeDir:        t.TempDir(),
	}

	err := application.Up(context.Background(), repository, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "outside the mounted home directory") {
		t.Fatalf("Up() error = %v, want unsupported out-of-home repository guidance", err)
	}
	if len(lifecycle.upRequests) != 0 {
		t.Fatalf("up requests = %+v, want no lifecycle mutation", lifecycle.upRequests)
	}
}

func TestUpRejectsUnmanagedBaseImageBeforeLifecycleMutation(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository := filepath.Join(home, "app")
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(repository, ".isolated-dev.toml"),
		[]byte("version = 1\nbase_image = \"registry.example/attacker:latest\"\n"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	lifecycle := &lifecycleStub{}
	var warnings bytes.Buffer
	application := App{
		HostChecker:    passingHostChecker(),
		MachineManager: lifecycle,
		HomeDir:        home,
		WarningOutput:  &warnings,
	}

	err := application.Up(context.Background(), repository, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "not a managed isolated-dev image") {
		t.Fatalf("Up() error = %v, want unmanaged image rejection", err)
	}
	if len(lifecycle.upRequests) != 0 {
		t.Fatalf("up requests = %+v, want no lifecycle mutation", lifecycle.upRequests)
	}
	if warnings.Len() != 0 {
		t.Fatalf("warning = %q, want rejection before mount warning", warnings.String())
	}
}

func passingHostChecker() host.Checker {
	return host.Checker{
		LookPath: func(name string) (string, error) {
			return "/usr/bin/" + name, nil
		},
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("container CLI version 1.1.0"), nil
		},
	}
}
