package app

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// Open opens the mounted project in Zed's SSH remote-development mode.
//
// The machine and its managed SSH host are reconciled first, so a stopped
// machine, a machine that moved to a new address, and a machine that was never
// created all end up connectable without a separate command.
func (app App) Open(ctx context.Context, projectPath string, output io.Writer) error {
	if app.Zed == nil {
		return errors.New("Zed integration is not configured")
	}
	outcome, err := app.up(ctx, projectPath, output)
	if err != nil {
		return err
	}
	if err := app.Zed.Open(
		ctx,
		outcome.project.MachineName,
		outcome.guestProjectPath,
	); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(
		output,
		"opening %s in Zed over %s\n",
		outcome.guestProjectPath,
		outcome.project.MachineName,
	); err != nil {
		return fmt.Errorf("write open summary: %w", err)
	}
	return nil
}
