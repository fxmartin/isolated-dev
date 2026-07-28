package app

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/fxmartin/isolated-dev/internal/machine"
	"github.com/fxmartin/isolated-dev/internal/state"
	upgradeview "github.com/fxmartin/isolated-dev/internal/upgrade"
)

// Upgrade moves a project machine onto the base image its configuration
// selects. A machine is never migrated implicitly: an upgrade recreates it,
// which discards every byte of guest-only state, so the caller must have seen
// the preview and passed the confirmation through.
func (app App) Upgrade(
	ctx context.Context,
	projectPath string,
	confirmed bool,
	output io.Writer,
) error {
	// Preparing the recreation up front keeps a rejected repository, unmanaged
	// image, or missing SSH key from surfacing only after the machine — and the
	// data that lives nowhere else — has already been deleted.
	preparation, err := app.prepareUp(ctx, projectPath)
	if err != nil {
		return err
	}
	resolved := preparation.project

	stored, err := app.StateStore.Load(resolved.MachineName)
	if errors.Is(err, state.ErrNotFound) {
		return fmt.Errorf(
			"no project machine is recorded for %q; run up to create one before upgrading",
			resolved.Path,
		)
	}
	if err != nil {
		return fmt.Errorf("load project state: %w", err)
	}

	target := preparation.config.BaseImage
	if stored.BaseImage == target {
		if _, err := fmt.Fprintf(
			output,
			"%s is already pinned to %s; no upgrade is available\n",
			resolved.MachineName,
			target,
		); err != nil {
			return fmt.Errorf("write upgrade summary: %w", err)
		}
		return nil
	}

	if err := upgradeview.Write(output, upgradeview.Plan{
		ProjectPath:      resolved.Path,
		MachineName:      resolved.MachineName,
		CurrentBaseImage: stored.BaseImage,
		CurrentVersion:   storedVersion(stored),
		TargetBaseImage:  target,
		TargetVersion:    imageVersion(target),
	}); err != nil {
		return err
	}

	if !confirmed {
		if _, err := fmt.Fprintf(
			output,
			"No changes made. Re-run with --yes to recreate %s on %s.\n",
			resolved.MachineName,
			target,
		); err != nil {
			return fmt.Errorf("write upgrade guidance: %w", err)
		}
		return nil
	}

	if err := app.MachineManager.Destroy(ctx, machine.Target{
		ProjectPath: resolved.Path,
		MachineName: resolved.MachineName,
	}); err != nil {
		return err
	}
	// The replacement goes through the ordinary `up` path so mount, identity,
	// SSH, and tunnel reconciliation behave exactly as they do for any machine.
	if err := app.Up(ctx, projectPath, output); err != nil {
		return fmt.Errorf(
			"machine %q was removed but could not be recreated on %s; rerun up once this is resolved: %w",
			resolved.MachineName,
			target,
			err,
		)
	}
	return nil
}

// storedVersion falls back to the image reference for state recorded before the
// base-image version was persisted alongside it.
func storedVersion(stored state.Project) string {
	if stored.BaseImageVersion != "" {
		return stored.BaseImageVersion
	}
	return imageVersion(stored.BaseImage)
}
