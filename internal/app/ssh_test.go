package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fxmartin/isolated-dev/internal/machine"
	"github.com/fxmartin/isolated-dev/internal/project"
	"github.com/fxmartin/isolated-dev/internal/sshconfig"
)

type addressStub struct {
	targets []machine.Target
	address string
	err     error
}

func (stub *addressStub) Address(
	_ context.Context,
	target machine.Target,
) (string, error) {
	stub.targets = append(stub.targets, target)
	address := stub.address
	if address == "" {
		address = "192.168.64.5"
	}
	return address, stub.err
}

type sshStub struct {
	applied   []sshconfig.Entry
	removed   []string
	forgotten []string
	applyErr  error
	removeErr error
	forgetErr error
}

func (stub *sshStub) Apply(entry sshconfig.Entry) error {
	stub.applied = append(stub.applied, entry)
	return stub.applyErr
}

func (stub *sshStub) Remove(alias string) error {
	stub.removed = append(stub.removed, alias)
	return stub.removeErr
}

func (stub *sshStub) ForgetHostKey(alias string) error {
	stub.forgotten = append(stub.forgotten, alias)
	return stub.forgetErr
}

// sshRepository prepares a repository inside home the way `up` requires.
func sshRepository(t *testing.T, home string) (string, string) {
	t.Helper()

	repository := filepath.Join(home, "app")
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	resolved, err := project.Resolve(repository)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	return repository, resolved.MachineName
}

func TestUpConfiguresTheManagedSSHHostForZed(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository, machineName := sshRepository(t, home)
	sshManager := &sshStub{}
	resolver := &addressStub{address: "192.168.64.5"}
	application := upApp(t, home, repository, &lifecycleStub{})
	application.SSHConfig = sshManager
	application.AddressResolver = resolver

	var summary bytes.Buffer
	if err := application.Up(context.Background(), repository, &summary); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	if len(sshManager.applied) != 1 {
		t.Fatalf("applied entries = %+v, want one", sshManager.applied)
	}
	want := sshconfig.Entry{Alias: machineName, HostName: "192.168.64.5", User: "fx"}
	if sshManager.applied[0] != want {
		t.Errorf("entry = %+v, want %+v", sshManager.applied[0], want)
	}
	if len(resolver.targets) != 1 || resolver.targets[0].MachineName != machineName {
		t.Errorf("address queries = %+v, want one for %q", resolver.targets, machineName)
	}
	if !strings.Contains(summary.String(), "ssh "+machineName+" (fx@192.168.64.5)") {
		t.Errorf("summary = %q, want the ready SSH host", summary.String())
	}

	stored, err := application.StateStore.Load(machineName)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if stored.SSHAddress != "192.168.64.5" {
		t.Errorf("SSHAddress = %q, want the resolved address", stored.SSHAddress)
	}
}

func TestUpForgetsHostKeysOnlyForANewlyCreatedMachine(t *testing.T) {
	t.Parallel()

	for name, existing := range map[string]bool{"created": false, "restarted": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			repository, machineName := sshRepository(t, home)
			sshManager := &sshStub{}
			application := upApp(t, home, repository, &lifecycleStub{upExisting: existing})
			application.SSHConfig = sshManager
			// A machine that moved address still has its host reconciled.
			application.AddressResolver = &addressStub{address: "192.168.64.7"}

			if err := application.Up(context.Background(), repository, io.Discard); err != nil {
				t.Fatalf("Up() error = %v", err)
			}
			if len(sshManager.applied) != 1 {
				t.Errorf("applied entries = %+v, want the host reconciled", sshManager.applied)
			}
			forgotten := len(sshManager.forgotten)
			if existing && forgotten != 0 {
				t.Errorf("forgotten = %v, want the host key of a running machine kept", sshManager.forgotten)
			}
			// A recreated machine answers under the same alias with a new host
			// key, so the stale one has to go.
			if !existing && (forgotten != 1 || sshManager.forgotten[0] != machineName) {
				t.Errorf("forgotten = %v, want the stale key of %q dropped", sshManager.forgotten, machineName)
			}
		})
	}
}

func TestUpReportsSSHReconciliationFailures(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		resolver   *addressStub
		sshManager *sshStub
		want       string
	}{
		"address unresolvable": {
			resolver:   &addressStub{err: errors.New("machine is not running")},
			sshManager: &sshStub{},
			want:       "machine is not running",
		},
		"stale host key kept": {
			resolver:   &addressStub{},
			sshManager: &sshStub{forgetErr: errors.New("known hosts is read-only")},
			want:       "known hosts is read-only",
		},
		"configuration unwritable": {
			resolver:   &addressStub{},
			sshManager: &sshStub{applyErr: errors.New("permission denied")},
			want:       "permission denied",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			repository, _ := sshRepository(t, home)
			application := upApp(t, home, repository, &lifecycleStub{})
			application.SSHConfig = testCase.sshManager
			application.AddressResolver = testCase.resolver

			err := application.Up(context.Background(), repository, io.Discard)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("Up() error = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestUpRequiresSSHAccessBeforeLifecycleMutation(t *testing.T) {
	t.Parallel()

	cases := map[string]func(*App){
		"no SSH configuration": func(application *App) { application.SSHConfig = nil },
		"no address resolver":  func(application *App) { application.AddressResolver = nil },
	}
	for name, disable := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			repository, _ := sshRepository(t, home)
			lifecycle := &lifecycleStub{}
			application := upApp(t, home, repository, lifecycle)
			disable(&application)

			err := application.Up(context.Background(), repository, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "SSH access is not configured") {
				t.Fatalf("Up() error = %v, want an SSH configuration rejection", err)
			}
			if len(lifecycle.upRequests) != 0 {
				t.Fatalf("up requests = %+v, want no lifecycle mutation", lifecycle.upRequests)
			}
		})
	}
}

func TestStatusReportsTheManagedSSHHost(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository, machineName := sshRepository(t, home)
	application := upApp(t, home, repository, &lifecycleStub{})
	if err := application.Up(context.Background(), repository, io.Discard); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	var output bytes.Buffer
	if err := application.Status(context.Background(), repository, &output); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	want := "SSH: " + machineName + " (fx@192.168.64.5)"
	if !strings.Contains(output.String(), want) {
		t.Errorf("status output missing %q:\n%s", want, output.String())
	}
}

func TestDestroyRemovesTheManagedSSHHost(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository, machineName := sshRepository(t, home)
	sshManager := &sshStub{}
	application := upApp(t, home, repository, &lifecycleStub{})
	application.SSHConfig = sshManager

	if err := application.Destroy(context.Background(), repository); err != nil {
		t.Fatalf("Destroy() error = %v", err)
	}
	if len(sshManager.removed) != 1 || sshManager.removed[0] != machineName {
		t.Errorf("removed = %v, want the host of %q dropped", sshManager.removed, machineName)
	}
}

func TestDestroyKeepsTheManagedSSHHostWhenTheMachineSurvives(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository, _ := sshRepository(t, home)
	sshManager := &sshStub{}
	application := upApp(t, home, repository, &lifecycleStub{
		destroyErr: errors.New("machine is in use"),
	})
	application.SSHConfig = sshManager

	if err := application.Destroy(context.Background(), repository); err == nil {
		t.Fatal("Destroy() error = nil, want the lifecycle failure reported")
	}
	if len(sshManager.removed) != 0 {
		t.Errorf("removed = %v, want the host kept for the surviving machine", sshManager.removed)
	}
}

func TestDestroyReportsFailedSSHCleanup(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository, _ := sshRepository(t, home)
	application := upApp(t, home, repository, &lifecycleStub{})
	application.SSHConfig = &sshStub{removeErr: errors.New("permission denied")}

	err := application.Destroy(context.Background(), repository)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("Destroy() error = %v, want the cleanup failure reported", err)
	}
}

func TestDestroyRequiresSSHConfiguration(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository, _ := sshRepository(t, home)
	lifecycle := &lifecycleStub{}
	application := upApp(t, home, repository, lifecycle)
	application.SSHConfig = nil

	err := application.Destroy(context.Background(), repository)
	if err == nil || !strings.Contains(err.Error(), "SSH access is not configured") {
		t.Fatalf("Destroy() error = %v, want an SSH configuration rejection", err)
	}
	if len(lifecycle.destroyed) != 0 {
		t.Fatalf("destroyed = %+v, want no lifecycle mutation", lifecycle.destroyed)
	}
}
