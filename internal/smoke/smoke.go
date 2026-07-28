// Package smoke automates the baseline nested-Compose test.
//
// The baseline is the smallest workload that proves Docker still runs inside an
// Apple Container Machine: two pinned images on a private Compose network,
// serving a marker file that lives on the macOS side of the mount. It is a
// regression check on the platform rather than on any project, so it creates
// every resource it touches — base image, machine, and fixtures — and removes
// exactly those again, whether it passes or fails.
package smoke

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fxmartin/isolated-dev/internal/baseimage"
	"github.com/fxmartin/isolated-dev/internal/machine"
)

var machineNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// guestUserPattern is the Linux login-name shape `guest.NewIdentity` derives.
// It is re-checked here because it also composes the guest home directory the
// fixture may be mounted under.
var guestUserPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)

// markerPattern keeps the marker a single safe token: it travels through a
// Compose bind mount, an HTTP body, and a guest command line.
var markerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// guestPath is the search path guest commands run with. `container machine run`
// does not guarantee a PATH, so it is set explicitly rather than inherited.
const guestPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// composeWaitSeconds bounds `compose up --wait`. Pulling two small images and
// passing their health checks is well inside this, and a bound is what turns a
// wedged daemon into a reported failure instead of a hung run.
const composeWaitSeconds = 240

// teardownTimeout bounds cleanup, which runs on a context detached from the
// caller's: a run that failed by timing out still has to remove what it made.
const teardownTimeout = 5 * time.Minute

type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// MachineLifecycle is the machine.Manager surface the baseline drives. Going
// through it is what exercises the documented boot retries and the Docker
// readiness fallback rather than a second, divergent startup path.
type MachineLifecycle interface {
	Up(context.Context, machine.Request) (machine.UpResult, error)
	Destroy(context.Context, machine.Target) error
}

type ImageEnsurer interface {
	EnsureReference(context.Context, string) error
}

// DockerWaiter reports when the guest Docker daemon answers `docker info`, and
// starts `dockerd` directly when Apple Container 1.1.0 leaves systemd down.
type DockerWaiter interface {
	WaitDocker(ctx context.Context, machineName string) error
}

type AddressResolver interface {
	Address(context.Context, machine.Target) (string, error)
}

// Prober performs the macOS-side HTTP request. It is the only step that leaves
// the host, and it is what proves the published guest port is reachable from
// macOS rather than only from inside the machine.
type Prober interface {
	Get(ctx context.Context, url string) (string, error)
}

type Test struct {
	Runner       Runner
	Machines     MachineLifecycle
	ImageEnsurer ImageEnsurer
	DockerWaiter DockerWaiter
	Address      AddressResolver
	Prober       Prober
	ProbeTries   int
	RetryDelay   time.Duration
	Sleep        func(time.Duration)
	// Diagnostics receives the read-only guest state captured when a step
	// fails. Apple Container Machine 1.1.0 can fail its first remote command
	// and leave systemd unavailable, and neither is visible from the failing
	// step alone.
	Diagnostics io.Writer
}

// Request describes one baseline run. Every field names a resource the run
// owns: the machine it creates, the base-image version it builds, and the
// fixture directory it writes.
type Request struct {
	MachineName string
	// BaseImageVersion must differ from the shared default: teardown deletes
	// the image this run builds.
	BaseImageVersion string
	// HomeDir is the macOS home directory the machine mounts, and FixtureDir
	// must be inside it.
	HomeDir    string
	FixtureDir string
	// GuestUser is the login whose guest home may expose the macOS home mount.
	// It is optional: without it only the host path is probed.
	GuestUser string
	CPUs      int
	MemoryGB  int
	HostPort  int
	// Marker is the unique token the run expects to travel from the macOS
	// fixture back through both probes.
	Marker string
}

// Result reports what the baseline observed. It is filled in as the run
// progresses, so a failed run still describes how far it got.
type Result struct {
	BaseImage string
	// GuestDir is the Linux path at which the macOS fixture directory appeared.
	GuestDir      string
	NetworkName   string
	NetworkDriver string
	// AttachedContainers is how many containers joined the private network.
	AttachedContainers int
	// GuestMarker and HostMarker are the marker as it came back from inside the
	// guest and from macOS through the published port.
	GuestMarker string
	HostMarker  string
	HostURL     string
}

// Run performs the baseline end to end and removes everything it created.
//
// A failure is returned with the teardown result joined onto it: cleanup runs
// on every path, and a leaked machine or image has to be visible even when the
// baseline itself is what failed.
func (test Test) Run(ctx context.Context, request Request) (Result, error) {
	reference, err := test.validate(request)
	if err != nil {
		return Result{}, err
	}
	fixture, err := WriteFixture(
		request.FixtureDir,
		composeProjectName(request.MachineName),
		request.Marker,
		request.HostPort,
	)
	if err != nil {
		return Result{}, err
	}

	result, state, runErr := test.perform(ctx, request, fixture, reference)
	teardownErr := test.teardown(ctx, request, fixture, reference, state)
	if runErr != nil {
		return result, errors.Join(runErr, teardownErr)
	}
	if teardownErr != nil {
		return result, teardownErr
	}
	return result, nil
}

// progress records which resources exist, so teardown removes what was created
// and stays quiet about what never was.
type progress struct {
	imageBuilt bool
	guestDir   string
	composeUp  bool
}

func (test Test) perform(
	ctx context.Context,
	request Request,
	fixture Fixture,
	reference string,
) (Result, progress, error) {
	result := Result{BaseImage: reference, NetworkName: fixture.NetworkName()}
	var state progress

	if err := test.ImageEnsurer.EnsureReference(ctx, reference); err != nil {
		return result, state, fmt.Errorf("build the baseline base image %s: %w", reference, err)
	}
	state.imageBuilt = true

	if _, err := test.Machines.Up(ctx, machine.Request{
		ProjectPath:      request.FixtureDir,
		MachineName:      request.MachineName,
		BaseImage:        reference,
		BaseImageVersion: request.BaseImageVersion,
		CPUs:             request.CPUs,
		MemoryGB:         request.MemoryGB,
		MountScope:       "home",
	}); err != nil {
		return result, state, test.diagnose(ctx, request, fixture, state, fmt.Errorf(
			"create the baseline machine %q: %w",
			request.MachineName,
			err,
		))
	}

	// `machine.Up` already waited for Docker and fell back to starting dockerd
	// directly. Compose is a separate `machine run`, and the Apple Container
	// 1.1.0 startup race can still leave the daemon down between the two, so
	// readiness is confirmed again here exactly as a declared Compose command
	// does.
	if err := test.DockerWaiter.WaitDocker(ctx, request.MachineName); err != nil {
		return result, state, test.diagnose(ctx, request, fixture, state, fmt.Errorf(
			"Docker is not ready in the baseline machine %q: %w",
			request.MachineName,
			err,
		))
	}

	guestDir, err := test.locate(ctx, request)
	if err != nil {
		return result, state, test.diagnose(ctx, request, fixture, state, err)
	}
	state.guestDir = guestDir
	result.GuestDir = guestDir

	// Recorded before the call, not after it: a Compose start that fails
	// halfway has still created containers and a network for teardown to
	// remove.
	state.composeUp = true
	if output, err := test.guest(ctx, request.MachineName, composeArgs(
		guestDir,
		fixture.ProjectName,
		"up", "--detach", "--wait", "--wait-timeout", strconv.Itoa(composeWaitSeconds),
	)...); err != nil {
		return result, state, test.diagnose(ctx, request, fixture, state, fmt.Errorf(
			"start the baseline Compose workload: %w\n%s",
			err,
			output,
		))
	}

	if err := test.inspectNetwork(ctx, request, fixture, &result); err != nil {
		return result, state, test.diagnose(ctx, request, fixture, state, err)
	}
	if err := test.inspectServices(ctx, request, fixture); err != nil {
		return result, state, test.diagnose(ctx, request, fixture, state, err)
	}

	guestMarker, err := test.probeGuest(ctx, request)
	if err != nil {
		return result, state, test.diagnose(ctx, request, fixture, state, err)
	}
	result.GuestMarker = guestMarker

	address, err := test.Address.Address(ctx, machine.Target{
		ProjectPath: request.FixtureDir,
		MachineName: request.MachineName,
	})
	if err != nil {
		return result, state, test.diagnose(ctx, request, fixture, state, err)
	}
	result.HostURL = "http://" + authority(address, request.HostPort) + "/" + MarkerFileName

	hostMarker, err := test.probeHost(ctx, request, result.HostURL)
	if err != nil {
		return result, state, test.diagnose(ctx, request, fixture, state, err)
	}
	result.HostMarker = hostMarker

	return result, state, nil
}

// locate resolves the Linux path of the mounted fixture directory. Apple
// Container Machine 1.1.0 exposes the macOS home without documenting the guest
// path, so both the host path and the guest home are probed, as guest
// provisioning does for the project itself.
func (test Test) locate(ctx context.Context, request Request) (string, error) {
	candidates := guestCandidates(request)
	for _, candidate := range candidates {
		if _, err := test.guest(
			ctx,
			request.MachineName,
			"test", "-f", path.Join(candidate, MarkerFileName),
		); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf(
		"could not find the baseline fixtures inside machine %q at %s; the macOS home mount is missing or incomplete",
		request.MachineName,
		strings.Join(candidates, " or "),
	)
}

// guestCandidates lists where the fixture directory can appear inside the
// guest. Validation has already established that the fixture lives under the
// mounted home, so the guest-home candidate is that same relative path.
func guestCandidates(request Request) []string {
	fixtureDir := filepath.Clean(request.FixtureDir)
	candidates := []string{fixtureDir}
	if request.GuestUser == "" {
		return candidates
	}
	relative := strings.TrimPrefix(
		fixtureDir,
		filepath.Clean(request.HomeDir)+string(filepath.Separator),
	)
	return append(candidates, path.Join("/home", request.GuestUser, filepath.ToSlash(relative)))
}

// inspectNetwork confirms the two services share a Compose-created bridge
// network rather than the guest's own networking.
func (test Test) inspectNetwork(
	ctx context.Context,
	request Request,
	fixture Fixture,
	result *Result,
) error {
	driver, err := test.guestValue(
		ctx, request.MachineName,
		"docker", "network", "inspect", fixture.NetworkName(), "--format", "{{.Driver}}",
	)
	if err != nil {
		return fmt.Errorf("inspect the private network %s: %w", fixture.NetworkName(), err)
	}
	result.NetworkDriver = driver
	if driver != "bridge" {
		return fmt.Errorf(
			"the baseline network %s uses the %q driver, want %q: the two services must share a private Compose network",
			fixture.NetworkName(),
			driver,
			"bridge",
		)
	}

	attached, err := test.guestValue(
		ctx, request.MachineName,
		"docker", "network", "inspect", fixture.NetworkName(), "--format", "{{len .Containers}}",
	)
	if err != nil {
		return fmt.Errorf("count the containers on %s: %w", fixture.NetworkName(), err)
	}
	count, err := strconv.Atoi(attached)
	if err != nil {
		return fmt.Errorf("decode the container count of %s: %w", fixture.NetworkName(), err)
	}
	result.AttachedContainers = count
	if count != 2 {
		return fmt.Errorf(
			"the baseline network %s carries %d containers, want 2",
			fixture.NetworkName(),
			count,
		)
	}
	return nil
}

// inspectServices confirms that the containers Compose started are the pinned
// images, running. The Compose file declares the pins; this is what proves they
// are what actually came up.
func (test Test) inspectServices(ctx context.Context, request Request, fixture Fixture) error {
	services := []struct {
		name  string
		image string
	}{
		{name: OriginService, image: OriginImage},
		{name: ProxyService, image: ProxyImage},
	}
	args := []string{"docker", "inspect", "--format", "{{.Config.Image}} {{.State.Running}}"}
	for _, service := range services {
		args = append(args, fixture.ContainerName(service.name))
	}
	output, err := test.guest(ctx, request.MachineName, args...)
	if err != nil {
		return fmt.Errorf("inspect the baseline containers: %w\n%s", err, output)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != len(services) {
		return fmt.Errorf(
			"the baseline started %d containers, want %d:\n%s",
			len(lines),
			len(services),
			output,
		)
	}
	for index, service := range services {
		fields := strings.Fields(lines[index])
		if len(fields) != 2 {
			return fmt.Errorf("could not read the state of the %s container: %q", service.name, lines[index])
		}
		if fields[0] != service.image {
			return fmt.Errorf(
				"the %s service runs %s, want the pinned %s",
				service.name,
				fields[0],
				service.image,
			)
		}
		if fields[1] != "true" {
			return fmt.Errorf("the %s service is not running", service.name)
		}
	}
	return nil
}

// probeGuest reads the marker through Nginx from inside the machine, over the
// published port on guest loopback.
func (test Test) probeGuest(ctx context.Context, request Request) (string, error) {
	url := "http://" + authority("127.0.0.1", request.HostPort) + "/" + MarkerFileName
	var lastErr error
	for attempt := 0; attempt < test.probeTries(); attempt++ {
		output, err := test.guest(
			ctx, request.MachineName,
			"curl", "--fail", "--silent", "--show-error", "--max-time", "5", url,
		)
		if err == nil {
			return verifyMarker("inside the guest", url, string(output), request.Marker)
		}
		lastErr = fmt.Errorf("%w\n%s", err, output)
		if err := test.pause(ctx, attempt); err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("read the marker at %s inside the guest: %w", url, lastErr)
}

// probeHost reads the same marker from macOS through the published guest port,
// which is the half of the baseline that the guest itself cannot prove.
func (test Test) probeHost(ctx context.Context, request Request, url string) (string, error) {
	var lastErr error
	for attempt := 0; attempt < test.probeTries(); attempt++ {
		body, err := test.Prober.Get(ctx, url)
		if err == nil {
			return verifyMarker("from macOS", url, body, request.Marker)
		}
		lastErr = err
		if err := test.pause(ctx, attempt); err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("read the marker at %s from macOS: %w", url, lastErr)
}

func verifyMarker(where string, url string, body string, marker string) (string, error) {
	got := strings.TrimSpace(body)
	if got != marker {
		return "", fmt.Errorf(
			"%s returned %q %s, want the marker %q from the macOS-mounted fixture",
			url,
			got,
			where,
			marker,
		)
	}
	return got, nil
}

// teardown removes the containers, the private network, the machine, the base
// image, and the fixtures — and only those. Every step reports its own failure
// so one leak cannot hide another.
func (test Test) teardown(
	ctx context.Context,
	request Request,
	fixture Fixture,
	reference string,
	state progress,
) error {
	teardownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), teardownTimeout)
	defer cancel()

	var failures []error
	if state.composeUp {
		// The machine is deleted next and would take these with it, but a
		// resource the baseline created is removed explicitly rather than left
		// to a side effect.
		if output, err := test.guest(teardownCtx, request.MachineName, composeArgs(
			state.guestDir,
			fixture.ProjectName,
			"down", "--remove-orphans", "--volumes", "--timeout", "10",
		)...); err != nil {
			failures = append(failures, fmt.Errorf(
				"remove the baseline containers and network: %w\n%s",
				err,
				output,
			))
		}
	}
	if err := test.Machines.Destroy(teardownCtx, machine.Target{
		ProjectPath: request.FixtureDir,
		MachineName: request.MachineName,
	}); err != nil {
		failures = append(failures, fmt.Errorf(
			"delete the baseline machine %q: %w",
			request.MachineName,
			err,
		))
	}
	if state.imageBuilt {
		if output, err := test.Runner.Run(
			teardownCtx,
			"container",
			"image", "delete", reference,
		); err != nil {
			failures = append(failures, fmt.Errorf(
				"delete the baseline base image %s: %w\n%s",
				reference,
				err,
				output,
			))
		}
	}
	if err := fixture.Remove(); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

// diagnostic is one read-only command whose output explains a failure that the
// failing step alone does not.
type diagnostic struct {
	label string
	args  []string
}

// diagnose captures guest state and returns the original failure unchanged.
// The known Apple Container 1.1.0 startup race shows up as a terminated systemd
// and an absent Docker daemon, neither of which appears in the error of the
// step that tripped over it.
func (test Test) diagnose(
	ctx context.Context,
	request Request,
	fixture Fixture,
	state progress,
	cause error,
) error {
	if test.Diagnostics == nil {
		return cause
	}
	diagnosticCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), teardownTimeout)
	defer cancel()

	diagnostics := []diagnostic{
		{label: "container machine list", args: []string{"machine", "list", "--format", "json"}},
		{label: "systemctl is-system-running", args: guestArgs(
			request.MachineName,
			"systemctl", "is-system-running",
		)},
		{label: "docker info", args: guestArgs(request.MachineName, "docker", "info")},
	}
	if state.composeUp {
		diagnostics = append(
			diagnostics,
			diagnostic{label: "compose ps", args: guestArgs(request.MachineName, composeArgs(
				state.guestDir, fixture.ProjectName, "ps", "--all",
			)...)},
			diagnostic{label: "compose logs", args: guestArgs(request.MachineName, composeArgs(
				state.guestDir, fixture.ProjectName, "logs", "--no-color", "--tail", "100",
			)...)},
		)
	}

	fmt.Fprintf(
		test.Diagnostics,
		"baseline nested-Compose diagnostics for machine %q\nfailure: %v\n",
		request.MachineName,
		cause,
	)
	for _, entry := range diagnostics {
		output, err := test.Runner.Run(diagnosticCtx, "container", entry.args...)
		fmt.Fprintf(
			test.Diagnostics,
			"--- %s ---\n%s\n",
			entry.label,
			strings.TrimRight(string(output), "\n"),
		)
		if err != nil {
			fmt.Fprintf(test.Diagnostics, "(command failed: %v)\n", err)
		}
	}
	return cause
}

// guest runs one command inside the machine as root and returns its combined
// output. Every element is a separate argument: no shell interprets any of it.
func (test Test) guest(
	ctx context.Context,
	machineName string,
	args ...string,
) ([]byte, error) {
	return test.Runner.Run(ctx, "container", guestArgs(machineName, args...)...)
}

// guestValue runs a guest command whose single-line output is the answer.
func (test Test) guestValue(
	ctx context.Context,
	machineName string,
	args ...string,
) (string, error) {
	output, err := test.guest(ctx, machineName, args...)
	if err != nil {
		return "", fmt.Errorf("%w\n%s", err, output)
	}
	return strings.TrimSpace(string(output)), nil
}

func guestArgs(machineName string, args ...string) []string {
	invocation := []string{
		"machine", "run",
		"--name", machineName,
		"--root",
		"--",
		"/usr/bin/env", "PATH=" + guestPath,
	}
	return append(invocation, args...)
}

// composeArgs addresses the fixture explicitly. The project name, directory,
// and file are all named so Compose never discovers anything of its own.
func composeArgs(guestDir string, projectName string, args ...string) []string {
	invocation := []string{
		"docker", "compose",
		"--project-name", projectName,
		"--project-directory", guestDir,
		"--file", path.Join(guestDir, ComposeFileName),
	}
	return append(invocation, args...)
}

// composeProjectName derives a Compose-acceptable project name from the machine
// name, which may carry upper case and dots that Compose rejects.
func composeProjectName(machineName string) string {
	var name strings.Builder
	for _, letter := range strings.ToLower(machineName) {
		switch {
		case letter >= 'a' && letter <= 'z', letter >= '0' && letter <= '9', letter == '-', letter == '_':
			name.WriteRune(letter)
		default:
			name.WriteRune('-')
		}
	}
	return strings.TrimLeft(name.String(), "-_")
}

func (test Test) probeTries() int {
	if test.ProbeTries > 0 {
		return test.ProbeTries
	}
	return 15
}

// pause waits between probe attempts and stops early when the caller gives up.
func (test Test) pause(ctx context.Context, attempt int) error {
	if attempt+1 >= test.probeTries() {
		return nil
	}
	delay := test.RetryDelay
	if delay <= 0 {
		delay = 2 * time.Second
	}
	sleep := test.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	sleep(delay)
	// A caller that gave up during the wait should not be made to sit through
	// the rest of the retry budget.
	return ctx.Err()
}

func (test Test) validate(request Request) (string, error) {
	missing := []struct {
		name     string
		supplied bool
	}{
		{name: "host command runner", supplied: test.Runner != nil},
		{name: "machine lifecycle", supplied: test.Machines != nil},
		{name: "base-image builder", supplied: test.ImageEnsurer != nil},
		{name: "Docker readiness waiter", supplied: test.DockerWaiter != nil},
		{name: "machine address resolver", supplied: test.Address != nil},
		{name: "macOS HTTP prober", supplied: test.Prober != nil},
	}
	for _, dependency := range missing {
		if !dependency.supplied {
			return "", fmt.Errorf("baseline %s is not configured", dependency.name)
		}
	}

	if !machineNamePattern.MatchString(request.MachineName) {
		return "", fmt.Errorf("invalid machine name %q", request.MachineName)
	}
	if request.BaseImageVersion == baseimage.DefaultVersion {
		return "", fmt.Errorf(
			"baseline base-image version must not be the shared %q: teardown deletes the image the baseline builds, and deleting the shared base image would destroy a resource other projects depend on",
			baseimage.DefaultVersion,
		)
	}
	reference, err := baseimage.Reference(request.BaseImageVersion)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(request.HomeDir) {
		return "", errors.New("home directory must be absolute")
	}
	if !filepath.IsAbs(request.FixtureDir) {
		return "", errors.New("baseline fixture directory must be absolute")
	}
	homeDir := filepath.Clean(request.HomeDir)
	fixtureDir := filepath.Clean(request.FixtureDir)
	if !within(homeDir, fixtureDir) {
		return "", fmt.Errorf(
			"baseline fixtures %q are outside the mounted home directory %q; Apple Container Machine 1.1.0 cannot expose them to the machine",
			request.FixtureDir,
			request.HomeDir,
		)
	}
	if fixtureDir == homeDir {
		return "", fmt.Errorf(
			"baseline fixtures must live in their own directory under %q, which teardown removes",
			request.HomeDir,
		)
	}
	if request.GuestUser != "" && !guestUserPattern.MatchString(request.GuestUser) {
		return "", fmt.Errorf("invalid guest user name %q", request.GuestUser)
	}
	if !markerPattern.MatchString(request.Marker) {
		return "", fmt.Errorf(
			"invalid baseline marker %q: it must be a single token of letters, digits, dots, hyphens, and underscores",
			request.Marker,
		)
	}
	if request.CPUs <= 0 {
		return "", errors.New("CPUs must be positive")
	}
	if request.MemoryGB <= 0 {
		return "", errors.New("memory must be positive")
	}
	if request.HostPort < 1 || request.HostPort > 65535 {
		return "", fmt.Errorf("baseline host port %d is outside 1-65535", request.HostPort)
	}
	return reference, nil
}

// within reports whether candidate is dir or one of its descendants. Both
// paths are absolute by the time this runs, and Clean resolves any `..` before
// the comparison, so a prefix check is exact.
func within(dir string, candidate string) bool {
	if dir == candidate {
		return true
	}
	return strings.HasPrefix(candidate, dir+string(filepath.Separator))
}

func authority(host string, port int) string {
	return host + ":" + strconv.Itoa(port)
}
