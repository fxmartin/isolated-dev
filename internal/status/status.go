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
	MountScope       string
	TunnelStatus     string
	Config           config.Config
}

func Write(writer io.Writer, snapshot Snapshot) error {
	lines := []string{
		"CLI: " + snapshot.CLIVersion,
		"Apple Container: " + snapshot.ContainerVersion,
		"Project: " + snapshot.ProjectPath,
		fmt.Sprintf("Machine: %s (%s)", snapshot.MachineName, snapshot.MachineStatus),
		"Base image: " + snapshot.BaseImage,
		"Mount scope: " + snapshot.MountScope,
		"Tunnel: " + snapshot.TunnelStatus,
		fmt.Sprintf(
			"Resources: %d CPU, %d GB memory, %d GB disk",
			snapshot.Config.Resources.CPUs,
			snapshot.Config.Resources.MemoryGB,
			snapshot.Config.Resources.DiskGB,
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
