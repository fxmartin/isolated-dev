package tunnel

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// exitPollInterval and exitPollAttempts bound how long a terminating tunnel
	// is given to release its listening sockets before it is killed outright.
	// The next reconciliation rebinds those ports, so waiting is not optional.
	exitPollInterval = 20 * time.Millisecond
	exitPollAttempts = 50
)

// ExecController runs the tunnel as a detached macOS process.
type ExecController struct{}

// Start launches the command in a session of its own, with none of the caller's
// streams attached, so the tunnel survives the CLI exiting and the terminal
// that ran it closing.
func (ExecController) Start(name string, args []string) (int, error) {
	command := exec.Command(name, args...)
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", os.DevNull, err)
	}
	defer devNull.Close()
	command.Stdin = devNull
	command.Stdout = devNull
	command.Stderr = devNull

	if err := command.Start(); err != nil {
		return 0, fmt.Errorf("start %s: %w", name, err)
	}
	pid := command.Process.Pid
	// The CLI exits moments from now and never waits for this process, so the
	// handle is released rather than left behind as an unreaped child.
	if err := command.Process.Release(); err != nil {
		return pid, fmt.Errorf("detach %s: %w", name, err)
	}
	return pid, nil
}

// Running reports whether the process ID is live and still belongs to us.
func (ExecController) Running(pid int, marker string) bool {
	return isMarkedProcess(pid, marker)
}

// Stop terminates the tunnel and waits for it to release its ports. A process
// that has already exited — or a process ID the system has since reused for
// something else — leaves nothing of ours to signal, which is what keeps
// repeated cleanup safe.
func (controller ExecController) Stop(pid int, marker string) error {
	if !isMarkedProcess(pid, marker) {
		return nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find tunnel process %d: %w", pid, err)
	}
	if err := signal(process, syscall.SIGTERM); err != nil {
		return err
	}
	if waitForExit(pid, marker) {
		return nil
	}
	if err := signal(process, syscall.SIGKILL); err != nil {
		return err
	}
	if !waitForExit(pid, marker) {
		return fmt.Errorf("tunnel process %d did not exit", pid)
	}
	return nil
}

func signal(process *os.Process, sig syscall.Signal) error {
	if err := process.Signal(sig); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("send %s to tunnel process %d: %w", sig, process.Pid, err)
	}
	return nil
}

func waitForExit(pid int, marker string) bool {
	for attempt := 0; attempt < exitPollAttempts; attempt++ {
		if !isMarkedProcess(pid, marker) {
			return true
		}
		time.Sleep(exitPollInterval)
	}
	return false
}

// isMarkedProcess reports whether the process ID names a live process whose
// command line carries the marker. Matching the command line rather than
// trusting the number alone is what keeps a recycled process ID from being
// signalled: the tunnel's marker is the project machine name, which no other
// program on the host carries.
func isMarkedProcess(pid int, marker string) bool {
	if pid <= 0 || marker == "" {
		return false
	}
	// -ww keeps a long forwarding command line from being truncated before the
	// marker at its end.
	output, err := exec.Command(
		"/bin/ps", "-ww", "-o", "stat=", "-o", "command=", "-p", strconv.Itoa(pid),
	).Output()
	if err != nil {
		return false
	}
	state, command, _ := strings.Cut(strings.TrimSpace(string(output)), " ")
	// A zombie has exited and released its sockets; only its exit status lingers
	// until a parent that may never wait collects it. Treating it as alive would
	// make Stop wait forever on hosts where nothing reaps detached children.
	if strings.HasPrefix(state, "Z") {
		return false
	}
	return strings.Contains(command, marker)
}

// ProbeHostPort reports whether a macOS loopback port is free to forward to.
//
// It binds and immediately releases the port, which is exactly what the tunnel
// itself does: a listener that is already there is detected without being
// connected to, written to, or disturbed in any way.
func ProbeHostPort(port int) error {
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("macOS port %d is already in use: %w", port, err)
	}
	if err := listener.Close(); err != nil {
		return fmt.Errorf("release macOS port %d: %w", port, err)
	}
	return nil
}
