package app

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fxmartin/isolated-dev/internal/config"
	"github.com/fxmartin/isolated-dev/internal/guest"
	"github.com/fxmartin/isolated-dev/internal/host"
	"github.com/fxmartin/isolated-dev/internal/machine"
	"github.com/fxmartin/isolated-dev/internal/project"
	"github.com/fxmartin/isolated-dev/internal/state"
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

const testPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK7Q3rP0m3xnKcQ9pR6l3n3v1wq1V7pQvXvJ0uZ0aAaA fx@mac"

var testIdentity = guest.Identity{Username: "fx", UID: 501, GID: 20}

type guestStub struct {
	requests         []guest.Request
	err              error
	ownershipMissing bool
	guestPath        string
}

func (stub *guestStub) Provision(
	_ context.Context,
	request guest.Request,
) (guest.Result, error) {
	stub.requests = append(stub.requests, request)
	guestPath := stub.guestPath
	if guestPath == "" {
		guestPath = request.ProjectPath
	}
	return guest.Result{
		Identity:         request.Identity,
		GuestProjectPath: guestPath,
		OwnershipMatched: !stub.ownershipMissing,
	}, stub.err
}

// upApp assembles the collaborators `up` requires: a host public key, the
// project state a created machine records, and a guest provisioner.
func upApp(t *testing.T, home string, repository string, lifecycle MachineManager) App {
	t.Helper()

	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(home, ".ssh", "id_ed25519.pub"),
		[]byte(testPublicKey+"\n"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return App{
		HostChecker:      passingHostChecker(),
		MachineManager:   lifecycle,
		GuestProvisioner: &guestStub{},
		StateStore:       seededStateStore(t, repository),
		AddressResolver:  &addressStub{},
		SSHConfig:        &sshStub{},
		Tunnels:          &tunnelStub{},
		Zed:              &zedStub{},
		HomeDir:          home,
		ResolveIdentity:  func() (guest.Identity, error) { return testIdentity, nil },
	}
}

// seededStateStore mirrors the record `machine.Manager` writes when it creates
// a project machine.
func seededStateStore(t *testing.T, repository string) state.Store {
	t.Helper()

	store := state.Store{Root: t.TempDir()}
	resolved, err := project.Resolve(repository)
	if err != nil {
		return store
	}
	if err := store.Save(state.Project{
		SchemaVersion:    1,
		ProjectPath:      resolved.Path,
		MachineName:      resolved.MachineName,
		BaseImage:        config.DefaultBaseImage,
		BaseImageVersion: "1",
		MountScope:       "home",
		CPUs:             4,
		MemoryGB:         8,
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	return store
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
	application := upApp(t, home, repository, lifecycle)
	application.WarningOutput = &warnings

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
	resolved, err := project.Resolve(repository)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	wantSummary := "created " + canonicalRepository + "\n" +
		"guest fx (501:20) at " + canonicalRepository + "\n" +
		"ssh " + resolved.MachineName + " (fx@192.168.64.5)\n"
	if got := summary.String(); got != wantSummary {
		t.Errorf("summary = %q, want %q", got, wantSummary)
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
	application := upApp(t, t.TempDir(), repository, lifecycle)

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
	application := upApp(t, home, repository, lifecycle)
	application.WarningOutput = &warnings

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

func TestUpProvisionsTheGuestIdentityAndRecordsIt(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository := filepath.Join(home, "app")
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	provisioner := &guestStub{guestPath: "/home/fx/app"}
	application := upApp(t, home, repository, &lifecycleStub{})
	application.GuestProvisioner = provisioner
	canonicalHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}

	if err := application.Up(context.Background(), repository, io.Discard); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if len(provisioner.requests) != 1 {
		t.Fatalf("guest requests = %+v, want one", provisioner.requests)
	}
	request := provisioner.requests[0]
	if request.Identity != testIdentity {
		t.Errorf("Identity = %+v, want the invoking macOS identity %+v", request.Identity, testIdentity)
	}
	if request.HomeDir != canonicalHome {
		t.Errorf("HomeDir = %q, want %q", request.HomeDir, canonicalHome)
	}
	if len(request.PublicKeys) != 1 || request.PublicKeys[0] != testPublicKey {
		t.Errorf("PublicKeys = %#v, want the host public key", request.PublicKeys)
	}

	resolved, err := project.Resolve(repository)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	stored, err := application.StateStore.Load(resolved.MachineName)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if stored.GuestUser != "fx" || stored.GuestUID != 501 || stored.GuestGID != 20 {
		t.Errorf("stored identity = %+v, want the provisioned guest user", stored)
	}
	if stored.GuestProjectPath != "/home/fx/app" {
		t.Errorf("GuestProjectPath = %q, want the discovered guest path", stored.GuestProjectPath)
	}
	if stored.BaseImage != config.DefaultBaseImage || stored.CPUs != 4 {
		t.Errorf("stored state = %+v, want pinned lifecycle fields preserved", stored)
	}
}

func TestUpWarnsWhenTheMountedProjectIsNotOwnedByTheGuestUser(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository := filepath.Join(home, "app")
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	var warnings bytes.Buffer
	application := upApp(t, home, repository, &lifecycleStub{})
	application.GuestProvisioner = &guestStub{ownershipMissing: true}
	application.WarningOutput = &warnings

	if err := application.Up(context.Background(), repository, io.Discard); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if !strings.Contains(warnings.String(), "is not owned by fx (501:20)") {
		t.Errorf("warnings = %q, want a mount ownership warning", warnings.String())
	}
}

func TestUpChecksSecretFileReferencesWithoutReadingThem(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository := filepath.Join(home, "app")
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(repository, ".isolated-dev.toml"),
		[]byte("version = 1\n[secrets]\nfiles = [\".env\", \"missing.env\"]\n"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(repository, ".env"),
		[]byte("API_TOKEN=super-secret\n"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	var warnings, summary bytes.Buffer
	application := upApp(t, home, repository, &lifecycleStub{})
	application.WarningOutput = &warnings

	if err := application.Up(context.Background(), repository, &summary); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if !strings.Contains(warnings.String(), "referenced secret file missing.env is not present") {
		t.Errorf("warnings = %q, want the missing reference reported", warnings.String())
	}
	combined := warnings.String() + summary.String()
	if strings.Contains(combined, "super-secret") {
		t.Fatalf("up leaked secret contents:\n%s", combined)
	}
	if strings.Contains(warnings.String(), "referenced secret file .env is not present") {
		t.Errorf("warnings = %q, want the present reference accepted", warnings.String())
	}
}

func TestUpRequiresAPublicKeyBeforeLifecycleMutation(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository := filepath.Join(home, "app")
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	lifecycle := &lifecycleStub{}
	application := upApp(t, home, repository, lifecycle)
	if err := os.Remove(filepath.Join(home, ".ssh", "id_ed25519.pub")); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	err := application.Up(context.Background(), repository, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "ssh-keygen") {
		t.Fatalf("Up() error = %v, want actionable key guidance", err)
	}
	if len(lifecycle.upRequests) != 0 {
		t.Fatalf("up requests = %+v, want no lifecycle mutation", lifecycle.upRequests)
	}
}

func TestStatusReportsTheProvisionedGuest(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository := filepath.Join(home, "app")
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	application := upApp(t, home, repository, &lifecycleStub{})
	application.GuestProvisioner = &guestStub{guestPath: "/home/fx/app"}
	application.Version = "test"
	if err := application.Up(context.Background(), repository, io.Discard); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	var output bytes.Buffer
	if err := application.Status(context.Background(), repository, &output); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	for _, want := range []string{"Guest user: fx (501:20)", "Guest project: /home/fx/app"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("status output missing %q:\n%s", want, output.String())
		}
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
