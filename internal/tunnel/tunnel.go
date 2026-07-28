// Package tunnel maintains the managed background SSH port forwards that make
// guest services reachable from macOS.
//
// The tunnel deliberately outlives the command that creates it: a browser tab
// or an HTTP client must keep reaching the guest while the CLI exits and while
// Zed is closed and reopened. Each project machine has at most one tunnel
// process, identified by a record under a tool-owned directory, so a machine
// that moved to a new address or a tunnel that died is replaced rather than
// duplicated.
package tunnel

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// aliasPattern matches the derived project-machine names, which are both the
// managed SSH host alias and the record file name.
var aliasPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

const recordSchemaVersion = 1

// Forward is one configured mapping from a macOS loopback port to a guest port.
type Forward struct {
	Name  string `json:"name"`
	Host  int    `json:"host"`
	Guest int    `json:"guest"`
}

func (forward Forward) String() string {
	return fmt.Sprintf("%s localhost:%d -> guest:%d", forward.Name, forward.Host, forward.Guest)
}

// Spec is the tunnel one project machine should have.
type Spec struct {
	// MachineName is the managed SSH host alias the tunnel connects through. It
	// also identifies the tunnel process, so a recycled process ID is never
	// mistaken for ours.
	MachineName string
	// Address is the machine address currently behind the alias. It is recorded
	// rather than dialled, so a machine that moved replaces its tunnel.
	Address string
	// User is the provisioned guest account.
	User string
	// Forwards are the mappings the project configuration declares.
	Forwards []Forward
}

// State is what a project machine's tunnel currently is.
type State struct {
	Running bool
	PID     int
	Address string
	// Forwards are the mappings the tunnel process carries.
	Forwards []Forward
	// Unforwarded are the mappings a macOS port conflict kept out of it.
	Unforwarded []Forward
}

// Controller starts, inspects, and terminates the detached tunnel process.
type Controller interface {
	// Start launches the command detached from the caller and returns its
	// process ID.
	Start(name string, args []string) (int, error)
	// Running reports whether the process ID is live and its command line
	// contains marker.
	Running(pid int, marker string) bool
	// Stop terminates the process only while it is still the marked one, and
	// waits for it to release its listening sockets. Stopping a process that is
	// already gone succeeds.
	Stop(pid int, marker string) error
}

// Manager owns the tunnel records and the processes they describe.
type Manager struct {
	// Root is the tool-owned directory holding one record per project machine.
	Root string
	// Controller defaults to running the tunnel as a detached macOS process.
	Controller Controller
	// ProbeHostPort defaults to a macOS loopback bind test. It reports the
	// conflict that keeps a mapping out of the tunnel.
	ProbeHostPort func(port int) error
}

// DefaultManager keeps tunnel records beside the project state, under the
// user's configuration directory.
func DefaultManager() (Manager, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return Manager{}, fmt.Errorf("resolve user configuration directory: %w", err)
	}
	return Manager{Root: filepath.Join(root, "isolated-dev", "tunnels")}, nil
}

// Reconcile converges the machine's tunnel on the declared mappings and reports
// what it forwards.
//
// A live tunnel that already carries exactly the requested mappings from the
// same address is left running, which is what keeps repeated `up` and `open`
// runs from stacking processes on the same macOS ports.
func (manager Manager) Reconcile(spec Spec) (State, error) {
	if err := manager.validate(); err != nil {
		return State{}, err
	}
	if err := validateSpec(spec); err != nil {
		return State{}, err
	}

	existing, err := manager.load(spec.MachineName)
	if err != nil {
		return State{}, err
	}
	if existing != nil && manager.isRunning(*existing) && existing.matches(spec) {
		return existing.state(true), nil
	}
	// Anything else — a moved machine, changed ports, or a process that died —
	// makes the record stale. Our own process is stopped before the ports are
	// probed, because otherwise it is the conflict the probe would find.
	if existing != nil {
		if err := manager.stop(*existing); err != nil {
			return State{}, err
		}
		if err := manager.delete(spec.MachineName); err != nil {
			return State{}, err
		}
	}
	if len(spec.Forwards) == 0 {
		return State{}, nil
	}

	forwards := make([]Forward, 0, len(spec.Forwards))
	var unforwarded []Forward
	for _, forward := range spec.Forwards {
		// A macOS port someone else is listening on is reported and skipped:
		// seizing it is impossible and disrupting it is not ours to do.
		if err := manager.probe(forward.Host); err != nil {
			unforwarded = append(unforwarded, forward)
			continue
		}
		forwards = append(forwards, forward)
	}

	saved := record{
		SchemaVersion: recordSchemaVersion,
		MachineName:   spec.MachineName,
		Address:       spec.Address,
		User:          spec.User,
		Forwards:      forwards,
		Unforwarded:   unforwarded,
	}
	// Even a tunnel that cannot be started is recorded, so `status` can explain
	// why a configured port is unreachable.
	if len(forwards) == 0 {
		if err := manager.save(saved); err != nil {
			return State{}, err
		}
		return saved.state(false), nil
	}

	pid, err := manager.controller().Start("ssh", sshArguments(spec.MachineName, forwards))
	if err != nil {
		return State{}, fmt.Errorf("start the port tunnel for %q: %w", spec.MachineName, err)
	}
	saved.PID = pid
	if err := manager.save(saved); err != nil {
		// A tunnel no record points at is a tunnel no later run can find or
		// replace, so it does not get to keep holding macOS ports.
		if stopErr := manager.stop(saved); stopErr != nil {
			return State{}, fmt.Errorf("%w; the tunnel process %d also survived: %v", err, pid, stopErr)
		}
		return State{}, err
	}
	return saved.state(true), nil
}

// Remove terminates the machine's tunnel and forgets it. Repeating it is safe,
// so both `stop` and `destroy` can always run it.
func (manager Manager) Remove(machineName string) error {
	if err := manager.validate(); err != nil {
		return err
	}
	if err := validateMachineName(machineName); err != nil {
		return err
	}
	existing, err := manager.load(machineName)
	if err != nil || existing == nil {
		return err
	}
	if err := manager.stop(*existing); err != nil {
		return err
	}
	return manager.delete(machineName)
}

// Inspect reports the machine's tunnel without changing it.
func (manager Manager) Inspect(machineName string) (State, error) {
	if err := manager.validate(); err != nil {
		return State{}, err
	}
	if err := validateMachineName(machineName); err != nil {
		return State{}, err
	}
	existing, err := manager.load(machineName)
	if err != nil || existing == nil {
		return State{}, err
	}
	return existing.state(manager.isRunning(*existing)), nil
}

// sshArguments builds a tunnel that binds macOS loopback only, never prompts,
// and exits rather than pretending to forward a port it could not bind.
//
// The target is the managed host alias, so the address, the guest account, and
// the tool-owned host keys all come from the SSH configuration `up` maintains.
func sshArguments(alias string, forwards []Forward) []string {
	args := []string{
		"-N", "-T",
		"-o", "BatchMode=yes",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
	}
	for _, forward := range forwards {
		args = append(args, "-L", fmt.Sprintf(
			"127.0.0.1:%d:127.0.0.1:%d",
			forward.Host,
			forward.Guest,
		))
	}
	return append(args, alias)
}

func (manager Manager) controller() Controller {
	if manager.Controller != nil {
		return manager.Controller
	}
	return ExecController{}
}

func (manager Manager) probe(port int) error {
	if manager.ProbeHostPort != nil {
		return manager.ProbeHostPort(port)
	}
	return ProbeHostPort(port)
}

func (manager Manager) isRunning(existing record) bool {
	return existing.PID > 0 && manager.controller().Running(existing.PID, existing.MachineName)
}

func (manager Manager) stop(existing record) error {
	if existing.PID <= 0 {
		return nil
	}
	if err := manager.controller().Stop(existing.PID, existing.MachineName); err != nil {
		return fmt.Errorf("stop the port tunnel for %q: %w", existing.MachineName, err)
	}
	return nil
}

func (manager Manager) validate() error {
	if manager.Root == "" {
		return errors.New("tunnel state directory is not configured")
	}
	return nil
}

func validateSpec(spec Spec) error {
	if err := validateMachineName(spec.MachineName); err != nil {
		return err
	}
	if err := validateField("machine address", spec.Address); err != nil {
		return err
	}
	if err := validateField("guest user", spec.User); err != nil {
		return err
	}

	hostPorts := make(map[int]string, len(spec.Forwards))
	for _, forward := range spec.Forwards {
		if strings.TrimSpace(forward.Name) == "" {
			return errors.New("every forwarded port needs a name")
		}
		if !validPort(forward.Host) {
			return fmt.Errorf("%s: macOS port %d is out of range", forward.Name, forward.Host)
		}
		if !validPort(forward.Guest) {
			return fmt.Errorf("%s: guest port %d is out of range", forward.Name, forward.Guest)
		}
		if owner, exists := hostPorts[forward.Host]; exists {
			return fmt.Errorf(
				"%s: macOS port %d is already forwarded by %s",
				forward.Name,
				forward.Host,
				owner,
			)
		}
		hostPorts[forward.Host] = forward.Name
	}
	return nil
}

func validateMachineName(value string) error {
	if !aliasPattern.MatchString(value) {
		return fmt.Errorf("invalid machine name %q", value)
	}
	return nil
}

func validateField(name string, value string) error {
	if value == "" || len(strings.Fields(value)) != 1 {
		return fmt.Errorf("invalid %s %q", name, value)
	}
	return nil
}

func validPort(value int) bool {
	return value > 0 && value <= 65535
}
