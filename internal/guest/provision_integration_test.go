package guest_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
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
	"github.com/fxmartin/isolated-dev/internal/state"
)

// TestHostProvisionsGuestIdentityAndCredentials is the destructive host check
// for this story: it creates one uniquely named machine, provisions it, and
// verifies guest ownership, credentials, and the absence of private keys.
func TestHostProvisionsGuestIdentityAndCredentials(t *testing.T) {
	if os.Getenv("ISOLATED_DEV_RUN_HOST_TESTS") != "1" {
		t.Skip("set ISOLATED_DEV_RUN_HOST_TESTS=1 to run the Apple Container guest test")
	}
	if runtime.GOOS != "darwin" {
		t.Fatal("Apple Container guest test requires macOS")
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
	repository, err := os.MkdirTemp(canonicalHome, ".isolated-dev-guest-test-*")
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
	publicKey := generateThrowawayPublicKey(t)

	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("rand.Read() error = %v", err)
	}
	machineName := "isolated-dev-guest-" + hex.EncodeToString(suffix[:])

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

	provisioner := guest.Provisioner{Runner: runner}
	request := guest.Request{
		MachineName: machineName,
		ProjectPath: repository,
		HomeDir:     canonicalHome,
		Identity:    identity,
		PublicKeys:  []string{publicKey},
	}
	result, err := provisioner.Provision(ctx, request)
	if err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	// Rerunning must converge rather than fail on the existing account.
	if _, err := provisioner.Provision(ctx, request); err != nil {
		t.Fatalf("second Provision() error = %v", err)
	}
	if !result.OwnershipMatched {
		t.Errorf("mounted project %s is not owned by the guest user", result.GuestProjectPath)
	}

	user := identity.Username
	if got := strings.TrimSpace(string(runInGuest(
		t, ctx, runner, machineName, "/usr/bin/id", "-u", user,
	))); got != fmt.Sprintf("%d", identity.UID) {
		t.Errorf("guest UID = %s, want %d", got, identity.UID)
	}
	if got := strings.TrimSpace(string(runInGuest(
		t, ctx, runner, machineName, "/usr/bin/id", "-g", user,
	))); got != fmt.Sprintf("%d", identity.GID) {
		t.Errorf("guest GID = %s, want %d", got, identity.GID)
	}
	if groups := string(runInGuest(
		t, ctx, runner, machineName, "/usr/bin/id", "-nG", user,
	)); !strings.Contains(groups, "docker") {
		t.Errorf("guest groups = %q, want docker membership", groups)
	}

	runInGuest(t, ctx, runner, machineName, "/usr/bin/su", "-", user, "-c", "sudo -n true")

	sshdConfig := string(runInGuest(
		t, ctx, runner, machineName,
		"/usr/bin/cat", "/etc/ssh/sshd_config.d/10-isolated-dev.conf",
	))
	for _, directive := range []string{
		"PermitRootLogin no",
		"PasswordAuthentication no",
		"AllowAgentForwarding yes",
		"AllowUsers " + user,
	} {
		if !strings.Contains(sshdConfig, directive) {
			t.Errorf("sshd drop-in missing %q:\n%s", directive, sshdConfig)
		}
	}
	effective := strings.ToLower(string(runInGuest(
		t, ctx, runner, machineName, "/usr/sbin/sshd", "-T",
	)))
	for _, directive := range []string{
		"permitrootlogin no",
		"passwordauthentication no",
		"allowagentforwarding yes",
	} {
		if !strings.Contains(effective, directive) {
			t.Errorf("effective sshd configuration missing %q", directive)
		}
	}

	authorized := "/home/" + user + "/.ssh/authorized_keys"
	if got := string(runInGuest(
		t, ctx, runner, machineName, "/usr/bin/cat", authorized,
	)); !strings.Contains(got, publicKey) {
		t.Errorf("authorized_keys = %q, want the host public key", got)
	}
	if got := strings.TrimSpace(string(runInGuest(
		t, ctx, runner, machineName, "/usr/bin/stat", "-c", "%U:%a", authorized,
	))); got != user+":600" {
		t.Errorf("authorized_keys ownership = %q, want %s:600", got, user)
	}
	runInGuest(
		t, ctx, runner, machineName,
		"/usr/bin/bash", "-c",
		"! grep -rq 'PRIVATE KEY' /home/"+user+"/.ssh /etc/ssh/sshd_config.d",
	)

	marker := ".guest-created"
	link := ".guest-link"
	runInGuest(
		t, ctx, runner, machineName,
		"/usr/bin/su", "-", user, "-c",
		fmt.Sprintf(
			"touch %[1]s && chmod 0755 %[1]s && ln -sfn %[2]s %[3]s",
			filepath.Join(result.GuestProjectPath, marker),
			marker,
			filepath.Join(result.GuestProjectPath, link),
		),
	)
	info, err := os.Stat(filepath.Join(repository, marker))
	if err != nil {
		t.Fatalf("guest-created file is not visible on macOS: %v", err)
	}
	if info.IsDir() {
		t.Fatalf("guest-created path %s is a directory", marker)
	}
	// AC4: the executable bit and the symlink must survive the mount.
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("guest-created file mode = %v, want the executable bit preserved", info.Mode().Perm())
	}
	linkInfo, err := os.Lstat(filepath.Join(repository, link))
	if err != nil {
		t.Fatalf("guest-created symlink is not visible on macOS: %v", err)
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Errorf("guest-created path %s mode = %v, want a symlink", link, linkInfo.Mode())
	}
	if target, err := os.Readlink(filepath.Join(repository, link)); err != nil {
		t.Fatalf("Readlink() error = %v", err)
	} else if target != marker {
		t.Errorf("symlink target = %q, want %q", target, marker)
	}
}

func generateThrowawayPublicKey(t *testing.T) string {
	t.Helper()

	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if output, err := exec.Command(
		"ssh-keygen", "-t", "ed25519", "-N", "", "-C", "isolated-dev-guest-test", "-f", keyPath,
	).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen error = %v\n%s", err, output)
	}
	data, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	return strings.TrimSpace(string(data))
}

func runInGuest(
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
