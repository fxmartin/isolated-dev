package forge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The cached readiness targets the requirements set. They are measured and
// reported rather than enforced: a restart that works but is slow is a finding
// about this host, not a broken environment.
const (
	// MachineReadyTarget bounds a cached `up`, which is what Zed connects
	// through.
	MachineReadyTarget = 30 * time.Second
	// StackReadyTarget bounds the cached Forge DEV stack becoming healthy and
	// answering on macOS.
	StackReadyTarget = 2 * time.Minute
)

// DefaultMarkerName is the temporary file the mounted-edit round trip writes
// into the project. It is created, read from both sides of the mount, and
// removed again on every path.
const DefaultMarkerName = ".isolated-dev-persistence-check"

// guestCopySuffix names the Linux-created counterpart of the marker, which is
// what proves a file made in the guest is usable from macOS.
const guestCopySuffix = ".guest"

// volumeFormat reads the identity of a named volume. A volume that was
// recreated rather than preserved reports a new creation timestamp, so the
// three fields together are what distinguishes "the same volume" from "a volume
// with the same name".
const volumeFormat = "{{.Driver}} {{.Mountpoint}} {{.CreatedAt}}"

// Volume is one named Compose volume whose contents have to outlive a machine
// restart.
type Volume struct {
	Name string
	// Description is how the volume is named in the acceptance criteria, so a
	// failure reads as the data it holds rather than as a volume id.
	Description string
}

// DevVolumes are the named volumes the Forge DEV profile declares: the database
// and the application data.
func DevVolumes() []Volume {
	return []Volume{
		{Name: "rosetta-db-dev", Description: "the PostgreSQL 16 database"},
		{Name: "rosetta-data-dev", Description: "the application data"},
	}
}

// VolumeIdentity is what a named volume was at one point in time.
type VolumeIdentity struct {
	Driver     string
	Mountpoint string
	// CreatedAt is Docker's own creation timestamp, which changes only when the
	// volume is recreated.
	CreatedAt string
	// Entries are the top-level names inside the volume, sorted. They are the
	// data itself rather than the container that holds it.
	Entries []string
}

// VolumeState is one named volume before and after the restart.
type VolumeState struct {
	Volume
	Before VolumeIdentity
	After  VolumeIdentity
	// Preserved reports that the restart returned the same volume with at least
	// the data it had. Difference explains a false.
	Preserved  bool
	Difference string
}

// PortLifecycle is what the configured macOS ports did across the restart.
type PortLifecycle struct {
	// BeforeStop are the endpoints reached from macOS while no CLI command was
	// running, which is what the tunnel outliving the CLI means in practice.
	BeforeStop []EndpointState
	// ClosedAfterStop are the endpoints that stopped answering once the machine
	// was stopped.
	ClosedAfterStop []Endpoint
	// AfterRestart are the endpoints reached again through the reconciled
	// tunnel, whose address the restart is free to have changed.
	AfterRestart []EndpointState
}

// EditRoundTrip is what a source edit did across the mount, in both directions.
type EditRoundTrip struct {
	// HostPath is the macOS path of the marker, GuestPath its Linux path.
	HostPath  string
	GuestPath string
	// HostEditRead reports that Linux read back what macOS wrote.
	HostEditRead bool
	// GuestFileRead reports that macOS read back what Linux created.
	GuestFileRead bool
	// GuestUID and GuestGID are the ownership Linux sees on the guest-created
	// file; HostUID and HostGID are what macOS sees on the same file.
	GuestUID int
	GuestGID int
	HostUID  int
	HostGID  int
	// OwnershipMatched reports that both sides see the file as the developer's
	// own, which is what makes ordinary editing, building, and Git work.
	OwnershipMatched bool
}

// Timing is one measured interval against its target.
type Timing struct {
	Label   string
	Elapsed time.Duration
	Target  time.Duration
}

func (timing Timing) Met() bool {
	return timing.Elapsed <= timing.Target
}

func (timing Timing) String() string {
	outcome := "met"
	if !timing.Met() {
		outcome = "missed"
	}
	return fmt.Sprintf(
		"%s took %s against a %s target (%s)",
		timing.Label,
		timing.Elapsed.Round(time.Millisecond),
		timing.Target,
		outcome,
	)
}

// PersistenceReport is the evidence a persistence run collected. It is filled
// in as the run progresses, so a failed run still describes how far it got.
type PersistenceReport struct {
	Volumes []VolumeState
	Ports   PortLifecycle
	Edit    EditRoundTrip
	// Restart is the acceptance result of the DEV stack the restart brought
	// back.
	Restart Result
	Timings []Timing
}

// MissedTargets are the measurements that exceeded their target.
func (report PersistenceReport) MissedTargets() []Timing {
	var missed []Timing
	for _, timing := range report.Timings {
		if !timing.Met() {
			missed = append(missed, timing)
		}
	}
	return missed
}

// Lifecycle stops and restarts the project machine through the CLI's own
// commands, so a persistence run proves what a developer's day does rather than
// what a test-only restart does.
type Lifecycle interface {
	Stop(ctx context.Context, projectPath string) error
	Up(ctx context.Context, projectPath string, output io.Writer) error
}

// Persistence proves the Forge environment supports daily work and not only
// startup: its named volumes survive a machine restart, its macOS ports follow
// the machine's lifecycle, and edits made on either side of the mount are
// usable on the other.
//
// It inherits the acceptance run's rule about the workload: it owns nothing and
// removes nothing. The only things it ever writes are two marker files inside
// the project, and it removes both on every path, including a failing one.
type Persistence struct {
	Acceptance
	Lifecycle Lifecycle
	// Now defaults to time.Now and measures the cached readiness targets.
	Now func() time.Time
	// ClosureTries bounds the wait for the macOS ports to stop answering after
	// the machine is stopped.
	ClosureTries int
}

// PersistenceRequest describes one persistence run against an existing project
// machine and its already-running DEV stack.
type PersistenceRequest struct {
	Request
	// Volumes default to the named volumes the DEV profile declares.
	Volumes []Volume
	// GuestUID and GuestGID are the provisioned identity `up` recorded, which
	// the guest-created file has to carry.
	GuestUID int
	GuestGID int
	// MarkerName is the project-relative file the mounted-edit round trip uses.
	MarkerName string
}

func (request PersistenceRequest) withDefaults() PersistenceRequest {
	request.Request = request.Request.withDefaults()
	if request.Volumes == nil {
		request.Volumes = DevVolumes()
	}
	if request.MarkerName == "" {
		request.MarkerName = DefaultMarkerName
	}
	return request
}

// Validate runs the whole day: it proves the running stack is reachable from
// macOS, that both sides of the mount can edit the repository, that stopping
// the machine closes the macOS ports, and that restarting it brings the same
// data and the same reachable stack back.
//
// A failure returns the partially filled report alongside the error, so the
// evidence gathered before it is still reviewable.
func (persistence Persistence) Validate(
	ctx context.Context,
	request PersistenceRequest,
) (PersistenceReport, error) {
	request = request.withDefaults()
	declared, err := persistence.prepare(request.Request)
	if err != nil {
		return PersistenceReport{}, err
	}
	if err := persistence.validatePersistenceRequest(request); err != nil {
		return PersistenceReport{}, err
	}

	var report PersistenceReport
	if err := persistence.perform(ctx, request, declared.Args, &report); err != nil {
		return report, persistence.diagnose(ctx, request.Request, err)
	}
	return report, nil
}

// perform runs the steps in the only order that proves anything: everything
// that describes the machine as it is has to be captured before it is stopped.
func (persistence Persistence) perform(
	ctx context.Context,
	request PersistenceRequest,
	declaredArgs []string,
	report *PersistenceReport,
) error {
	before, err := persistence.captureVolumes(ctx, request)
	if err != nil {
		return err
	}
	// The tunnel and the stack were left by an earlier command that has since
	// exited, so reaching them now is what "reachable after the CLI exits"
	// means.
	var running Result
	if err := persistence.reachEndpoints(ctx, request.Request, &running); err != nil {
		return fmt.Errorf("the Forge DEV stack is not reachable from macOS before the restart: %w", err)
	}
	report.Ports.BeforeStop = running.Endpoints

	roundTrip, err := persistence.checkMountedEdits(ctx, request)
	report.Edit = roundTrip
	if err != nil {
		return err
	}

	if err := persistence.Lifecycle.Stop(ctx, request.ProjectPath); err != nil {
		return fmt.Errorf("stop the project machine %q: %w", request.MachineName, err)
	}
	closed, err := persistence.confirmPortsClosed(ctx, request)
	report.Ports.ClosedAfterStop = closed
	if err != nil {
		return err
	}

	restarted, err := persistence.restart(ctx, request, declaredArgs, report)
	if err != nil {
		return err
	}
	report.Restart = restarted
	report.Ports.AfterRestart = restarted.Endpoints

	after, err := persistence.captureVolumes(ctx, request)
	if err != nil {
		return err
	}
	states, err := compareVolumes(request.Volumes, before, after)
	report.Volumes = states
	return err
}

// restart brings the machine and the DEV stack back and measures both against
// their cached readiness targets. The stack is started through the same
// acceptance run that first proved it, so a restarted stack is held to exactly
// the same standard as a fresh one.
func (persistence Persistence) restart(
	ctx context.Context,
	request PersistenceRequest,
	declaredArgs []string,
	report *PersistenceReport,
) (Result, error) {
	machineStarted := persistence.now()
	if err := persistence.Lifecycle.Up(ctx, request.ProjectPath, persistence.restartOutput()); err != nil {
		return Result{}, fmt.Errorf("restart the project machine %q: %w", request.MachineName, err)
	}
	report.Timings = append(report.Timings, Timing{
		Label:   "cached machine restart",
		Elapsed: persistence.now().Sub(machineStarted),
		Target:  MachineReadyTarget,
	})

	stackStarted := persistence.now()
	restarted, err := persistence.Acceptance.Run(ctx, request.Request)
	report.Timings = append(report.Timings, Timing{
		Label:   "cached Forge DEV readiness",
		Elapsed: persistence.now().Sub(stackStarted),
		Target:  StackReadyTarget,
	})
	if err != nil {
		return restarted, fmt.Errorf(
			"the Forge DEV stack did not come back after the restart with `%s`: %w",
			strings.Join(declaredArgs, " "),
			err,
		)
	}
	return restarted, nil
}

// captureVolumes records what each named volume is, so the restart can be shown
// to have preserved it rather than to have created a new one under the same
// name. Every command it runs reads.
func (persistence Persistence) captureVolumes(
	ctx context.Context,
	request PersistenceRequest,
) ([]VolumeIdentity, error) {
	args := []string{"docker", "volume", "inspect", "--format", volumeFormat}
	for _, volume := range request.Volumes {
		args = append(args, volume.Name)
	}
	output, err := persistence.guest(ctx, request.MachineName, args...)
	if err != nil {
		return nil, fmt.Errorf(
			"inspect the Forge DEV named volumes: %w\n%s",
			err,
			output,
		)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != len(request.Volumes) {
		return nil, fmt.Errorf(
			"the machine reported %d named volumes, want %d:\n%s",
			len(lines),
			len(request.Volumes),
			output,
		)
	}
	identities := make([]VolumeIdentity, 0, len(request.Volumes))
	for index, volume := range request.Volumes {
		fields := strings.Fields(lines[index])
		if len(fields) != 3 {
			return nil, fmt.Errorf(
				"could not read the identity of %s (%s): %q",
				volume.Name,
				volume.Description,
				lines[index],
			)
		}
		identity := VolumeIdentity{
			Driver:     fields[0],
			Mountpoint: fields[1],
			CreatedAt:  fields[2],
		}
		entries, err := persistence.volumeEntries(ctx, request.MachineName, volume, identity.Mountpoint)
		if err != nil {
			return nil, err
		}
		identity.Entries = entries
		identities = append(identities, identity)
	}
	return identities, nil
}

// volumeEntries lists what the volume holds. The top-level names are stable
// while a database writes, which the contents of the files below them are not.
func (persistence Persistence) volumeEntries(
	ctx context.Context,
	machineName string,
	volume Volume,
	mountpoint string,
) ([]string, error) {
	output, err := persistence.guest(ctx, machineName, "ls", "-A", mountpoint)
	if err != nil {
		return nil, fmt.Errorf(
			"list %s (%s) at %s: %w\n%s",
			volume.Name,
			volume.Description,
			mountpoint,
			err,
			output,
		)
	}
	entries := strings.Fields(string(output))
	slices.Sort(entries)
	return entries, nil
}

// compareVolumes reports whether each named volume came back as itself. A
// volume that gained files while the stack ran is preserved; one that lost any
// of them, or that carries a new creation timestamp, is not.
func compareVolumes(
	volumes []Volume,
	before []VolumeIdentity,
	after []VolumeIdentity,
) ([]VolumeState, error) {
	states := make([]VolumeState, 0, len(volumes))
	var failure error
	for index, volume := range volumes {
		state := VolumeState{
			Volume:    volume,
			Before:    before[index],
			After:     after[index],
			Preserved: true,
		}
		if difference := describeVolumeDifference(state.Before, state.After); difference != "" {
			state.Preserved = false
			state.Difference = difference
			if failure == nil {
				failure = fmt.Errorf(
					"%s (%s) did not survive the machine restart: %s",
					volume.Name,
					volume.Description,
					difference,
				)
			}
		}
		states = append(states, state)
	}
	return states, failure
}

func describeVolumeDifference(before VolumeIdentity, after VolumeIdentity) string {
	if before.CreatedAt != after.CreatedAt {
		return fmt.Sprintf(
			"it was recreated (created %s, now created %s), so the data it held is gone",
			before.CreatedAt,
			after.CreatedAt,
		)
	}
	if before.Driver != after.Driver || before.Mountpoint != after.Mountpoint {
		return fmt.Sprintf(
			"it is now a different volume (%s at %s became %s at %s)",
			before.Driver,
			before.Mountpoint,
			after.Driver,
			after.Mountpoint,
		)
	}
	var lost []string
	for _, entry := range before.Entries {
		if !slices.Contains(after.Entries, entry) {
			lost = append(lost, entry)
		}
	}
	if len(lost) > 0 {
		return "it no longer holds " + strings.Join(lost, ", ")
	}
	return ""
}

// checkMountedEdits proves the mounted repository is a working place to develop:
// a macOS edit is visible in Linux, a Linux-created file is visible on macOS,
// and both carry ownership the developer can use from either side.
//
// The marker files are the only thing isolated-dev ever writes into the
// acceptance workload, so they are refused rather than overwritten if the names
// are taken, and they are removed however the check ends.
func (persistence Persistence) checkMountedEdits(
	ctx context.Context,
	request PersistenceRequest,
) (EditRoundTrip, error) {
	hostPath := filepath.Join(request.ProjectPath, request.MarkerName)
	hostCopy := hostPath + guestCopySuffix
	guestPath := path.Join(request.GuestProjectPath, request.MarkerName)
	guestCopy := guestPath + guestCopySuffix
	roundTrip := EditRoundTrip{HostPath: hostPath, GuestPath: guestPath}

	content := markerContent(request.MachineName)
	if err := writeMarker(hostPath, content); err != nil {
		return roundTrip, err
	}
	// Both markers go away even when a step below fails, because a real
	// repository may not be left holding files of ours.
	defer os.Remove(hostPath)
	defer os.Remove(hostCopy)

	output, err := persistence.guestAsUser(ctx, request, "cat", guestPath)
	if err != nil {
		return roundTrip, fmt.Errorf(
			"read the macOS edit at %s from Linux as %s: %w\n%s",
			guestPath,
			request.GuestUser,
			err,
			output,
		)
	}
	if strings.TrimSpace(string(output)) != strings.TrimSpace(content) {
		return roundTrip, fmt.Errorf(
			"Linux read %q from %s, but macOS wrote %q; the mounted repository is not carrying edits into the guest",
			strings.TrimSpace(string(output)),
			guestPath,
			strings.TrimSpace(content),
		)
	}
	roundTrip.HostEditRead = true

	if output, err := persistence.guestAsUser(ctx, request, "cp", guestPath, guestCopy); err != nil {
		return roundTrip, fmt.Errorf(
			"create %s in the guest as %s: %w\n%s",
			guestCopy,
			request.GuestUser,
			err,
			output,
		)
	}
	if err := persistence.readGuestOwnership(ctx, request, guestCopy, &roundTrip); err != nil {
		return roundTrip, err
	}
	if err := readHostCopy(hostCopy, content, &roundTrip); err != nil {
		return roundTrip, err
	}

	roundTrip.OwnershipMatched = roundTrip.GuestUID == request.GuestUID &&
		roundTrip.GuestGID == request.GuestGID &&
		roundTrip.HostUID == os.Getuid()
	if !roundTrip.OwnershipMatched {
		return roundTrip, fmt.Errorf(
			"the file Linux created at %s has ownership %d:%d in the guest and %d:%d on macOS, but the provisioned guest identity is %d:%d and macOS runs as %d:%d; files created on either side would not be usable on the other",
			guestCopy,
			roundTrip.GuestUID,
			roundTrip.GuestGID,
			roundTrip.HostUID,
			roundTrip.HostGID,
			request.GuestUID,
			request.GuestGID,
			os.Getuid(),
			os.Getgid(),
		)
	}
	return roundTrip, nil
}

// writeMarker creates the marker only if nothing is there, so a name that
// collides with real repository content fails before anything is overwritten.
func writeMarker(hostPath string, content string) error {
	file, err := os.OpenFile(hostPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf(
			"create the mounted-edit marker %s: %w; remove it or pass another marker name",
			filepath.Base(hostPath),
			err,
		)
	}
	_, writeErr := file.WriteString(content)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		os.Remove(hostPath)
		return fmt.Errorf("write the mounted-edit marker %s: %w", filepath.Base(hostPath), err)
	}
	return nil
}

// readGuestOwnership reads the ownership Linux sees on the file Linux created.
func (persistence Persistence) readGuestOwnership(
	ctx context.Context,
	request PersistenceRequest,
	guestCopy string,
	roundTrip *EditRoundTrip,
) error {
	output, err := persistence.guestAsUser(ctx, request, "stat", "-c", "%u:%g", guestCopy)
	if err != nil {
		return fmt.Errorf("read the guest ownership of %s: %w\n%s", guestCopy, err, output)
	}
	uid, gid, err := parseOwnership(strings.TrimSpace(string(output)))
	if err != nil {
		return fmt.Errorf("read the guest ownership of %s: %w", guestCopy, err)
	}
	roundTrip.GuestUID = uid
	roundTrip.GuestGID = gid
	return nil
}

// readHostCopy reads the guest-created file back from macOS, which is the other
// half of the round trip: an edit made in Linux has to be a file the developer
// owns.
func readHostCopy(hostCopy string, content string, roundTrip *EditRoundTrip) error {
	data, err := os.ReadFile(hostCopy)
	if err != nil {
		return fmt.Errorf(
			"read the file Linux created back from macOS at %s: %w",
			hostCopy,
			err,
		)
	}
	if strings.TrimSpace(string(data)) != strings.TrimSpace(content) {
		return fmt.Errorf(
			"macOS read %q from the file Linux created at %s, want %q",
			strings.TrimSpace(string(data)),
			hostCopy,
			strings.TrimSpace(content),
		)
	}
	roundTrip.GuestFileRead = true

	info, err := os.Stat(hostCopy)
	if err != nil {
		return fmt.Errorf("read the macOS ownership of %s: %w", hostCopy, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("read the macOS ownership of %s: unsupported file information", hostCopy)
	}
	roundTrip.HostUID = int(stat.Uid)
	roundTrip.HostGID = int(stat.Gid)
	return nil
}

// confirmPortsClosed proves the macOS ports followed the machine down. A tunnel
// left behind would keep a socket bound that nothing answers on, which is worse
// than an honest connection refusal.
func (persistence Persistence) confirmPortsClosed(
	ctx context.Context,
	request PersistenceRequest,
) ([]Endpoint, error) {
	state, err := persistence.Tunnels.Inspect(request.MachineName)
	if err != nil {
		return nil, fmt.Errorf("inspect the managed tunnel of %q: %w", request.MachineName, err)
	}
	if state.Running {
		return nil, fmt.Errorf(
			"the managed tunnel for %q is still running after `stop`; its forwards would keep macOS ports bound to a stopped machine",
			request.MachineName,
		)
	}

	closed := make([]Endpoint, 0, len(request.Endpoints))
	for _, endpoint := range request.Endpoints {
		if err := persistence.awaitClosed(ctx, endpoint); err != nil {
			return closed, err
		}
		closed = append(closed, endpoint)
	}
	return closed, nil
}

// awaitClosed waits for one endpoint to stop answering. A socket the tunnel
// process was still releasing when `stop` returned is given a moment rather
// than reported immediately.
func (persistence Persistence) awaitClosed(ctx context.Context, endpoint Endpoint) error {
	tries := persistence.closureTries()
	for attempt := 0; attempt < tries; attempt++ {
		if _, err := persistence.Prober.Get(ctx, endpoint.URL()); err != nil {
			return nil
		}
		if err := persistence.pause(ctx, attempt, tries); err != nil {
			return err
		}
	}
	return fmt.Errorf(
		"macOS port %d still answers for the Forge %s at %s after `stop`, so the stopped machine's ports are not released",
		endpoint.HostPort,
		endpoint.Label,
		endpoint.URL(),
	)
}

// guestAsUser runs one command inside the machine as the provisioned guest
// account, which is who a developer's own edits and commands run as. Every
// element is a separate argument: no shell interprets any of it.
func (persistence Persistence) guestAsUser(
	ctx context.Context,
	request PersistenceRequest,
	args ...string,
) ([]byte, error) {
	invocation := []string{
		"machine", "run",
		"--name", request.MachineName,
		"--root",
		"--",
		"/usr/sbin/runuser", "-u", request.GuestUser, "--",
		"/usr/bin/env", "-C", request.GuestProjectPath,
		"PATH=" + guestPath,
		"HOME=" + path.Join("/home", request.GuestUser),
	}
	return persistence.Runner.Run(ctx, "container", append(invocation, args...)...)
}

// validatePersistenceRequest checks everything the acceptance run does not:
// the restart itself, the recorded numeric identity, and the marker name that
// becomes a path inside a real repository.
func (persistence Persistence) validatePersistenceRequest(request PersistenceRequest) error {
	if persistence.Lifecycle == nil {
		return errors.New("the project machine lifecycle is not configured")
	}
	if len(request.Volumes) == 0 {
		return errors.New("no named volumes were given to check for persistence")
	}
	if request.GuestUID <= 0 || request.GuestGID <= 0 {
		return fmt.Errorf(
			"machine %q has no recorded numeric guest identity (%d:%d); run `isolated-dev up %s` first",
			request.MachineName,
			request.GuestUID,
			request.GuestGID,
			request.ProjectPath,
		)
	}
	if err := validateMarkerName(request.MarkerName); err != nil {
		return err
	}
	return nil
}

// validateMarkerName keeps the marker a single name directly inside the
// project, so no configuration can point the one file this run writes at
// something outside the repository.
func validateMarkerName(name string) error {
	if name != filepath.Base(name) || name == "." || name == ".." || strings.TrimSpace(name) == "" {
		return fmt.Errorf(
			"the mounted-edit marker name %q must be a single file name directly inside the project",
			name,
		)
	}
	return nil
}

func markerContent(machineName string) string {
	return "isolated-dev persistence check for " + machineName + "\n"
}

func parseOwnership(value string) (int, int, error) {
	uid, gid, found := strings.Cut(value, ":")
	if !found {
		return 0, 0, fmt.Errorf("unexpected ownership %q, want uid:gid", value)
	}
	parsedUID, err := strconv.Atoi(uid)
	if err != nil {
		return 0, 0, fmt.Errorf("unexpected ownership %q, want uid:gid", value)
	}
	parsedGID, err := strconv.Atoi(gid)
	if err != nil {
		return 0, 0, fmt.Errorf("unexpected ownership %q, want uid:gid", value)
	}
	return parsedUID, parsedGID, nil
}

func (persistence Persistence) now() time.Time {
	if persistence.Now != nil {
		return persistence.Now()
	}
	return time.Now()
}

// restartOutput forwards the restart's own progress to wherever the acceptance
// run's output goes, so a slow `up` is visible while it runs.
func (persistence Persistence) restartOutput() io.Writer {
	if persistence.Output != nil {
		return persistence.Output
	}
	return io.Discard
}

func (persistence Persistence) closureTries() int {
	if persistence.ClosureTries > 0 {
		return persistence.ClosureTries
	}
	return 15
}
