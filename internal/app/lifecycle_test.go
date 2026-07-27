package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fxmartin/isolated-dev/internal/host"
	"github.com/fxmartin/isolated-dev/internal/machine"
)

type lifecycleStub struct {
	upRequests []machine.Request
	stopped    []string
	destroyed  []string
	upErr      error
	stopErr    error
	destroyErr error
}

func (lifecycle *lifecycleStub) Up(
	_ context.Context,
	request machine.Request,
) (machine.UpResult, error) {
	lifecycle.upRequests = append(lifecycle.upRequests, request)
	return machine.UpResult{Created: true}, lifecycle.upErr
}

func (lifecycle *lifecycleStub) Stop(_ context.Context, machineName string) error {
	lifecycle.stopped = append(lifecycle.stopped, machineName)
	return lifecycle.stopErr
}

func (lifecycle *lifecycleStub) Destroy(_ context.Context, machineName string) error {
	lifecycle.destroyed = append(lifecycle.destroyed, machineName)
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
	var warnings bytes.Buffer
	application := App{
		HostChecker:    passingHostChecker(),
		MachineManager: lifecycle,
		HomeDir:        home,
		WarningOutput:  &warnings,
	}

	result, err := application.Up(context.Background(), repository)
	if err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if !result.Created {
		t.Fatal("Up() Created = false, want true")
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

	_, err := application.Up(context.Background(), repository)
	if err == nil || !strings.Contains(err.Error(), "outside the mounted home directory") {
		t.Fatalf("Up() error = %v, want unsupported out-of-home repository guidance", err)
	}
	if len(lifecycle.upRequests) != 0 {
		t.Fatalf("up requests = %+v, want no lifecycle mutation", lifecycle.upRequests)
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
