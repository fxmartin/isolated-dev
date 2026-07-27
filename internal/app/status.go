package app

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/fxmartin/isolated-dev/internal/config"
	"github.com/fxmartin/isolated-dev/internal/host"
	"github.com/fxmartin/isolated-dev/internal/project"
	"github.com/fxmartin/isolated-dev/internal/state"
	statusview "github.com/fxmartin/isolated-dev/internal/status"
)

type App struct {
	Version     string
	HostChecker host.Checker
	StateStore  state.Store
}

func (app App) Status(ctx context.Context, projectPath string, output io.Writer) error {
	resolved, err := project.Resolve(projectPath)
	if err != nil {
		return err
	}
	effectiveConfig, err := config.Load(resolved.Path)
	if err != nil {
		return err
	}
	prerequisites, err := app.HostChecker.Check(ctx)
	if err != nil {
		return err
	}

	snapshot := statusview.Snapshot{
		CLIVersion:       app.Version,
		ContainerVersion: prerequisites.ContainerVersion,
		ProjectPath:      resolved.Path,
		MachineName:      resolved.MachineName,
		MachineStatus:    "not-created",
		BaseImage:        effectiveConfig.BaseImage,
		MountScope:       "not-created",
		TunnelStatus:     "stopped",
		Config:           effectiveConfig,
	}
	stored, err := app.StateStore.Load(resolved.MachineName)
	if err == nil {
		snapshot.MachineStatus = "unknown"
		snapshot.BaseImage = stored.BaseImage
		snapshot.MountScope = stored.MountScope
		snapshot.TunnelStatus = "unknown"
	} else if !errors.Is(err, state.ErrNotFound) {
		return fmt.Errorf("load project state: %w", err)
	}

	return statusview.Write(output, snapshot)
}
