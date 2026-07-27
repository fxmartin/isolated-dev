package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/fxmartin/isolated-dev/internal/config"
	"github.com/fxmartin/isolated-dev/internal/host"
	"github.com/fxmartin/isolated-dev/internal/machine"
	"github.com/fxmartin/isolated-dev/internal/project"
	"github.com/fxmartin/isolated-dev/internal/state"
	statusview "github.com/fxmartin/isolated-dev/internal/status"
)

type MachineManager interface {
	Up(context.Context, machine.Request) (machine.UpResult, error)
	Stop(context.Context, string) error
	Destroy(context.Context, string) error
}

type App struct {
	Version        string
	HostChecker    host.Checker
	StateStore     state.Store
	MachineManager MachineManager
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

func (app App) Up(ctx context.Context, projectPath string) (machine.UpResult, error) {
	resolved, err := project.Resolve(projectPath)
	if err != nil {
		return machine.UpResult{}, err
	}
	effectiveConfig, err := config.Load(resolved.Path)
	if err != nil {
		return machine.UpResult{}, err
	}
	if _, err := app.HostChecker.Check(ctx); err != nil {
		return machine.UpResult{}, err
	}
	if app.MachineManager == nil {
		return machine.UpResult{}, errors.New("machine lifecycle is not configured")
	}
	return app.MachineManager.Up(ctx, machine.Request{
		ProjectPath:      resolved.Path,
		MachineName:      resolved.MachineName,
		BaseImage:        effectiveConfig.BaseImage,
		BaseImageVersion: imageVersion(effectiveConfig.BaseImage),
		CPUs:             effectiveConfig.Resources.CPUs,
		MemoryGB:         effectiveConfig.Resources.MemoryGB,
		MountScope:       "home",
	})
}

func (app App) Stop(ctx context.Context, projectPath string) error {
	resolved, err := app.resolveForMutation(ctx, projectPath)
	if err != nil {
		return err
	}
	return app.MachineManager.Stop(ctx, resolved.MachineName)
}

func (app App) Destroy(ctx context.Context, projectPath string) error {
	resolved, err := app.resolveForMutation(ctx, projectPath)
	if err != nil {
		return err
	}
	return app.MachineManager.Destroy(ctx, resolved.MachineName)
}

func (app App) resolveForMutation(ctx context.Context, projectPath string) (project.Project, error) {
	resolved, err := project.Resolve(projectPath)
	if err != nil {
		return project.Project{}, err
	}
	if _, err := app.HostChecker.Check(ctx); err != nil {
		return project.Project{}, err
	}
	if app.MachineManager == nil {
		return project.Project{}, errors.New("machine lifecycle is not configured")
	}
	return resolved, nil
}

func imageVersion(reference string) string {
	lastSlash := strings.LastIndexByte(reference, '/')
	lastColon := strings.LastIndexByte(reference, ':')
	if lastColon > lastSlash && lastColon+1 < len(reference) {
		return reference[lastColon+1:]
	}
	return reference
}
