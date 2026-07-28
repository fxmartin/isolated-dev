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

	"github.com/fxmartin/isolated-dev/internal/tunnel"
)

var (
	webForward = tunnel.Forward{Name: "web", Host: 3001, Guest: 3000}
	apiForward = tunnel.Forward{Name: "api", Host: 8001, Guest: 8000}
)

const portConfiguration = "version = 1\n" +
	"[[ports]]\nname = \"web\"\nguest = 3000\nhost = 3001\n" +
	"[[ports]]\nname = \"api\"\nguest = 8000\nhost = 8001\n"

type tunnelStub struct {
	specs        []tunnel.Spec
	removed      []string
	inspected    []string
	reconciled   tunnel.State
	state        tunnel.State
	reconcileErr error
	removeErr    error
	inspectErr   error
}

func (stub *tunnelStub) Reconcile(spec tunnel.Spec) (tunnel.State, error) {
	stub.specs = append(stub.specs, spec)
	return stub.reconciled, stub.reconcileErr
}

func (stub *tunnelStub) Remove(machineName string) error {
	stub.removed = append(stub.removed, machineName)
	return stub.removeErr
}

func (stub *tunnelStub) Inspect(machineName string) (tunnel.State, error) {
	stub.inspected = append(stub.inspected, machineName)
	return stub.state, stub.inspectErr
}

// tunnelRepository prepares a repository whose configuration declares two
// forwarded ports.
func tunnelRepository(t *testing.T, home string) string {
	t.Helper()

	repository := filepath.Join(home, "app")
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(repository, ".isolated-dev.toml"),
		[]byte(portConfiguration),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return repository
}

func TestUpForwardsTheConfiguredPortsThroughOneManagedTunnel(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository := tunnelRepository(t, home)
	tunnels := &tunnelStub{reconciled: tunnel.State{
		Running:  true,
		PID:      4242,
		Address:  "192.168.64.5",
		Forwards: []tunnel.Forward{webForward, apiForward},
	}}
	application := upApp(t, home, repository, &lifecycleStub{})
	application.Tunnels = tunnels
	_, machineName := sshRepository(t, home)

	var summary bytes.Buffer
	if err := application.Up(context.Background(), repository, &summary); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if len(tunnels.specs) != 1 {
		t.Fatalf("specs = %+v, want exactly one reconciled tunnel", tunnels.specs)
	}
	spec := tunnels.specs[0]
	if spec.MachineName != machineName || spec.Address != "192.168.64.5" || spec.User != "fx" {
		t.Errorf("spec = %+v, want the managed host of %q", spec, machineName)
	}
	want := []tunnel.Forward{webForward, apiForward}
	if len(spec.Forwards) != 2 || spec.Forwards[0] != want[0] || spec.Forwards[1] != want[1] {
		t.Errorf("forwards = %+v, want %+v", spec.Forwards, want)
	}
	// The tunnel is reported after the SSH host it connects through.
	wantLine := "tunnel pid 4242 (web localhost:3001 -> guest:3000, api localhost:8001 -> guest:8000)\n"
	if !strings.HasSuffix(summary.String(), wantLine) {
		t.Errorf("summary = %q, want it to end with %q", summary.String(), wantLine)
	}
}

func TestOpenForwardsTheConfiguredPorts(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository := tunnelRepository(t, home)
	tunnels := &tunnelStub{reconciled: tunnel.State{
		Running:  true,
		PID:      4242,
		Forwards: []tunnel.Forward{webForward},
	}}
	application := upApp(t, home, repository, &lifecycleStub{})
	application.Tunnels = tunnels

	if err := application.Open(context.Background(), repository, io.Discard); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if len(tunnels.specs) != 1 {
		t.Errorf("specs = %+v, want the tunnel reconciled by open", tunnels.specs)
	}
}

// A macOS port someone else is listening on is reported; the listener keeps its
// socket and the remaining ports still reach the guest.
func TestUpReportsAHostPortConflictWithoutDisruptingTheListener(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository := tunnelRepository(t, home)
	application := upApp(t, home, repository, &lifecycleStub{})
	application.Tunnels = &tunnelStub{reconciled: tunnel.State{
		Running:     true,
		PID:         4242,
		Forwards:    []tunnel.Forward{apiForward},
		Unforwarded: []tunnel.Forward{webForward},
	}}
	var warnings, summary bytes.Buffer
	application.WarningOutput = &warnings

	if err := application.Up(context.Background(), repository, &summary); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if !strings.Contains(warnings.String(), "web: macOS port 3001 is already in use") {
		t.Errorf("warnings = %q, want the affected mapping reported", warnings.String())
	}
	if !strings.Contains(warnings.String(), "guest port 3000 is not forwarded") {
		t.Errorf("warnings = %q, want the unforwarded guest port named", warnings.String())
	}
	if !strings.Contains(summary.String(), "tunnel pid 4242 (api localhost:8001 -> guest:8000)") {
		t.Errorf("summary = %q, want the remaining port forwarded", summary.String())
	}
}

func TestUpReconcilesNoTunnelWhenNoPortsAreConfigured(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository, _ := sshRepository(t, home)
	tunnels := &tunnelStub{}
	application := upApp(t, home, repository, &lifecycleStub{})
	application.Tunnels = tunnels

	var summary bytes.Buffer
	if err := application.Up(context.Background(), repository, &summary); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	// Reconciliation still runs, because a project that dropped its last port
	// must not keep a tunnel from an earlier run.
	if len(tunnels.specs) != 1 || len(tunnels.specs[0].Forwards) != 0 {
		t.Errorf("specs = %+v, want one reconciliation without forwards", tunnels.specs)
	}
	if strings.Contains(summary.String(), "tunnel") {
		t.Errorf("summary = %q, want no tunnel reported", summary.String())
	}
}

// A recreated machine — which is what `upgrade` leaves behind — may come back
// at the address its predecessor used, so the tunnel that still points at the
// machine that is gone has to be dropped rather than recognised as current.
func TestUpDropsTheTunnelOfAReplacedMachine(t *testing.T) {
	t.Parallel()

	for name, existing := range map[string]bool{"created": false, "restarted": true} {
		existing := existing
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			repository := tunnelRepository(t, home)
			tunnels := &tunnelStub{}
			application := upApp(t, home, repository, &lifecycleStub{upExisting: existing})
			application.Tunnels = tunnels

			if err := application.Up(context.Background(), repository, io.Discard); err != nil {
				t.Fatalf("Up() error = %v", err)
			}
			if len(tunnels.specs) != 1 {
				t.Fatalf("specs = %+v, want the tunnel reconciled", tunnels.specs)
			}
			if existing && len(tunnels.removed) != 0 {
				t.Errorf("removed = %v, want the tunnel of a running machine kept", tunnels.removed)
			}
			if !existing && len(tunnels.removed) != 1 {
				t.Errorf("removed = %v, want the tunnel of the replaced machine dropped", tunnels.removed)
			}
		})
	}
}

func TestUpReportsTunnelFailures(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository := tunnelRepository(t, home)
	application := upApp(t, home, repository, &lifecycleStub{})
	application.Tunnels = &tunnelStub{reconcileErr: errors.New("ssh is missing")}

	err := application.Up(context.Background(), repository, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "ssh is missing") {
		t.Fatalf("Up() error = %v, want the tunnel failure reported", err)
	}
}

func TestUpRequiresPortForwardingBeforeLifecycleMutation(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository := tunnelRepository(t, home)
	lifecycle := &lifecycleStub{}
	application := upApp(t, home, repository, lifecycle)
	application.Tunnels = nil

	err := application.Up(context.Background(), repository, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "port forwarding is not configured") {
		t.Fatalf("Up() error = %v, want a port forwarding rejection", err)
	}
	if len(lifecycle.upRequests) != 0 {
		t.Fatalf("up requests = %+v, want no lifecycle mutation", lifecycle.upRequests)
	}
}

func TestStopAndDestroyRemoveTheManagedTunnel(t *testing.T) {
	t.Parallel()

	cases := map[string]func(App, string) error{
		"stop": func(application App, path string) error {
			return application.Stop(context.Background(), path)
		},
		"destroy": func(application App, path string) error {
			return application.Destroy(context.Background(), path)
		},
	}
	for name, operation := range cases {
		operation := operation
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			repository, machineName := sshRepository(t, home)
			tunnels := &tunnelStub{}
			application := upApp(t, home, repository, &lifecycleStub{})
			application.Tunnels = tunnels

			// Repeating cleanup has to stay safe.
			for attempt := 0; attempt < 2; attempt++ {
				if err := operation(application, repository); err != nil {
					t.Fatalf("%s() attempt %d error = %v", name, attempt, err)
				}
			}
			if len(tunnels.removed) != 2 {
				t.Fatalf("removed = %v, want the tunnel cleaned up each time", tunnels.removed)
			}
			if tunnels.removed[0] != machineName {
				t.Errorf("removed = %q, want the tunnel of %q", tunnels.removed[0], machineName)
			}
		})
	}
}

func TestStopAndDestroyKeepTheTunnelWhenTheMachineSurvives(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		lifecycle *lifecycleStub
		operation func(App, string) error
	}{
		"stop": {
			lifecycle: &lifecycleStub{stopErr: errors.New("machine is busy")},
			operation: func(application App, path string) error {
				return application.Stop(context.Background(), path)
			},
		},
		"destroy": {
			lifecycle: &lifecycleStub{destroyErr: errors.New("machine is in use")},
			operation: func(application App, path string) error {
				return application.Destroy(context.Background(), path)
			},
		},
	}
	for name, testCase := range cases {
		testCase := testCase
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			repository, _ := sshRepository(t, home)
			tunnels := &tunnelStub{}
			application := upApp(t, home, repository, testCase.lifecycle)
			application.Tunnels = tunnels

			if err := testCase.operation(application, repository); err == nil {
				t.Fatalf("%s() error = nil, want the lifecycle failure reported", name)
			}
			if len(tunnels.removed) != 0 {
				t.Errorf("removed = %v, want the tunnel kept for the surviving machine", tunnels.removed)
			}
		})
	}
}

func TestStopAndDestroyReportFailedTunnelCleanup(t *testing.T) {
	t.Parallel()

	cases := map[string]func(App, string) error{
		"stop": func(application App, path string) error {
			return application.Stop(context.Background(), path)
		},
		"destroy": func(application App, path string) error {
			return application.Destroy(context.Background(), path)
		},
	}
	for name, operation := range cases {
		operation := operation
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			repository, _ := sshRepository(t, home)
			application := upApp(t, home, repository, &lifecycleStub{})
			application.Tunnels = &tunnelStub{removeErr: errors.New("operation not permitted")}

			err := operation(application, repository)
			if err == nil || !strings.Contains(err.Error(), "operation not permitted") {
				t.Fatalf("%s() error = %v, want the cleanup failure reported", name, err)
			}
		})
	}
}

func TestStopAndDestroyRequirePortForwarding(t *testing.T) {
	t.Parallel()

	cases := map[string]func(App, string) error{
		"stop": func(application App, path string) error {
			return application.Stop(context.Background(), path)
		},
		"destroy": func(application App, path string) error {
			return application.Destroy(context.Background(), path)
		},
	}
	for name, operation := range cases {
		operation := operation
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			repository, _ := sshRepository(t, home)
			lifecycle := &lifecycleStub{}
			application := upApp(t, home, repository, lifecycle)
			application.Tunnels = nil

			err := operation(application, repository)
			if err == nil || !strings.Contains(err.Error(), "port forwarding is not configured") {
				t.Fatalf("%s() error = %v, want a port forwarding rejection", name, err)
			}
			if len(lifecycle.stopped)+len(lifecycle.destroyed) != 0 {
				t.Errorf("lifecycle was mutated, want the rejection first")
			}
		})
	}
}

func TestStatusReportsTunnelState(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		state tunnel.State
		want  string
	}{
		"running": {
			state: tunnel.State{
				Running:  true,
				PID:      4242,
				Forwards: []tunnel.Forward{webForward},
			},
			want: "Tunnel: running (pid 4242): web localhost:3001 -> guest:3000",
		},
		"stopped": {
			state: tunnel.State{},
			want:  "Tunnel: stopped",
		},
		"process gone": {
			state: tunnel.State{PID: 4242, Forwards: []tunnel.Forward{webForward}},
			want:  "Tunnel: stopped",
		},
		"port conflict": {
			state: tunnel.State{
				Running:     true,
				PID:         4242,
				Forwards:    []tunnel.Forward{apiForward},
				Unforwarded: []tunnel.Forward{webForward},
			},
			want: "web not forwarded (macOS port 3001 in use)",
		},
	}
	for name, testCase := range cases {
		testCase := testCase
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			repository := tunnelRepository(t, home)
			application := upApp(t, home, repository, &lifecycleStub{})
			application.Tunnels = &tunnelStub{state: testCase.state}

			var output bytes.Buffer
			if err := application.Status(context.Background(), repository, &output); err != nil {
				t.Fatalf("Status() error = %v", err)
			}
			if !strings.Contains(output.String(), testCase.want) {
				t.Errorf("status output missing %q:\n%s", testCase.want, output.String())
			}
		})
	}
}

func TestStatusReportsAnUninspectableTunnel(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository := tunnelRepository(t, home)
	application := upApp(t, home, repository, &lifecycleStub{})
	application.Tunnels = &tunnelStub{inspectErr: errors.New("tunnel state is unreadable")}

	err := application.Status(context.Background(), repository, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "tunnel state is unreadable") {
		t.Fatalf("Status() error = %v, want the tunnel failure reported", err)
	}
}
