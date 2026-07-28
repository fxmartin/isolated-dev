package tunnel

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var (
	webForward = Forward{Name: "web", Host: 3000, Guest: 3000}
	apiForward = Forward{Name: "api", Host: 8080, Guest: 8000}
)

const testMachine = "isolated-dev-app-abcd1234"

type controllerStub struct {
	starts   [][]string
	stops    []int
	markers  []string
	lastPID  int
	alive    map[int]bool
	startErr error
	stopErr  error
}

func newControllerStub() *controllerStub {
	return &controllerStub{lastPID: 4242, alive: make(map[int]bool)}
}

func (stub *controllerStub) Start(name string, args []string) (int, error) {
	stub.starts = append(stub.starts, append([]string{name}, args...))
	if stub.startErr != nil {
		return 0, stub.startErr
	}
	stub.lastPID++
	stub.alive[stub.lastPID] = true
	return stub.lastPID, nil
}

func (stub *controllerStub) Running(pid int, _ string) bool {
	return stub.alive[pid]
}

func (stub *controllerStub) Stop(pid int, marker string) error {
	stub.stops = append(stub.stops, pid)
	stub.markers = append(stub.markers, marker)
	if stub.stopErr != nil {
		return stub.stopErr
	}
	delete(stub.alive, pid)
	return nil
}

func testManager(t *testing.T, controller Controller) Manager {
	t.Helper()

	return Manager{
		Root:          t.TempDir(),
		Controller:    controller,
		ProbeHostPort: func(int) error { return nil },
	}
}

func testSpec(forwards ...Forward) Spec {
	return Spec{
		MachineName: testMachine,
		Address:     "192.168.64.5",
		User:        "fx",
		Forwards:    forwards,
	}
}

func TestReconcileStartsOneTunnelBoundToMacOSLoopback(t *testing.T) {
	t.Parallel()

	controller := newControllerStub()
	manager := testManager(t, controller)

	state, err := manager.Reconcile(testSpec(webForward, apiForward))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if !state.Running || state.PID != 4243 {
		t.Fatalf("state = %+v, want the started tunnel", state)
	}
	if len(controller.starts) != 1 {
		t.Fatalf("starts = %v, want exactly one tunnel process", controller.starts)
	}

	argv := controller.starts[0]
	if argv[0] != "ssh" {
		t.Errorf("command = %q, want ssh", argv[0])
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{
		"-L 127.0.0.1:3000:127.0.0.1:3000",
		"-L 127.0.0.1:8080:127.0.0.1:8000",
		"BatchMode=yes",
		"ExitOnForwardFailure=yes",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv = %q, want it to contain %q", joined, want)
		}
	}
	// Binding anything but the macOS loopback address would expose guest
	// services to the local network.
	if strings.Contains(joined, "-L 0.0.0.0") || strings.Contains(joined, "-L *:") {
		t.Errorf("argv = %q, want loopback-only forwards", joined)
	}
	// The managed host alias carries the address, the guest user, and the
	// tool-owned host keys, so it is what the tunnel connects to.
	if argv[len(argv)-1] != testMachine {
		t.Errorf("target = %q, want the managed alias %q", argv[len(argv)-1], testMachine)
	}
}

func TestReconcileLeavesAMatchingLiveTunnelAlone(t *testing.T) {
	t.Parallel()

	controller := newControllerStub()
	manager := testManager(t, controller)
	first, err := manager.Reconcile(testSpec(webForward))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	second, err := manager.Reconcile(testSpec(webForward))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if second.PID != first.PID {
		t.Errorf("pid = %d, want the existing tunnel %d kept", second.PID, first.PID)
	}
	if len(controller.starts) != 1 {
		t.Errorf("starts = %v, want no duplicate tunnel process", controller.starts)
	}
	if len(controller.stops) != 0 {
		t.Errorf("stops = %v, want the live tunnel untouched", controller.stops)
	}
}

func TestReconcileReplacesAStaleTunnelWithoutDuplicating(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		next    Spec
		prepare func(*controllerStub, int)
	}{
		"machine moved": {
			next: Spec{
				MachineName: testMachine,
				Address:     "192.168.64.9",
				User:        "fx",
				Forwards:    []Forward{webForward},
			},
		},
		"ports changed": {
			next: testSpec(webForward, apiForward),
		},
		"tunnel died": {
			next: testSpec(webForward),
			prepare: func(controller *controllerStub, pid int) {
				delete(controller.alive, pid)
			},
		},
	}
	for name, testCase := range cases {
		testCase := testCase
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			controller := newControllerStub()
			manager := testManager(t, controller)
			first, err := manager.Reconcile(testSpec(webForward))
			if err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			if testCase.prepare != nil {
				testCase.prepare(controller, first.PID)
			}

			second, err := manager.Reconcile(testCase.next)
			if err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			if len(controller.starts) != 2 {
				t.Fatalf("starts = %v, want the stale tunnel replaced once", controller.starts)
			}
			if second.PID == first.PID {
				t.Errorf("pid = %d, want a replacement tunnel", second.PID)
			}
			// The replaced process must be gone, or both would hold the same
			// macOS ports.
			if controller.alive[first.PID] {
				t.Errorf("pid %d is still running, want the stale tunnel stopped", first.PID)
			}
			if len(controller.stops) != 1 || controller.stops[0] != first.PID {
				t.Errorf("stops = %v, want only the stale tunnel %d stopped", controller.stops, first.PID)
			}
			if len(controller.markers) != 1 || controller.markers[0] != testMachine {
				t.Errorf("markers = %v, want the machine name identifying our process", controller.markers)
			}
		})
	}
}

func TestReconcileSkipsAConflictingPortWithoutDisturbingTheListener(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()
	taken := listener.Addr().(*net.TCPAddr).Port

	controller := newControllerStub()
	manager := Manager{Root: t.TempDir(), Controller: controller}
	occupied := Forward{Name: "web", Host: taken, Guest: 3000}

	state, err := manager.Reconcile(testSpec(occupied, apiForward))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(state.Unforwarded) != 1 || state.Unforwarded[0] != occupied {
		t.Errorf("unforwarded = %+v, want the conflicting mapping reported", state.Unforwarded)
	}
	if len(state.Forwards) != 1 || state.Forwards[0] != apiForward {
		t.Errorf("forwards = %+v, want the free mapping still forwarded", state.Forwards)
	}
	joined := strings.Join(controller.starts[0], " ")
	if strings.Contains(joined, fmt.Sprintf(":%d:", taken)) {
		t.Errorf("argv = %q, want the conflicting port left out", joined)
	}
	// Nothing may be taken away from whatever already listens there.
	if _, err := net.Dial("tcp", listener.Addr().String()); err != nil {
		t.Errorf("Dial() error = %v, want the existing listener untouched", err)
	}
}

func TestReconcileRecordsAConflictThatBlocksEveryPort(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()
	occupied := Forward{Name: "web", Host: listener.Addr().(*net.TCPAddr).Port, Guest: 3000}

	controller := newControllerStub()
	manager := Manager{Root: t.TempDir(), Controller: controller}

	state, err := manager.Reconcile(testSpec(occupied))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if state.Running || len(controller.starts) != 0 {
		t.Errorf("state = %+v, starts = %v, want no tunnel process", state, controller.starts)
	}
	// The conflict has to survive the command that found it, or `status` could
	// not report why a configured port is unreachable.
	inspected, err := manager.Inspect(testMachine)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if len(inspected.Unforwarded) != 1 || inspected.Unforwarded[0] != occupied {
		t.Errorf("unforwarded = %+v, want the conflict recorded", inspected.Unforwarded)
	}
}

func TestReconcileWithoutPortsRemovesAnExistingTunnel(t *testing.T) {
	t.Parallel()

	controller := newControllerStub()
	manager := testManager(t, controller)
	first, err := manager.Reconcile(testSpec(webForward))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	state, err := manager.Reconcile(testSpec())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if state.Running || state.PID != 0 {
		t.Errorf("state = %+v, want no tunnel", state)
	}
	if controller.alive[first.PID] {
		t.Errorf("pid %d is still running, want it stopped", first.PID)
	}
	if _, err := os.Stat(filepath.Join(manager.Root, testMachine+".json")); !os.IsNotExist(err) {
		t.Errorf("record still present, want it removed")
	}
}

func TestRemoveStopsTheTunnelAndRepeatsSafely(t *testing.T) {
	t.Parallel()

	controller := newControllerStub()
	manager := testManager(t, controller)
	started, err := manager.Reconcile(testSpec(webForward))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	for attempt := 0; attempt < 3; attempt++ {
		if err := manager.Remove(testMachine); err != nil {
			t.Fatalf("Remove() attempt %d error = %v", attempt, err)
		}
	}
	if controller.alive[started.PID] {
		t.Errorf("pid %d is still running, want the tunnel gone", started.PID)
	}
	if len(controller.stops) != 1 {
		t.Errorf("stops = %v, want the tunnel stopped once", controller.stops)
	}
	state, err := manager.Inspect(testMachine)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if state.Running || state.PID != 0 {
		t.Errorf("state = %+v, want a removed tunnel", state)
	}
}

func TestRemoveIsSafeForAMachineThatNeverHadATunnel(t *testing.T) {
	t.Parallel()

	controller := newControllerStub()
	manager := testManager(t, controller)

	if err := manager.Remove(testMachine); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if len(controller.stops) != 0 {
		t.Errorf("stops = %v, want nothing signalled", controller.stops)
	}
}

func TestInspectReportsATunnelWhoseProcessIsGone(t *testing.T) {
	t.Parallel()

	controller := newControllerStub()
	manager := testManager(t, controller)
	started, err := manager.Reconcile(testSpec(webForward))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	delete(controller.alive, started.PID)

	state, err := manager.Inspect(testMachine)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if state.Running {
		t.Errorf("state = %+v, want a stopped tunnel", state)
	}
	if state.Address != "192.168.64.5" || len(state.Forwards) != 1 {
		t.Errorf("state = %+v, want the recorded tunnel described", state)
	}
}

func TestInspectReportsNoTunnelForAnUnknownMachine(t *testing.T) {
	t.Parallel()

	manager := testManager(t, newControllerStub())

	state, err := manager.Inspect(testMachine)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if state.Running || state.PID != 0 || len(state.Forwards) != 0 {
		t.Errorf("state = %+v, want an empty tunnel state", state)
	}
}

func TestReconcileRejectsUnusableSpecifications(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		spec Spec
		want string
	}{
		"empty machine name": {
			spec: Spec{Address: "192.168.64.5", User: "fx", Forwards: []Forward{webForward}},
			want: "machine name",
		},
		"machine name escaping the state directory": {
			spec: Spec{MachineName: "../escape", Address: "192.168.64.5", User: "fx"},
			want: "machine name",
		},
		"missing address": {
			spec: Spec{MachineName: testMachine, User: "fx", Forwards: []Forward{webForward}},
			want: "address",
		},
		"missing guest user": {
			spec: Spec{MachineName: testMachine, Address: "192.168.64.5", Forwards: []Forward{webForward}},
			want: "guest user",
		},
		"unnamed port": {
			spec: testSpec(Forward{Host: 3000, Guest: 3000}),
			want: "name",
		},
		"host port out of range": {
			spec: testSpec(Forward{Name: "web", Host: 70000, Guest: 3000}),
			want: "macOS port",
		},
		"guest port out of range": {
			spec: testSpec(Forward{Name: "web", Host: 3000, Guest: 0}),
			want: "guest port",
		},
		"two mappings on one macOS port": {
			spec: testSpec(webForward, Forward{Name: "api", Host: 3000, Guest: 8000}),
			want: "already forwarded",
		},
	}
	for name, testCase := range cases {
		testCase := testCase
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			controller := newControllerStub()
			manager := testManager(t, controller)

			_, err := manager.Reconcile(testCase.spec)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("Reconcile() error = %v, want it to name %q", err, testCase.want)
			}
			if len(controller.starts) != 0 {
				t.Errorf("starts = %v, want no tunnel process", controller.starts)
			}
		})
	}
}

func TestReconcileReportsBoundaryFailures(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		manager func(*testing.T, *controllerStub) Manager
		want    string
	}{
		"unconfigured state directory": {
			manager: func(_ *testing.T, controller *controllerStub) Manager {
				return Manager{Controller: controller, ProbeHostPort: func(int) error { return nil }}
			},
			want: "tunnel state directory",
		},
		"unwritable state directory": {
			manager: func(t *testing.T, controller *controllerStub) Manager {
				root := filepath.Join(t.TempDir(), "not-a-directory")
				if err := os.WriteFile(root, []byte("x"), 0o600); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
				return Manager{
					Root:          root,
					Controller:    controller,
					ProbeHostPort: func(int) error { return nil },
				}
			},
			want: "tunnel state",
		},
		"process cannot start": {
			manager: func(t *testing.T, controller *controllerStub) Manager {
				controller.startErr = errors.New("ssh is missing")
				return testManager(t, controller)
			},
			want: "ssh is missing",
		},
	}
	for name, testCase := range cases {
		testCase := testCase
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			manager := testCase.manager(t, newControllerStub())
			_, err := manager.Reconcile(testSpec(webForward))
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("Reconcile() error = %v, want it to name %q", err, testCase.want)
			}
		})
	}
}

// A tunnel that cannot be recorded is a tunnel nothing can find again, so it is
// stopped rather than left holding macOS ports no later run knows about.
func TestReconcileStopsATunnelItCannotRecord(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { os.Chmod(root, 0o700) })

	controller := newControllerStub()
	manager := Manager{
		Root:          root,
		Controller:    controller,
		ProbeHostPort: func(int) error { return nil },
	}

	if _, err := manager.Reconcile(testSpec(webForward)); err == nil {
		t.Fatal("Reconcile() error = nil, want the unrecordable tunnel reported")
	}
	for _, pid := range controller.stops {
		delete(controller.alive, pid)
	}
	for pid, alive := range controller.alive {
		if alive {
			t.Errorf("pid %d is still running, want the unrecorded tunnel stopped", pid)
		}
	}
}

func TestInspectReportsAnUnreadableRecord(t *testing.T) {
	t.Parallel()

	manager := testManager(t, newControllerStub())
	if err := os.WriteFile(
		filepath.Join(manager.Root, testMachine+".json"),
		[]byte("not json"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := manager.Inspect(testMachine); err == nil {
		t.Fatal("Inspect() error = nil, want the unreadable record reported")
	}
	if err := manager.Remove(testMachine); err == nil {
		t.Fatal("Remove() error = nil, want the unreadable record reported")
	}
}

func TestRemoveReportsAFailedStop(t *testing.T) {
	t.Parallel()

	controller := newControllerStub()
	manager := testManager(t, controller)
	if _, err := manager.Reconcile(testSpec(webForward)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	controller.stopErr = errors.New("operation not permitted")

	err := manager.Remove(testMachine)
	if err == nil || !strings.Contains(err.Error(), "operation not permitted") {
		t.Fatalf("Remove() error = %v, want the failure reported", err)
	}
	// The record outlives a failed stop: dropping it would strand a process no
	// later run could find.
	if _, statErr := os.Stat(filepath.Join(manager.Root, testMachine+".json")); statErr != nil {
		t.Errorf("Stat() error = %v, want the record kept", statErr)
	}
}

func TestRemoveRejectsAnUnusableMachineName(t *testing.T) {
	t.Parallel()

	manager := testManager(t, newControllerStub())

	if err := manager.Remove("../escape"); err == nil {
		t.Fatal("Remove() error = nil, want the machine name rejected")
	}
	if _, err := manager.Inspect(""); err == nil {
		t.Fatal("Inspect() error = nil, want the machine name rejected")
	}
}

func TestForwardDescribesItsMapping(t *testing.T) {
	t.Parallel()

	if got := webForward.String(); got != "web localhost:3000 -> guest:3000" {
		t.Errorf("String() = %q, want the configured mapping", got)
	}
}

func TestDefaultManagerUsesTheUserConfigurationDirectory(t *testing.T) {
	t.Parallel()

	manager, err := DefaultManager()
	if err != nil {
		t.Fatalf("DefaultManager() error = %v", err)
	}
	if !filepath.IsAbs(manager.Root) || filepath.Base(manager.Root) != "tunnels" {
		t.Errorf("Root = %q, want an absolute tunnels directory", manager.Root)
	}
}
