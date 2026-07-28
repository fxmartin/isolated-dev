package app

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fxmartin/isolated-dev/internal/project"
)

// retarget rewrites the project configuration so a different base image becomes
// the upgrade target without rebuilding the whole fixture.
func retarget(t *testing.T, repository string, image string) {
	t.Helper()

	if err := os.WriteFile(
		filepath.Join(repository, ".isolated-dev.toml"),
		[]byte("version = 1\nbase_image = \""+image+"\"\n"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

// An unmanaged target is refused by the shared `up` preparation. The assertion
// that matters is not the message but the ordering: the machine, and the guest
// data only it holds, must still exist when the refusal is reported.
func TestUpgradeRefusesAnUnmanagedTargetImageBeforeDestroying(t *testing.T) {
	t.Parallel()

	application, lifecycle, repository := upgradeApp(t, "ubuntu:24.04")

	err := application.Upgrade(context.Background(), repository, true, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "not a managed isolated-dev image") {
		t.Fatalf("Upgrade() error = %v, want a refusal to recreate on an unmanaged image", err)
	}
	if len(lifecycle.destroyed) != 0 || len(lifecycle.upRequests) != 0 {
		t.Fatalf("machine mutated before validation: destroyed = %#v, up = %#v",
			lifecycle.destroyed, lifecycle.upRequests)
	}
}

// After a confirmed recreation the machine is pinned to the target, so a second
// run has nothing to do. Re-running an upgrade must never destroy a machine a
// second time.
func TestUpgradeIsIdempotentAfterASuccessfulRecreation(t *testing.T) {
	t.Parallel()

	application, lifecycle, repository := upgradeApp(t, "local/isolated-dev-base:2")
	if err := application.Upgrade(context.Background(), repository, true, io.Discard); err != nil {
		t.Fatalf("Upgrade() error = %v", err)
	}

	var output bytes.Buffer
	if err := application.Upgrade(context.Background(), repository, true, &output); err != nil {
		t.Fatalf("second Upgrade() error = %v", err)
	}

	if !strings.Contains(output.String(), "already pinned to local/isolated-dev-base:2") {
		t.Errorf("output = %q, want the machine reported as up to date", output.String())
	}
	if len(lifecycle.destroyed) != 1 {
		t.Fatalf("destroyed = %#v, want exactly one recreation across both runs",
			lifecycle.destroyed)
	}
}

// `upgrade` moves a machine onto the image its configuration selects, which is
// not always the newer one: pinning the configuration back to an older image
// makes that image the target, and the same destructive preview applies.
func TestUpgradeTargetsAnOlderConfiguredImage(t *testing.T) {
	t.Parallel()

	application, lifecycle, repository := upgradeApp(t, "local/isolated-dev-base:2")
	if err := application.Upgrade(context.Background(), repository, true, io.Discard); err != nil {
		t.Fatalf("Upgrade() error = %v", err)
	}
	retarget(t, repository, "local/isolated-dev-base:1")
	var output bytes.Buffer

	if err := application.Upgrade(context.Background(), repository, false, &output); err != nil {
		t.Fatalf("Upgrade() error = %v", err)
	}

	got := output.String()
	for _, want := range []string{
		"Current base image: local/isolated-dev-base:2 (version 2)",
		"Target base image: local/isolated-dev-base:1 (version 1)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("preview missing %q:\n%s", want, got)
		}
	}
	if len(lifecycle.destroyed) != 1 {
		t.Fatalf("destroyed = %#v, want the declined preview to change nothing",
			lifecycle.destroyed)
	}
}

// A declined preview must leave everything `status` reports — machine, tunnel,
// mount, and guest identity — byte-for-byte identical.
func TestUpgradePreviewLeavesTheReportedStatusUnchanged(t *testing.T) {
	t.Parallel()

	application, _, repository := upgradeApp(t, "local/isolated-dev-base:2")
	application.Version = "test"
	var before bytes.Buffer
	if err := application.Status(context.Background(), repository, &before); err != nil {
		t.Fatalf("Status() error = %v", err)
	}

	if err := application.Upgrade(context.Background(), repository, false, io.Discard); err != nil {
		t.Fatalf("Upgrade() error = %v", err)
	}

	var after bytes.Buffer
	if err := application.Status(context.Background(), repository, &after); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if after.String() != before.String() {
		t.Fatalf("status after a declined preview:\n%s\nwant unchanged:\n%s",
			after.String(), before.String())
	}
}

// The preview reports the current version from recorded state, which predates
// the version field and is editable by hand, so references that carry no tag at
// all still have to render something truthful rather than a stray path segment.
func TestImageVersionDerivesTheTagFromAReference(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		reference string
		want      string
	}{
		"tagged":            {reference: "local/isolated-dev-base:2", want: "2"},
		"untagged":          {reference: "local/isolated-dev-base", want: "local/isolated-dev-base"},
		"registry port":     {reference: "registry.example.com:5000/base", want: "registry.example.com:5000/base"},
		"port and tag":      {reference: "registry.example.com:5000/base:3", want: "3"},
		"empty tag":         {reference: "local/isolated-dev-base:", want: "local/isolated-dev-base:"},
		"unqualified image": {reference: "ubuntu", want: "ubuntu"},
	}

	for name, test := range tests {
		test := test
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := imageVersion(test.reference); got != test.want {
				t.Errorf("imageVersion(%q) = %q, want %q", test.reference, got, test.want)
			}
		})
	}
}

// A machine recreated by `upgrade` stays addressable as the same project
// machine, so the state the replacement is recorded under must match the one
// the destroyed machine used.
func TestUpgradeRecreatesUnderTheSameMachineName(t *testing.T) {
	t.Parallel()

	application, lifecycle, repository := upgradeApp(t, "local/isolated-dev-base:2")
	resolved, err := project.Resolve(repository)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if err := application.Upgrade(context.Background(), repository, true, io.Discard); err != nil {
		t.Fatalf("Upgrade() error = %v", err)
	}

	if len(lifecycle.upRequests) != 1 {
		t.Fatalf("up requests = %#v, want one recreation", lifecycle.upRequests)
	}
	request := lifecycle.upRequests[0]
	if request.MachineName != resolved.MachineName || request.ProjectPath != resolved.Path {
		t.Errorf("recreated %q at %q, want %q at %q",
			request.MachineName, request.ProjectPath, resolved.MachineName, resolved.Path)
	}
	stored, err := application.StateStore.Load(resolved.MachineName)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if stored.MachineName != resolved.MachineName {
		t.Errorf("stored machine name = %q, want %q", stored.MachineName, resolved.MachineName)
	}
}
