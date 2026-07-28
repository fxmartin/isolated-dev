package machine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/fxmartin/isolated-dev/internal/baseimage"
	"github.com/fxmartin/isolated-dev/internal/state"
)

func TestHostLifecyclePersistsProjectMachineData(t *testing.T) {
	if os.Getenv("ISOLATED_DEV_RUN_HOST_TESTS") != "1" {
		t.Skip("set ISOLATED_DEV_RUN_HOST_TESTS=1 to run the Apple Container lifecycle test")
	}
	if runtime.GOOS != "darwin" {
		t.Fatal("Apple Container lifecycle test requires macOS")
	}
	if _, err := exec.LookPath("container"); err != nil {
		t.Fatalf("find container CLI: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir() error = %v", err)
	}
	repository, err := os.MkdirTemp(home, ".isolated-dev-lifecycle-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(repository); err != nil {
			t.Errorf("remove test repository: %v", err)
		}
	})
	if err := os.Mkdir(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}

	suffix := makeIntegrationSuffix(t)
	machineName := "isolated-dev-lifecycle-" + suffix
	volumeName := "isolated-dev-lifecycle-" + suffix
	composePath := filepath.Join(repository, "compose.lifecycle-test.yaml")
	compose := fmt.Sprintf(`services:
  seed:
    image: busybox:1.36
    command: ["sh", "-c", "printf volume-data > /data/marker"]
    volumes:
      - persistence:/data
volumes:
  persistence:
    name: %s
`, volumeName)
	if err := os.WriteFile(composePath, []byte(compose), 0o600); err != nil {
		t.Fatalf("WriteFile(compose) error = %v", err)
	}

	runner := baseimage.ExecRunner{}
	imageManager := &baseimage.Manager{Runner: runner}
	store := state.Store{Root: t.TempDir()}
	manager := Manager{
		Runner:       runner,
		StateStore:   store,
		DockerWaiter: imageManager,
		ImageEnsurer: imageManager,
		BootTries:    10,
		RetryDelay:   time.Second,
	}
	request := Request{
		ProjectPath:      repository,
		MachineName:      machineName,
		BaseImage:        "local/isolated-dev-base:1",
		BaseImageVersion: "1",
		CPUs:             2,
		MemoryGB:         4,
		MountScope:       "home",
	}
	target := Target{ProjectPath: repository, MachineName: machineName}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		if err := manager.Destroy(cleanupCtx, target); err == nil {
			return
		}
		_, _ = runner.Run(cleanupCtx, "container", "machine", "stop", machineName)
		if output, err := runner.Run(
			cleanupCtx,
			"container",
			"machine", "delete", machineName,
		); err != nil && !strings.Contains(string(output), "not found") {
			t.Errorf("fallback machine cleanup failed: %v\n%s", err, output)
		}
	})

	result, err := manager.Up(ctx, request)
	if err != nil {
		t.Fatalf("initial Up() error = %v", err)
	}
	if !result.Created {
		t.Fatal("initial Up() Created = false, want true")
	}

	runInMachine(t, ctx, runner, machineName, "/usr/bin/apt-get", "update")
	runInMachine(
		t, ctx, runner, machineName,
		"/usr/bin/apt-get", "install", "-y", "--no-install-recommends", "tree",
	)
	runInMachine(
		t, ctx, runner, machineName,
		"/usr/bin/mkdir", "-p", "/var/lib/isolated-dev-lifecycle",
	)
	runInMachine(
		t, ctx, runner, machineName,
		"/usr/bin/touch", "/var/lib/isolated-dev-lifecycle/marker",
	)
	runInMachine(
		t, ctx, runner, machineName,
		"/usr/bin/touch", filepath.Join(repository, ".lifecycle-persistence"),
	)
	runInMachine(
		t, ctx, runner, machineName,
		"/usr/bin/docker", "compose", "-f", composePath, "run", "--rm", "seed",
	)

	if err := manager.Stop(ctx, target); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	result, err = manager.Up(ctx, request)
	if err != nil {
		t.Fatalf("restart Up() error = %v", err)
	}
	if result.Created {
		t.Fatal("restart Up() Created = true, want existing machine reuse")
	}

	runInMachine(t, ctx, runner, machineName, "/usr/bin/dpkg-query", "-W", "tree")
	runInMachine(
		t, ctx, runner, machineName,
		"/usr/bin/test", "-f", "/var/lib/isolated-dev-lifecycle/marker",
	)
	runInMachine(
		t, ctx, runner, machineName,
		"/usr/bin/test", "-f", filepath.Join(repository, ".lifecycle-persistence"),
	)
	runInMachine(
		t, ctx, runner, machineName,
		"/usr/bin/docker", "image", "inspect", "busybox:1.36",
	)
	output := runInMachine(
		t, ctx, runner, machineName,
		"/usr/bin/docker", "run", "--rm", "-v", volumeName+":/data",
		"busybox:1.36", "cat", "/data/marker",
	)
	if !strings.Contains(string(output), "volume-data") {
		t.Fatalf("persistent volume output = %q, want volume-data", output)
	}

	if err := manager.Destroy(ctx, target); err != nil {
		t.Fatalf("Destroy() error = %v", err)
	}
	if _, err := store.Load(machineName); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("Load() after destroy error = %v, want ErrNotFound", err)
	}
}

func makeIntegrationSuffix(t *testing.T) string {
	t.Helper()

	var value [6]byte
	if _, err := rand.Read(value[:]); err != nil {
		t.Fatalf("rand.Read() error = %v", err)
	}
	return hex.EncodeToString(value[:])
}

func runInMachine(
	t *testing.T,
	ctx context.Context,
	runner baseimage.ExecRunner,
	machineName string,
	args ...string,
) []byte {
	t.Helper()

	commandArgs := []string{"machine", "run", "--name", machineName, "--root", "--"}
	commandArgs = append(commandArgs, args...)
	output, err := runner.Run(ctx, "container", commandArgs...)
	if err != nil {
		t.Fatalf("container %s error = %v\n%s", strings.Join(commandArgs, " "), err, output)
	}
	return output
}
