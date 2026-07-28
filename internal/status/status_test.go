package status

import (
	"bytes"
	"strings"
	"testing"

	"github.com/fxmartin/isolated-dev/internal/config"
)

func TestWriteOmitsSecretReferencesAndValues(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	err := Write(&output, Snapshot{
		CLIVersion:       "1.0.0",
		ContainerVersion: "1.1.0",
		ProjectPath:      "/Users/fx/dev/app",
		MachineName:      "isolated-dev-app-abcd1234",
		MachineStatus:    "running",
		BaseImage:        "isolated-dev-base:1",
		MountScope:       "home",
		TunnelStatus:     "running",
		Config: config.Config{
			Resources: config.Resources{CPUs: 4, MemoryGB: 8},
			Ports:     []config.Port{{Name: "web", Guest: 3000, Host: 3001}},
			Secrets: config.SecretReferences{
				Environment: []string{"SUPER_SECRET"},
				Files:       []string{".env"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	got := output.String()
	for _, expected := range []string{
		"CLI: 1.0.0",
		"Apple Container: 1.1.0",
		"Machine: isolated-dev-app-abcd1234 (running)",
		"Mount scope: home",
		"Resources: 4 CPU, 8 GB memory",
		"web: localhost:3001 -> guest:3000",
	} {
		if !strings.Contains(got, expected) {
			t.Errorf("status output missing %q:\n%s", expected, got)
		}
	}
	if strings.Contains(got, "SUPER_SECRET") || strings.Contains(got, ".env") {
		t.Fatalf("status output exposed secret references:\n%s", got)
	}
	if strings.Contains(got, "disk") {
		t.Fatalf("status output presented unsupported disk allocation:\n%s", got)
	}
}

// A newer base image never migrates a machine on its own: status reports the
// available version while the machine stays pinned to the one it was built on.
func TestWriteReportsAnAvailableBaseImageWhileTheMachineStaysPinned(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	if err := Write(&output, Snapshot{
		BaseImage:          "local/isolated-dev-base:1",
		AvailableBaseImage: "local/isolated-dev-base:2",
		Config:             config.Config{Resources: config.Resources{CPUs: 4, MemoryGB: 8}},
	}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	got := output.String()
	want := "Base image: local/isolated-dev-base:1 (pinned; local/isolated-dev-base:2 available, run upgrade)"
	if !strings.Contains(got, want) {
		t.Errorf("status output missing %q:\n%s", want, got)
	}
}

func TestWriteOmitsTheAvailableBaseImageWhenTheMachineIsCurrent(t *testing.T) {
	t.Parallel()

	for name, snapshot := range map[string]Snapshot{
		"matching":  {BaseImage: "local/isolated-dev-base:1", AvailableBaseImage: "local/isolated-dev-base:1"},
		"unknown":   {BaseImage: "local/isolated-dev-base:1"},
		"uncreated": {BaseImage: "local/isolated-dev-base:1", AvailableBaseImage: ""},
	} {
		snapshot := snapshot
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			if err := Write(&output, snapshot); err != nil {
				t.Fatalf("Write() error = %v", err)
			}
			if got := output.String(); !strings.Contains(got, "Base image: local/isolated-dev-base:1\n") {
				t.Errorf("status output = %q, want an unadorned base image line", got)
			}
		})
	}
}

func TestWriteReportsGuestIdentityAndMountedProject(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	if err := Write(&output, Snapshot{
		GuestUser:        "fx",
		GuestUID:         501,
		GuestGID:         20,
		GuestProjectPath: "/Users/fx/dev/app",
		Config:           config.Config{Resources: config.Resources{CPUs: 4, MemoryGB: 8}},
	}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	got := output.String()
	for _, expected := range []string{
		"Guest user: fx (501:20)",
		"Guest project: /Users/fx/dev/app",
	} {
		if !strings.Contains(got, expected) {
			t.Errorf("status output missing %q:\n%s", expected, got)
		}
	}
}

func TestWriteReportsTheManagedSSHHost(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	if err := Write(&output, Snapshot{
		MachineName: "isolated-dev-app-abcd1234",
		GuestUser:   "fx",
		SSHAddress:  "192.168.64.5",
		Config:      config.Config{Resources: config.Resources{CPUs: 4, MemoryGB: 8}},
	}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	want := "SSH: isolated-dev-app-abcd1234 (fx@192.168.64.5)"
	if got := output.String(); !strings.Contains(got, want) {
		t.Errorf("status output missing %q:\n%s", want, got)
	}
}

// State written before the guest identity was recorded still names a usable
// host, so the login name is simply left out.
func TestWriteReportsTheSSHHostWithoutAGuestUser(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	if err := Write(&output, Snapshot{
		MachineName: "isolated-dev-app-abcd1234",
		SSHAddress:  "192.168.64.5",
	}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	want := "SSH: isolated-dev-app-abcd1234 (192.168.64.5)"
	if got := output.String(); !strings.Contains(got, want) {
		t.Errorf("status output missing %q:\n%s", want, got)
	}
}

func TestWriteReportsAnUnconfiguredSSHHost(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	if err := Write(&output, Snapshot{MachineName: "isolated-dev-app-abcd1234"}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	if got := output.String(); !strings.Contains(got, "SSH: not-configured") {
		t.Errorf("status output missing an unconfigured SSH host:\n%s", got)
	}
}

func TestWriteReportsAnUnprovisionedGuest(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	if err := Write(&output, Snapshot{}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	got := output.String()
	for _, expected := range []string{
		"Guest user: not-provisioned",
		"Guest project: not-provisioned",
	} {
		if !strings.Contains(got, expected) {
			t.Errorf("status output missing %q:\n%s", expected, got)
		}
	}
}
