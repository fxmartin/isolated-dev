package app

import (
	"fmt"
	"io"
	"strings"

	"github.com/fxmartin/isolated-dev/internal/config"
	"github.com/fxmartin/isolated-dev/internal/project"
	"github.com/fxmartin/isolated-dev/internal/tunnel"
)

// TunnelManager maintains the managed background port forwards of one project
// machine. They outlive the CLI, so browser and API access stay stable while
// commands come and go and while Zed is closed.
type TunnelManager interface {
	Reconcile(tunnel.Spec) (tunnel.State, error)
	Remove(machineName string) error
	Inspect(machineName string) (tunnel.State, error)
}

// reconcileTunnel converges the background tunnel on the ports the project
// declares. It runs after SSH reconciliation, because the tunnel connects
// through the managed host alias and the address behind it is what decides
// whether an existing tunnel is still valid.
func (app App) reconcileTunnel(
	resolved project.Project,
	effectiveConfig config.Config,
	address string,
	guestUser string,
	created bool,
	output io.Writer,
) error {
	// A machine that was just created has never been reached through the
	// recorded tunnel, even when it came back at the address its predecessor
	// used, so the process still pointing at that predecessor goes first.
	if created {
		if err := app.Tunnels.Remove(resolved.MachineName); err != nil {
			return err
		}
	}
	state, err := app.Tunnels.Reconcile(tunnel.Spec{
		MachineName: resolved.MachineName,
		Address:     address,
		User:        guestUser,
		Forwards:    declaredForwards(effectiveConfig.Ports),
	})
	if err != nil {
		return fmt.Errorf("forward the configured ports of %q: %w", resolved.MachineName, err)
	}

	// A macOS port that already has a listener is named rather than seized:
	// whatever is there keeps its socket, and the remaining ports still reach
	// the guest.
	for _, blocked := range state.Unforwarded {
		if err := app.warn(
			"warning: %s: macOS port %d is already in use, so guest port %d is not forwarded; free the port and rerun up",
			blocked.Name,
			blocked.Host,
			blocked.Guest,
		); err != nil {
			return fmt.Errorf("write port conflict warning: %w", err)
		}
	}
	if !state.Running || len(state.Forwards) == 0 {
		return nil
	}
	if _, err := fmt.Fprintf(
		output,
		"tunnel pid %d (%s)\n",
		state.PID,
		describeForwards(state.Forwards),
	); err != nil {
		return fmt.Errorf("write tunnel summary: %w", err)
	}
	return nil
}

// tunnelStatus describes the machine's tunnel for `status`. A CLI assembled
// without port forwarding reports it as unknown rather than claiming a tunnel
// it never manages is stopped.
func (app App) tunnelStatus(machineName string, fallback string) (string, error) {
	if app.Tunnels == nil {
		return fallback, nil
	}
	state, err := app.Tunnels.Inspect(machineName)
	if err != nil {
		return "", fmt.Errorf("inspect the port tunnel of %q: %w", machineName, err)
	}
	return describeTunnel(state), nil
}

func describeTunnel(state tunnel.State) string {
	description := "stopped"
	if state.Running {
		description = fmt.Sprintf("running (pid %d)", state.PID)
		if len(state.Forwards) > 0 {
			description += ": " + describeForwards(state.Forwards)
		}
	}
	// A port conflict outlives the run that found it, so the reason a
	// configured port is unreachable stays visible.
	for _, blocked := range state.Unforwarded {
		description += fmt.Sprintf(
			"; %s not forwarded (macOS port %d in use)",
			blocked.Name,
			blocked.Host,
		)
	}
	return description
}

func describeForwards(forwards []tunnel.Forward) string {
	described := make([]string, 0, len(forwards))
	for _, forward := range forwards {
		described = append(described, forward.String())
	}
	return strings.Join(described, ", ")
}

func declaredForwards(ports []config.Port) []tunnel.Forward {
	forwards := make([]tunnel.Forward, 0, len(ports))
	for _, port := range ports {
		forwards = append(forwards, tunnel.Forward{
			Name:  port.Name,
			Host:  port.Host,
			Guest: port.Guest,
		})
	}
	return forwards
}
