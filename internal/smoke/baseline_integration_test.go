package smoke_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/fxmartin/isolated-dev/internal/baseimage"
	"github.com/fxmartin/isolated-dev/internal/guest"
	"github.com/fxmartin/isolated-dev/internal/machine"
	"github.com/fxmartin/isolated-dev/internal/smoke"
	"github.com/fxmartin/isolated-dev/internal/state"
)

// TestHostRunsTheBaselineNestedComposeWorkload is the automated baseline: it
// creates a fresh machine from a base image it builds itself, starts the two
// pinned images on a private Compose network, reads a macOS-authored marker
// back from inside the guest and from macOS through the published port, and
// removes every one of those resources again.
//
// It is destructive and slow — run it only on a disposable Apple Container
// development host.
func TestHostRunsTheBaselineNestedComposeWorkload(t *testing.T) {
	if os.Getenv("ISOLATED_DEV_RUN_HOST_TESTS") != "1" {
		t.Skip("set ISOLATED_DEV_RUN_HOST_TESTS=1 to run the baseline nested-Compose test")
	}
	if runtime.GOOS != "darwin" {
		t.Fatal("the baseline nested-Compose test requires macOS")
	}
	if _, err := exec.LookPath("container"); err != nil {
		t.Fatalf("find container CLI: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir() error = %v", err)
	}
	canonicalHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}
	identity, err := guest.ResolveIdentity()
	if err != nil {
		t.Fatalf("ResolveIdentity() error = %v", err)
	}

	suffix := randomSuffix(t)
	machineName := "isolated-dev-baseline-" + suffix
	request := smoke.Request{
		MachineName:      machineName,
		BaseImageVersion: "baseline-" + suffix,
		HomeDir:          canonicalHome,
		FixtureDir:       filepath.Join(canonicalHome, ".isolated-dev-baseline-"+suffix),
		GuestUser:        identity.Username,
		CPUs:             2,
		MemoryGB:         4,
		// A high fixed port keeps the published mapping predictable; it is bound
		// inside the machine, not on macOS, so it cannot collide with host
		// services.
		HostPort: 18080,
		Marker:   "baseline-" + suffix,
	}

	runner := baseimage.ExecRunner{}
	imageManager := &baseimage.Manager{Runner: runner}
	machineManager := machine.Manager{
		Runner:       runner,
		StateStore:   state.Store{Root: t.TempDir()},
		DockerWaiter: imageManager,
		ImageEnsurer: imageManager,
		BootTries:    10,
		RetryDelay:   time.Second,
	}
	reference := "local/isolated-dev-base:" + request.BaseImageVersion
	// Teardown is part of what this test asserts, so this is only a safety net
	// for a run that dies before it: every command here is expected to find
	// nothing left to do.
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cleanupCancel()
		_, _ = runner.Run(cleanupCtx, "container", "machine", "stop", machineName)
		if output, err := runner.Run(
			cleanupCtx, "container", "machine", "delete", machineName,
		); err != nil && !strings.Contains(string(output), "not found") {
			t.Errorf("fallback machine cleanup failed: %v\n%s", err, output)
		}
		_, _ = runner.Run(cleanupCtx, "container", "image", "delete", reference)
		if err := os.RemoveAll(request.FixtureDir); err != nil {
			t.Errorf("fallback fixture cleanup failed: %v", err)
		}
	})

	test := smoke.Test{
		Runner:       runner,
		Machines:     machineManager,
		ImageEnsurer: imageManager,
		DockerWaiter: imageManager,
		Address:      machineManager,
		Prober:       smoke.HTTPProber{},
		RetryDelay:   2 * time.Second,
		Diagnostics:  testWriter{t: t},
	}

	result, err := test.Run(ctx, request)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.NetworkDriver != "bridge" || result.AttachedContainers != 2 {
		t.Errorf(
			"network %s = %q driver with %d containers, want a bridge carrying both services",
			result.NetworkName,
			result.NetworkDriver,
			result.AttachedContainers,
		)
	}
	if result.GuestMarker != request.Marker {
		t.Errorf("guest marker = %q, want %q", result.GuestMarker, request.Marker)
	}
	if result.HostMarker != request.Marker {
		t.Errorf("macOS marker = %q at %s, want %q", result.HostMarker, result.HostURL, request.Marker)
	}

	// Teardown removed exactly what the baseline created.
	if output, err := runner.Run(ctx, "container", "machine", "list", "--format", "json"); err != nil {
		t.Fatalf("machine list error = %v\n%s", err, output)
	} else if strings.Contains(string(output), machineName) {
		t.Errorf("machine %s survived teardown:\n%s", machineName, output)
	}
	if _, err := runner.Run(ctx, "container", "image", "inspect", reference); err == nil {
		t.Errorf("base image %s survived teardown", reference)
	}
	if _, err := os.Stat(request.FixtureDir); !os.IsNotExist(err) {
		t.Errorf("Stat(fixtures) error = %v, want them removed", err)
	}
}

func randomSuffix(t *testing.T) string {
	t.Helper()

	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("rand.Read() error = %v", err)
	}
	return hex.EncodeToString(suffix[:])
}

// testWriter forwards captured diagnostics into the test log, where a failing
// unattended run can still be read afterwards.
type testWriter struct {
	t *testing.T
}

func (writer testWriter) Write(data []byte) (int, error) {
	writer.t.Logf("%s", strings.TrimRight(string(data), "\n"))
	return len(data), nil
}
