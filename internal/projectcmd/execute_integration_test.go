package projectcmd_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fxmartin/isolated-dev/internal/baseimage"
	"github.com/fxmartin/isolated-dev/internal/config"
	"github.com/fxmartin/isolated-dev/internal/guest"
	"github.com/fxmartin/isolated-dev/internal/machine"
	"github.com/fxmartin/isolated-dev/internal/projectcmd"
	"github.com/fxmartin/isolated-dev/internal/state"
)

// TestHostRunsDeclaredCommandsInTheGuest is the destructive host check for this
// story. It creates one uniquely named machine and runs declared commands
// through the same path `isolated-dev run` takes, which is what proves the
// guest actually drops to the non-root user, lands in the mounted project, and
// hands back the command's own exit status.
func TestHostRunsDeclaredCommandsInTheGuest(t *testing.T) {
	if os.Getenv("ISOLATED_DEV_RUN_HOST_TESTS") != "1" {
		t.Skip("set ISOLATED_DEV_RUN_HOST_TESTS=1 to run the Apple Container command test")
	}
	if runtime.GOOS != "darwin" {
		t.Fatal("Apple Container command test requires macOS")
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
	canonicalHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}
	repository, err := os.MkdirTemp(canonicalHome, ".isolated-dev-command-test-*")
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
	if err := os.Mkdir(filepath.Join(repository, "service"), 0o755); err != nil {
		t.Fatalf("Mkdir(service) error = %v", err)
	}

	identity, err := guest.ResolveIdentity()
	if err != nil {
		t.Fatalf("ResolveIdentity() error = %v", err)
	}
	publicKey := throwawayPublicKey(t)

	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("rand.Read() error = %v", err)
	}
	machineName := "isolated-dev-command-" + hex.EncodeToString(suffix[:])

	runner := baseimage.ExecRunner{}
	imageManager := &baseimage.Manager{Runner: runner}
	manager := machine.Manager{
		Runner:       runner,
		StateStore:   state.Store{Root: t.TempDir()},
		DockerWaiter: imageManager,
		ImageEnsurer: imageManager,
		BootTries:    10,
		RetryDelay:   time.Second,
	}
	target := machine.Target{ProjectPath: repository, MachineName: machineName}
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

	if _, err := manager.Up(ctx, machine.Request{
		ProjectPath:      repository,
		MachineName:      machineName,
		BaseImage:        "local/isolated-dev-base:1",
		BaseImageVersion: "1",
		CPUs:             2,
		MemoryGB:         4,
		MountScope:       "home",
	}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	provisioned, err := guest.Provisioner{Runner: runner}.Provision(ctx, guest.Request{
		MachineName: machineName,
		ProjectPath: repository,
		HomeDir:     canonicalHome,
		Identity:    identity,
		PublicKeys:  []string{publicKey},
	})
	if err != nil {
		t.Fatalf("Provision() error = %v", err)
	}

	executor := projectcmd.Executor{
		Runner:       projectcmd.ExecRunner{},
		DockerWaiter: imageManager,
	}
	request := func(command config.Command) projectcmd.Request {
		return projectcmd.Request{
			MachineName:      machineName,
			GuestUser:        provisioned.Identity.Username,
			GuestProjectPath: provisioned.GuestProjectPath,
			Name:             "check",
			Command:          command,
		}
	}

	// The command must run as the provisioned non-root user, in the mounted
	// project, with a usable PATH.
	var stdout, stderr bytes.Buffer
	exitCode, err := executor.Execute(
		ctx,
		request(config.Command{Args: []string{"sh", "-c", "id -u; pwd"}}),
		projectcmd.Streams{Stdout: &stdout, Stderr: &stderr},
	)
	if err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, stderr.String())
	}
	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0\n%s", exitCode, stderr.String())
	}
	lines := strings.Fields(stdout.String())
	if len(lines) != 2 {
		t.Fatalf("output = %q, want the guest UID and working directory", stdout.String())
	}
	if lines[0] != strconv.Itoa(identity.UID) {
		t.Errorf("guest UID = %s, want the non-root %d", lines[0], identity.UID)
	}
	if lines[1] != provisioned.GuestProjectPath {
		t.Errorf("working directory = %s, want %s", lines[1], provisioned.GuestProjectPath)
	}

	// The declared workdir must be honoured relative to the mounted project.
	stdout.Reset()
	if _, err := executor.Execute(
		ctx,
		request(config.Command{Args: []string{"pwd"}, Workdir: "service"}),
		projectcmd.Streams{Stdout: &stdout, Stderr: &stderr},
	); err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != provisioned.GuestProjectPath+"/service" {
		t.Errorf("workdir = %q, want the declared subdirectory", got)
	}

	// A failing command reports its own exit status and stderr, not a wrapper's.
	stdout.Reset()
	stderr.Reset()
	exitCode, err = executor.Execute(
		ctx,
		request(config.Command{Args: []string{"sh", "-c", "echo boom >&2; exit 17"}}),
		projectcmd.Streams{Stdout: &stdout, Stderr: &stderr},
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if exitCode != 17 {
		t.Errorf("exit code = %d, want the guest exit status 17", exitCode)
	}
	if !strings.Contains(stderr.String(), "boom") {
		t.Errorf("stderr = %q, want the guest stderr", stderr.String())
	}

	// A Compose command waits for `docker info` first, and the guest user runs
	// Docker without sudo through its docker-group membership.
	stdout.Reset()
	stderr.Reset()
	if _, err := executor.Execute(
		ctx,
		request(config.Command{Args: []string{"docker", "compose", "version"}, Compose: true}),
		projectcmd.Streams{Stdout: &stdout, Stderr: &stderr},
	); err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, stderr.String())
	}
	if !strings.Contains(strings.ToLower(stdout.String()), "compose") {
		t.Errorf("compose output = %q, want the Compose version", stdout.String())
	}
}

func throwawayPublicKey(t *testing.T) string {
	t.Helper()

	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if output, err := exec.Command(
		"ssh-keygen", "-t", "ed25519", "-N", "", "-C", "isolated-dev-command-test", "-f", keyPath,
	).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen error = %v\n%s", err, output)
	}
	data, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	return strings.TrimSpace(string(data))
}
