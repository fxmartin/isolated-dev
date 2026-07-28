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
	"github.com/fxmartin/isolated-dev/internal/state"
)

// stateBackedLifecycle models the parts of machine.Manager that upgrade relies
// on: Destroy removes the recorded project state and Up records the requested
// base image, so the image a recreated machine ends up pinned to is
// observable.
type stateBackedLifecycle struct {
	lifecycleStub
	store state.Store
}

func (lifecycle *stateBackedLifecycle) Up(
	ctx context.Context,
	request machine.Request,
) (machine.UpResult, error) {
	result, err := lifecycle.lifecycleStub.Up(ctx, request)
	if err != nil {
		return result, err
	}
	return result, lifecycle.store.Save(state.Project{
		SchemaVersion:    1,
		ProjectPath:      request.ProjectPath,
		MachineName:      request.MachineName,
		BaseImage:        request.BaseImage,
		BaseImageVersion: request.BaseImageVersion,
		MountScope:       request.MountScope,
		CPUs:             request.CPUs,
		MemoryGB:         request.MemoryGB,
	})
}

func (lifecycle *stateBackedLifecycle) Destroy(
	ctx context.Context,
	target machine.Target,
) error {
	if err := lifecycle.lifecycleStub.Destroy(ctx, target); err != nil {
		return err
	}
	return lifecycle.store.Delete(target.MachineName)
}

// imageStub records the references an upgrade prepares, and can fail the way a
// missing network or a broken Dockerfile does.
type imageStub struct {
	ensured []string
	err     error
	// onEnsure observes the machine state at the moment the image is prepared.
	onEnsure func()
}

func (stub *imageStub) EnsureReference(_ context.Context, reference string) error {
	stub.ensured = append(stub.ensured, reference)
	if stub.onEnsure != nil {
		stub.onEnsure()
	}
	return stub.err
}

// upgradeApp seeds a machine pinned to the default base image and points the
// project configuration at the requested target, which is the only situation
// in which an upgrade is available.
func upgradeApp(t *testing.T, targetImage string) (App, *stateBackedLifecycle, string) {
	t.Helper()

	home := t.TempDir()
	repository := filepath.Join(home, "app")
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(repository, ".isolated-dev.toml"),
		[]byte("version = 1\nbase_image = \""+targetImage+"\"\n"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	application := upApp(t, home, repository, nil)
	lifecycle := &stateBackedLifecycle{store: application.StateStore}
	application.MachineManager = lifecycle
	application.ImageEnsurer = &imageStub{}
	application.WarningOutput = io.Discard
	return application, lifecycle, repository
}

func TestUpgradePreviewShowsVersionsAndDiscardedStateWithoutMutating(t *testing.T) {
	t.Parallel()

	application, lifecycle, repository := upgradeApp(t, "local/isolated-dev-base:2")
	var output bytes.Buffer

	if err := application.Upgrade(context.Background(), repository, false, &output); err != nil {
		t.Fatalf("Upgrade() error = %v", err)
	}

	got := output.String()
	for _, want := range []string{
		"Current base image: local/isolated-dev-base:1 (version 1)",
		"Target base image: local/isolated-dev-base:2 (version 2)",
		"Docker Compose volumes",
		"--yes",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("upgrade preview missing %q:\n%s", want, got)
		}
	}
	if len(lifecycle.destroyed) != 0 || len(lifecycle.upRequests) != 0 {
		t.Fatalf("preview mutated the machine: destroyed = %#v, up = %#v",
			lifecycle.destroyed, lifecycle.upRequests)
	}
}

// A declined preview must leave the recorded machine, its pinned image, and
// its persistent state exactly as they were.
func TestUpgradePreviewLeavesRecordedStateUnchanged(t *testing.T) {
	t.Parallel()

	application, _, repository := upgradeApp(t, "local/isolated-dev-base:2")
	resolved, err := project.Resolve(repository)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	before, err := application.StateStore.Load(resolved.MachineName)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if err := application.Upgrade(context.Background(), repository, false, io.Discard); err != nil {
		t.Fatalf("Upgrade() error = %v", err)
	}

	after, err := application.StateStore.Load(resolved.MachineName)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if after != before {
		t.Fatalf("state = %+v, want %+v unchanged", after, before)
	}
}

func TestUpgradeRecreatesOnTheTargetImageWhenConfirmed(t *testing.T) {
	t.Parallel()

	application, lifecycle, repository := upgradeApp(t, "local/isolated-dev-base:2")
	provisioner := &guestStub{guestPath: "/home/fx/app"}
	application.GuestProvisioner = provisioner
	resolved, err := project.Resolve(repository)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if err := application.Upgrade(context.Background(), repository, true, io.Discard); err != nil {
		t.Fatalf("Upgrade() error = %v", err)
	}

	if len(lifecycle.destroyed) != 1 || lifecycle.destroyed[0].MachineName != resolved.MachineName {
		t.Fatalf("destroyed = %#v, want the resolved project machine", lifecycle.destroyed)
	}
	if len(lifecycle.upRequests) != 1 {
		t.Fatalf("up requests = %#v, want one recreation", lifecycle.upRequests)
	}
	request := lifecycle.upRequests[0]
	if request.BaseImage != "local/isolated-dev-base:2" || request.BaseImageVersion != "2" {
		t.Errorf("recreated on %s (version %s), want the target image",
			request.BaseImage, request.BaseImageVersion)
	}
	if request.MountScope != "home" {
		t.Errorf("MountScope = %q, want normal mount reconciliation", request.MountScope)
	}
	if len(provisioner.requests) != 1 || provisioner.requests[0].Identity != testIdentity {
		t.Fatalf("guest requests = %#v, want normal identity reconciliation", provisioner.requests)
	}
	if keys := provisioner.requests[0].PublicKeys; len(keys) != 1 || keys[0] != testPublicKey {
		t.Errorf("PublicKeys = %#v, want normal SSH reconciliation", keys)
	}

	stored, err := application.StateStore.Load(resolved.MachineName)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if stored.BaseImage != "local/isolated-dev-base:2" || stored.GuestProjectPath != "/home/fx/app" {
		t.Errorf("stored state = %+v, want the target image and reconciled guest", stored)
	}
}

// Destroying before checking is the one ordering that loses data irrecoverably,
// so every `up` precondition is validated while the machine still exists.
func TestUpgradeValidatesEveryUpPreconditionBeforeDestroying(t *testing.T) {
	t.Parallel()

	application, lifecycle, repository := upgradeApp(t, "local/isolated-dev-base:2")
	if err := os.Remove(filepath.Join(application.HomeDir, ".ssh", "id_ed25519.pub")); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	err := application.Upgrade(context.Background(), repository, true, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "ssh-keygen") {
		t.Fatalf("Upgrade() error = %v, want actionable key guidance", err)
	}
	if len(lifecycle.destroyed) != 0 {
		t.Fatalf("destroyed = %#v, want no destruction before validation passes", lifecycle.destroyed)
	}
}

// The target image is the one precondition `up` cannot check ahead of time, and
// it is also the one most likely to fail: it may never have been built. A
// failure to produce it must leave the machine — and the guest-only data it
// holds — exactly where it was.
func TestUpgradeBuildsTheTargetImageBeforeDestroying(t *testing.T) {
	t.Parallel()

	application, lifecycle, repository := upgradeApp(t, "local/isolated-dev-base:2")
	destroyedWhenEnsured := -1
	images := &imageStub{
		onEnsure: func() { destroyedWhenEnsured = len(lifecycle.destroyed) },
	}
	application.ImageEnsurer = images

	if err := application.Upgrade(context.Background(), repository, true, io.Discard); err != nil {
		t.Fatalf("Upgrade() error = %v", err)
	}

	if len(images.ensured) != 1 || images.ensured[0] != "local/isolated-dev-base:2" {
		t.Fatalf("ensured = %#v, want the target image prepared once", images.ensured)
	}
	if destroyedWhenEnsured != 0 {
		t.Errorf("machine destroyed %d time(s) before the image was prepared, want 0",
			destroyedWhenEnsured)
	}
}

func TestUpgradeKeepsTheMachineWhenTheTargetImageCannotBeBuilt(t *testing.T) {
	t.Parallel()

	application, lifecycle, repository := upgradeApp(t, "local/isolated-dev-base:2")
	application.ImageEnsurer = &imageStub{err: errors.New("no network")}
	resolved, err := project.Resolve(repository)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	before, err := application.StateStore.Load(resolved.MachineName)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	err = application.Upgrade(context.Background(), repository, true, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "no network") {
		t.Fatalf("Upgrade() error = %v, want the image failure reported", err)
	}
	if !strings.Contains(err.Error(), "the machine is untouched") {
		t.Errorf("Upgrade() error = %v, want it to say the machine survived", err)
	}
	if len(lifecycle.destroyed) != 0 || len(lifecycle.upRequests) != 0 {
		t.Fatalf("machine mutated after a failed image build: destroyed = %#v, up = %#v",
			lifecycle.destroyed, lifecycle.upRequests)
	}
	after, err := application.StateStore.Load(resolved.MachineName)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if after != before {
		t.Fatalf("state = %+v, want %+v unchanged", after, before)
	}
}

// The pre-flight build is what makes the recreation survivable, so an
// unconfigured builder must fail rather than silently skip it.
func TestUpgradeRefusesWithoutAConfiguredImageBuilder(t *testing.T) {
	t.Parallel()

	application, lifecycle, repository := upgradeApp(t, "local/isolated-dev-base:2")
	application.ImageEnsurer = nil

	err := application.Upgrade(context.Background(), repository, true, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "base-image builder is not configured") {
		t.Fatalf("Upgrade() error = %v, want an unconfigured-builder refusal", err)
	}
	if len(lifecycle.destroyed) != 0 {
		t.Fatalf("destroyed = %#v, want no destruction", lifecycle.destroyed)
	}
}

// A preview changes nothing, and building an image is a change: it downloads,
// writes layers, and can take minutes.
func TestUpgradePreviewDoesNotBuildTheTargetImage(t *testing.T) {
	t.Parallel()

	application, _, repository := upgradeApp(t, "local/isolated-dev-base:2")
	images := &imageStub{}
	application.ImageEnsurer = images

	if err := application.Upgrade(context.Background(), repository, false, io.Discard); err != nil {
		t.Fatalf("Upgrade() error = %v", err)
	}
	if len(images.ensured) != 0 {
		t.Fatalf("ensured = %#v, want a preview to build nothing", images.ensured)
	}
}

func TestUpgradeReportsNoAvailableUpgrade(t *testing.T) {
	t.Parallel()

	application, lifecycle, repository := upgradeApp(t, "local/isolated-dev-base:1")
	var output bytes.Buffer

	if err := application.Upgrade(context.Background(), repository, true, &output); err != nil {
		t.Fatalf("Upgrade() error = %v", err)
	}
	if !strings.Contains(output.String(), "already pinned to local/isolated-dev-base:1") {
		t.Errorf("output = %q, want an up-to-date report", output.String())
	}
	if len(lifecycle.destroyed) != 0 {
		t.Fatalf("destroyed = %#v, want no recreation when the image already matches",
			lifecycle.destroyed)
	}
}

func TestUpgradeRequiresARecordedMachine(t *testing.T) {
	t.Parallel()

	application, lifecycle, repository := upgradeApp(t, "local/isolated-dev-base:2")
	application.StateStore = state.Store{Root: t.TempDir()}

	err := application.Upgrade(context.Background(), repository, true, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "run up") {
		t.Fatalf("Upgrade() error = %v, want guidance to create the machine first", err)
	}
	if len(lifecycle.destroyed) != 0 {
		t.Fatalf("destroyed = %#v, want no destruction", lifecycle.destroyed)
	}
}

// State written before base-image versions were recorded still has to produce a
// readable preview.
func TestUpgradeDerivesTheCurrentVersionFromLegacyState(t *testing.T) {
	t.Parallel()

	application, _, repository := upgradeApp(t, "local/isolated-dev-base:2")
	resolved, err := project.Resolve(repository)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	stored, err := application.StateStore.Load(resolved.MachineName)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	stored.BaseImageVersion = ""
	if err := application.StateStore.Save(stored); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	var output bytes.Buffer

	if err := application.Upgrade(context.Background(), repository, false, &output); err != nil {
		t.Fatalf("Upgrade() error = %v", err)
	}
	if !strings.Contains(output.String(), "local/isolated-dev-base:1 (version 1)") {
		t.Errorf("output = %q, want the version derived from the image reference", output.String())
	}
}

func TestUpgradeReportsBoundaryFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		application func(*testing.T, App, *stateBackedLifecycle) App
		output      io.Writer
		want        string
	}{
		{
			name: "host prerequisites",
			application: func(_ *testing.T, application App, _ *stateBackedLifecycle) App {
				application.HostChecker = failingHostChecker()
				return application
			},
			want: "not configured",
		},
		{
			name: "unreadable state",
			application: func(t *testing.T, application App, _ *stateBackedLifecycle) App {
				rootFile := filepath.Join(t.TempDir(), "not-a-directory")
				if err := os.WriteFile(rootFile, []byte("x"), 0o600); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
				application.StateStore = state.Store{Root: rootFile}
				return application
			},
			want: "load project state",
		},
		{
			name: "preview output",
			application: func(_ *testing.T, application App, _ *stateBackedLifecycle) App {
				return application
			},
			output: failingWriter{},
			want:   "write upgrade preview",
		},
		{
			name: "destroy",
			application: func(_ *testing.T, application App, lifecycle *stateBackedLifecycle) App {
				lifecycle.destroyErr = errors.New("machine delete failed")
				return application
			},
			want: "machine delete failed",
		},
		{
			name: "recreate",
			application: func(_ *testing.T, application App, lifecycle *stateBackedLifecycle) App {
				lifecycle.upErr = errors.New("machine create failed")
				return application
			},
			want: "was removed but could not be recreated",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			application, lifecycle, repository := upgradeApp(t, "local/isolated-dev-base:2")
			output := test.output
			if output == nil {
				output = io.Discard
			}

			err := test.application(t, application, lifecycle).
				Upgrade(context.Background(), repository, true, output)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Upgrade() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestUpgradeReportsSummaryOutputFailures(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		output    io.Writer
		target    string
		confirmed bool
		want      string
	}{
		"declined guidance": {
			output:    &writeAfter{failAfter: 11},
			target:    "local/isolated-dev-base:2",
			confirmed: false,
			want:      "write upgrade guidance",
		},
		"up to date": {
			output:    failingWriter{},
			target:    "local/isolated-dev-base:1",
			confirmed: true,
			want:      "write upgrade summary",
		},
	}

	for name, test := range tests {
		test := test
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			application, _, repository := upgradeApp(t, test.target)

			err := application.Upgrade(context.Background(), repository, test.confirmed, test.output)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Upgrade() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// A newer configured base image is reported by status but never applied on its
// own: the machine keeps running the image it was created from.
func TestStatusReportsAnAvailableUpgradeWithoutUnpinningTheMachine(t *testing.T) {
	t.Parallel()

	application, _, repository := upgradeApp(t, "local/isolated-dev-base:2")
	application.Version = "test"
	var output bytes.Buffer

	if err := application.Status(context.Background(), repository, &output); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	want := "Base image: local/isolated-dev-base:1 (pinned; local/isolated-dev-base:2 available, run upgrade)"
	if !strings.Contains(output.String(), want) {
		t.Errorf("status output missing %q:\n%s", want, output.String())
	}
}

func TestStatusOmitsAnAvailableUpgradeForAnUncreatedMachine(t *testing.T) {
	t.Parallel()

	application, _, repository := upgradeApp(t, "local/isolated-dev-base:2")
	application.StateStore = state.Store{Root: t.TempDir()}
	var output bytes.Buffer

	if err := application.Status(context.Background(), repository, &output); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !strings.Contains(output.String(), "Base image: local/isolated-dev-base:2\n") {
		t.Errorf("status output = %q, want the configured image without an upgrade hint", output.String())
	}
}
