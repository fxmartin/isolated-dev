package tunnel

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// startTestProcess launches a harmless long-running process the way the tunnel
// itself is launched, so the controller can be exercised against a real macOS
// process without needing a project machine.
func startTestProcess(t *testing.T) (ExecController, int) {
	t.Helper()

	controller := ExecController{}
	pid, err := controller.Start("/bin/sleep", []string{"120"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		if err := controller.Stop(pid, "sleep"); err != nil {
			t.Errorf("Stop() error = %v", err)
		}
	})
	return controller, pid
}

// The tunnel has to outlive the command that starts it, which it can only do
// from a session of its own: a process left in the CLI's process group dies
// with the terminal that ran it.
func TestExecControllerStartsTheProcessInItsOwnSession(t *testing.T) {
	t.Parallel()

	_, pid := startTestProcess(t)

	group, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatalf("Getpgid() error = %v", err)
	}
	own, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		t.Fatalf("Getpgid() error = %v", err)
	}
	if group == own {
		t.Errorf("process group = %d, want one detached from the caller's %d", group, own)
	}
}

func TestExecControllerRecognisesAndStopsItsOwnProcess(t *testing.T) {
	t.Parallel()

	controller, pid := startTestProcess(t)

	if !controller.Running(pid, "sleep") {
		t.Fatalf("Running(%d) = false, want the started process recognised", pid)
	}
	if err := controller.Stop(pid, "sleep"); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if controller.Running(pid, "sleep") {
		t.Errorf("Running(%d) = true, want the process stopped", pid)
	}
	// Repeating cleanup has to stay safe, because stop and destroy both run it.
	if err := controller.Stop(pid, "sleep"); err != nil {
		t.Errorf("Stop() error = %v, want repeated cleanup to succeed", err)
	}
}

// A process ID outlives the process that held it, so signalling one whose
// command line is not ours would kill an unrelated program.
func TestExecControllerLeavesAnUnrelatedProcessAlone(t *testing.T) {
	t.Parallel()

	controller, pid := startTestProcess(t)

	if controller.Running(pid, "isolated-dev-app-abcd1234") {
		t.Errorf("Running(%d) = true, want a foreign command line rejected", pid)
	}
	if err := controller.Stop(pid, "isolated-dev-app-abcd1234"); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if !controller.Running(pid, "sleep") {
		t.Errorf("Running(%d) = false, want the unrelated process untouched", pid)
	}
}

// detachedPIDEnv puts this test into the child mode that stands in for the CLI:
// it starts a tunnel and exits immediately, which is exactly what `up` does.
const detachedPIDEnv = "ISOLATED_DEV_TEST_DETACHED_CHILD"

var detachedPIDPattern = regexp.MustCompile(`detached-pid:(\d+)`)

// The whole point of the managed tunnel is that it keeps serving after the
// command that created it is gone, so the process has to survive its parent.
func TestExecControllerKeepsTheProcessAliveAfterItsParentExits(t *testing.T) {
	if os.Getenv(detachedPIDEnv) != "" {
		pid, err := (ExecController{}).Start("/bin/sleep", []string{"120"})
		if err != nil {
			t.Fatalf("Start() error = %v", err)
		}
		fmt.Printf("detached-pid:%d\n", pid)
		return
	}
	t.Parallel()

	parent := exec.Command(
		os.Args[0],
		"-test.run=^TestExecControllerKeepsTheProcessAliveAfterItsParentExits$",
	)
	parent.Env = append(os.Environ(), detachedPIDEnv+"=1")
	// Output waits for the stand-in parent to exit, so anything still running
	// afterwards outlived it.
	output, err := parent.Output()
	if err != nil {
		t.Fatalf("run the detaching parent: %v\n%s", err, output)
	}
	match := detachedPIDPattern.FindSubmatch(output)
	if match == nil {
		t.Fatalf("output = %q, want a reported process ID", output)
	}
	pid, err := strconv.Atoi(string(match[1]))
	if err != nil {
		t.Fatalf("Atoi() error = %v", err)
	}

	controller := ExecController{}
	t.Cleanup(func() {
		if err := controller.Stop(pid, "sleep"); err != nil {
			t.Errorf("Stop() error = %v", err)
		}
	})
	if !controller.Running(pid, "sleep") {
		t.Errorf("Running(%d) = false, want the tunnel to outlive the CLI", pid)
	}
}

func TestExecControllerReportsAnUnknownProcessAsStopped(t *testing.T) {
	t.Parallel()

	controller := ExecController{}
	for _, pid := range []int{0, -1, 4194303} {
		if controller.Running(pid, "sleep") {
			t.Errorf("Running(%d) = true, want no process", pid)
		}
		if err := controller.Stop(pid, "sleep"); err != nil {
			t.Errorf("Stop(%d) error = %v, want a safe no-op", pid, err)
		}
	}
}

func TestExecControllerReportsAnUnstartableCommand(t *testing.T) {
	t.Parallel()

	if _, err := (ExecController{}).Start("isolated-dev-not-a-command", nil); err == nil {
		t.Fatal("Start() error = nil, want the missing command reported")
	}
}

func TestProbeHostPortAcceptsAFreeMacOSPort(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	// The kernel needs a moment to release a just-closed listener on a busy
	// host, so the probe is given the same chance the tunnel would get.
	var probeErr error
	for attempt := 0; attempt < 20; attempt++ {
		if probeErr = ProbeHostPort(port); probeErr == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("ProbeHostPort() error = %v, want a free port accepted", probeErr)
}

func TestProbeHostPortReportsAnOccupiedMacOSPort(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port

	err = ProbeHostPort(port)
	if err == nil || !strings.Contains(err.Error(), strconv.Itoa(port)) {
		t.Fatalf("ProbeHostPort() error = %v, want the occupied port named", err)
	}
}
