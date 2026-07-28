package status

import (
	"fmt"
	"io"

	"github.com/fxmartin/isolated-dev/internal/config"
)

type Snapshot struct {
	CLIVersion       string
	ContainerVersion string
	ProjectPath      string
	MachineName      string
	MachineStatus    string
	BaseImage        string
	// AvailableBaseImage is the image the effective configuration selects for a
	// recorded machine; it is empty only when no machine has been created. It
	// routinely equals BaseImage, so an upgrade is available only when the two
	// differ.
	AvailableBaseImage string
	MountScope         string
	TunnelStatus       string
	GuestUser          string
	GuestUID           int
	GuestGID           int
	GuestProjectPath   string
	Config             config.Config
}

const notProvisioned = "not-provisioned"

func guestUser(snapshot Snapshot) string {
	if snapshot.GuestUser == "" {
		return notProvisioned
	}
	return fmt.Sprintf("%s (%d:%d)", snapshot.GuestUser, snapshot.GuestUID, snapshot.GuestGID)
}

// baseImage reports the image the machine is pinned to. A newer configured
// image is only announced: `status` never implies that a machine has moved to
// an image it was not created from.
func baseImage(snapshot Snapshot) string {
	if snapshot.AvailableBaseImage == "" || snapshot.AvailableBaseImage == snapshot.BaseImage {
		return snapshot.BaseImage
	}
	return fmt.Sprintf(
		"%s (pinned; %s available, run upgrade)",
		snapshot.BaseImage,
		snapshot.AvailableBaseImage,
	)
}

func orNotProvisioned(value string) string {
	if value == "" {
		return notProvisioned
	}
	return value
}

func Write(writer io.Writer, snapshot Snapshot) error {
	lines := []string{
		"CLI: " + snapshot.CLIVersion,
		"Apple Container: " + snapshot.ContainerVersion,
		"Project: " + snapshot.ProjectPath,
		fmt.Sprintf("Machine: %s (%s)", snapshot.MachineName, snapshot.MachineStatus),
		"Base image: " + baseImage(snapshot),
		"Mount scope: " + snapshot.MountScope,
		"Tunnel: " + snapshot.TunnelStatus,
		"Guest user: " + guestUser(snapshot),
		"Guest project: " + orNotProvisioned(snapshot.GuestProjectPath),
		fmt.Sprintf(
			"Resources: %d CPU, %d GB memory",
			snapshot.Config.Resources.CPUs,
			snapshot.Config.Resources.MemoryGB,
		),
	}
	for _, port := range snapshot.Config.Ports {
		lines = append(lines, fmt.Sprintf(
			"%s: localhost:%d -> guest:%d",
			port.Name,
			port.Host,
			port.Guest,
		))
	}
	for _, line := range lines {
		if _, err := fmt.Fprintln(writer, line); err != nil {
			return fmt.Errorf("write status: %w", err)
		}
	}
	return nil
}
