package sshconfig_test

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
	"github.com/fxmartin/isolated-dev/internal/sshconfig"
	"github.com/fxmartin/isolated-dev/internal/state"
)

// TestHostConnectsOverManagedSSH is the destructive host check for this story.
// It creates one uniquely named machine, configures the managed SSH host the
// way `up` does, and connects through the developer-facing configuration — the
// same path Zed's SSH remote development takes.
func TestHostConnectsOverManagedSSH(t *testing.T) {
	if os.Getenv("ISOLATED_DEV_RUN_HOST_TESTS") != "1" {
		t.Skip("set ISOLATED_DEV_RUN_HOST_TESTS=1 to run the Apple Container SSH test")
	}
	if runtime.GOOS != "darwin" {
		t.Fatal("Apple Container SSH test requires macOS")
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
	repository, err := os.MkdirTemp(canonicalHome, ".isolated-dev-ssh-test-*")
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

	identity, err := guest.ResolveIdentity()
	if err != nil {
		t.Fatalf("ResolveIdentity() error = %v", err)
	}
	privateKey := throwawayKey(t)

	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("rand.Read() error = %v", err)
	}
	machineName := "isolated-dev-ssh-" + hex.EncodeToString(suffix[:])

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
		PublicKeys:  []string{publicKeyOf(t, privateKey)},
	})
	if err != nil {
		t.Fatalf("Provision() error = %v", err)
	}

	address, err := manager.Address(ctx, target)
	if err != nil {
		t.Fatalf("Address() error = %v", err)
	}
	sshDir := filepath.Join(t.TempDir(), ".ssh")
	sshManager := sshconfig.Manager{SSHDir: sshDir}
	if err := sshManager.Apply(sshconfig.Entry{
		Alias:    machineName,
		HostName: address,
		User:     identity.Username,
	}); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	// The developer-facing file is the one Zed reads; it only reaches the
	// machine through the Include that Apply added.
	developerConfig := filepath.Join(sshDir, "config")
	login := sshInGuest(t, ctx, developerConfig, privateKey, machineName, "id -un")
	if login != identity.Username {
		t.Errorf("login = %q, want the guest user %q", login, identity.Username)
	}
	writable := sshInGuest(
		t, ctx, developerConfig, privateKey, machineName,
		"test -w "+provisioned.GuestProjectPath+" && echo writable",
	)
	if writable != "writable" {
		t.Errorf("mounted project = %q, want it writable over SSH", writable)
	}
	forwarding := sshInGuest(
		t, ctx, developerConfig, privateKey, machineName,
		"sshd -T 2>/dev/null | grep -c '^allowagentforwarding yes'",
	)
	if forwarding != "1" {
		t.Errorf("agent forwarding = %q, want it enabled for Git over SSH", forwarding)
	}

	// Host keys belong to the tool-owned file, never to the developer's.
	knownHosts, err := os.ReadFile(sshManager.KnownHostsPath())
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(knownHosts), machineName) {
		t.Errorf("managed known hosts = %q, want the machine host key", knownHosts)
	}
	if _, err := os.Stat(filepath.Join(sshDir, "known_hosts")); err == nil {
		t.Error("the developer known-hosts file was written")
	}

	if err := sshManager.Remove(machineName); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	remaining, err := os.ReadFile(sshManager.KnownHostsPath())
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if strings.Contains(string(remaining), machineName) {
		t.Errorf("known hosts = %q, want the destroyed machine forgotten", remaining)
	}
}

// throwawayKey generates a keypair that exists only for this test, so the
// developer's own keys are never involved.
func throwawayKey(t *testing.T) string {
	t.Helper()

	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if output, err := exec.Command(
		"ssh-keygen", "-t", "ed25519", "-N", "", "-C", "isolated-dev-ssh-test", "-f", keyPath,
	).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen error = %v\n%s", err, output)
	}
	return keyPath
}

func publicKeyOf(t *testing.T, privateKeyPath string) string {
	t.Helper()

	data, err := os.ReadFile(privateKeyPath + ".pub")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	return strings.TrimSpace(string(data))
}

func sshInGuest(
	t *testing.T,
	ctx context.Context,
	configPath string,
	privateKeyPath string,
	alias string,
	command string,
) string {
	t.Helper()

	output, err := exec.CommandContext(
		ctx,
		"ssh",
		"-F", configPath,
		"-i", privateKeyPath,
		"-o", "IdentitiesOnly=yes",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=15",
		alias,
		command,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("ssh %s %q error = %v\n%s", alias, command, err, output)
	}
	return strings.TrimSpace(string(output))
}
