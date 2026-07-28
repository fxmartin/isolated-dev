// Package forge runs the unmodified Forge DEV Compose stack as the upper-bound
// acceptance workload.
//
// The baseline nested-Compose test in `internal/smoke` proves the platform on
// resources it creates and destroys. This one proves a real project: the Forge
// repository, its own `docker-compose.yml`, its four DEV services, and the two
// macOS ports a developer actually opens. It therefore owns nothing and removes
// nothing — no machine, no container, no image, and above all no named volume
// holding real data. Everything it does is either a read or the one explicitly
// declared DEV command the project itself defines.
package forge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/fxmartin/isolated-dev/internal/config"
	"github.com/fxmartin/isolated-dev/internal/projectcmd"
	"github.com/fxmartin/isolated-dev/internal/tunnel"
)

var machineNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// guestPath is the search path guest commands run with. `container machine run`
// does not guarantee a PATH, so it is set explicitly rather than inherited.
const guestPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// maxCapturedOutput bounds what a startup keeps in memory for classification.
// A Forge build prints a lot; the failure that explains it is in the first
// megabyte of it.
const maxCapturedOutput = 1 << 20

// maxRecordedBody bounds what a probed response contributes to the result.
const maxRecordedBody = 200

// diagnosticTimeout bounds the read-only capture that explains a failure. It
// runs on a context detached from the caller's, because a run that failed by
// timing out is exactly the one whose guest state has to be visible.
const diagnosticTimeout = 2 * time.Minute

// Runner executes a host process and returns its combined output. It carries
// the read-only guest inspections; the DEV command itself goes through the
// declared-command path instead.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// CommandExecutor runs one explicitly declared project command. Going through
// it is what makes this an acceptance test of `isolated-dev run` rather than of
// a second, divergent way to start Compose.
type CommandExecutor interface {
	Execute(context.Context, projectcmd.Request, projectcmd.Streams) (int, error)
}

// TunnelInspector reports the managed background port forwards of the project
// machine without changing them.
type TunnelInspector interface {
	Inspect(machineName string) (tunnel.State, error)
}

// Prober performs the macOS-side HTTP request. It is the only step that leaves
// the host, and it is what proves the stack is reachable from macOS rather than
// only from inside the machine.
type Prober interface {
	Get(ctx context.Context, url string) (string, error)
}

type Acceptance struct {
	Commands CommandExecutor
	Runner   Runner
	Tunnels  TunnelInspector
	Prober   Prober
	// HealthTries and ProbeTries bound the waits for the stack to become healthy
	// and for macOS to reach it.
	HealthTries int
	ProbeTries  int
	RetryDelay  time.Duration
	Sleep       func(time.Duration)
	// Output receives the DEV command's own output as it is produced, so a long
	// first build is visible while it runs.
	Output io.Writer
	// Diagnostics receives the read-only guest state captured when a step fails.
	Diagnostics io.Writer
}

// Request describes one acceptance run against an existing project machine.
// Every field describes something that already exists: the run creates no
// machine, no configuration, and no fixture of its own.
type Request struct {
	// ProjectPath is the canonical macOS path of the Forge repository, and
	// GuestProjectPath the Linux path `up` recorded for it.
	ProjectPath      string
	MachineName      string
	GuestUser        string
	GuestProjectPath string
	// CommandName is the declared DEV command to invoke.
	CommandName string
	// Config is the project's effective configuration, which is what declares
	// both that command and the forwarded ports.
	Config config.Config
	// Services and Endpoints default to the DEV profile the Forge repository
	// declares.
	Services  []Service
	Endpoints []Endpoint
}

func (request Request) withDefaults() Request {
	if request.Services == nil {
		request.Services = DevServices()
	}
	if request.Endpoints == nil {
		request.Endpoints = DevEndpoints()
	}
	return request
}

// Result reports what the acceptance run observed. It is filled in as the run
// progresses, so a failed run still describes how far it got.
type Result struct {
	ComposeFile string
	// ComposeDigest is the SHA-256 of the project's Compose file, identical
	// before and after the run.
	ComposeDigest string
	// Command is the declared argument vector that started the stack.
	Command  []string
	ExitCode int
	Services []ServiceState
	// Endpoints are the macOS addresses proven through the managed tunnel.
	Endpoints []EndpointState
	// Architecture is set when a failure was an architecture incompatibility.
	Architecture *ArchitectureIssue
}

type ServiceState struct {
	Service
	Image   string
	Running bool
	// Health is the container health status, or "none" for a service that
	// declares no health check.
	Health string
}

type EndpointState struct {
	Endpoint
	// Forward is the managed mapping the endpoint was reached through.
	Forward tunnel.Forward
	Body    string
}

// Run starts the project's declared DEV command and verifies the stack it
// brings up, from the four guest services out to the two macOS ports.
//
// It removes nothing on any path. A failure returns the partially filled result
// alongside the error, so what did work is still reported.
func (acceptance Acceptance) Run(ctx context.Context, request Request) (Result, error) {
	request = request.withDefaults()
	declared, err := acceptance.prepare(request)
	if err != nil {
		return Result{}, err
	}
	digest, err := ComposeDigest(request.ProjectPath)
	if err != nil {
		return Result{}, err
	}

	result := Result{
		ComposeFile:   filepath.Join(request.ProjectPath, ComposeFileName),
		ComposeDigest: digest,
		Command:       declared.Args,
	}
	if err := acceptance.perform(ctx, request, declared, &result); err != nil {
		return result, acceptance.diagnose(ctx, request, err)
	}
	return result, nil
}

// prepare validates everything that can be known before the stack starts: the
// dependencies, the machine and guest identity, the declared DEV command, and
// the declared macOS ports. A project that cannot pass this would start real
// services and only then be told the run could never have proven anything.
func (acceptance Acceptance) prepare(request Request) (config.Command, error) {
	if err := acceptance.validate(request); err != nil {
		return config.Command{}, err
	}
	declared, ok := request.Config.Commands[request.CommandName]
	if !ok {
		return config.Command{}, fmt.Errorf(
			"the DEV command %q is not declared by the project; declared commands: %s",
			request.CommandName,
			describeNames(request.Config.CommandNames()),
		)
	}
	if err := VerifyDevCommand(request.CommandName, declared); err != nil {
		return config.Command{}, err
	}
	for _, endpoint := range request.Endpoints {
		if _, err := declaredPort(request.Config, endpoint); err != nil {
			return config.Command{}, err
		}
	}
	return declared, nil
}

func (acceptance Acceptance) perform(
	ctx context.Context,
	request Request,
	declared config.Command,
	result *Result,
) error {
	if err := acceptance.start(ctx, request, declared, result); err != nil {
		return err
	}
	if err := acceptance.confirmComposeUnchanged(request, result.ComposeDigest); err != nil {
		return err
	}
	if err := acceptance.awaitServices(ctx, request, result); err != nil {
		return err
	}
	return acceptance.reachEndpoints(ctx, request, result)
}

// reachEndpoints proves the declared macOS ports answer through the managed
// tunnel. It is the step that leaves the host, so it is also what a persistence
// run repeats to show the ports followed the machine's lifecycle.
func (acceptance Acceptance) reachEndpoints(
	ctx context.Context,
	request Request,
	result *Result,
) error {
	forwards, err := acceptance.verifyTunnel(request)
	if err != nil {
		return err
	}
	return acceptance.probeEndpoints(ctx, request, forwards, result)
}

// start runs the declared DEV command and keeps its output, which is the only
// place an architecture incompatibility in a pull or a build ever appears.
func (acceptance Acceptance) start(
	ctx context.Context,
	request Request,
	declared config.Command,
	result *Result,
) error {
	captured := &boundedBuffer{limit: maxCapturedOutput}
	// Standard output and standard error share one writer value, which is what
	// keeps the two streams from being written concurrently.
	var stream io.Writer = captured
	if acceptance.Output != nil {
		stream = io.MultiWriter(captured, acceptance.Output)
	}

	exitCode, err := acceptance.Commands.Execute(ctx, projectcmd.Request{
		MachineName:      request.MachineName,
		GuestUser:        request.GuestUser,
		GuestProjectPath: request.GuestProjectPath,
		Name:             request.CommandName,
		Command:          declared,
	}, projectcmd.Streams{Stdout: stream, Stderr: stream})
	result.ExitCode = exitCode
	if err != nil {
		return withArchitecture(result, captured.String(), fmt.Errorf(
			"run the Forge DEV command %q in machine %q: %w",
			request.CommandName,
			request.MachineName,
			err,
		))
	}
	if exitCode != 0 {
		return withArchitecture(result, captured.String(), fmt.Errorf(
			"the Forge DEV command %q exited %d in machine %q",
			request.CommandName,
			exitCode,
			request.MachineName,
		))
	}
	return nil
}

// confirmComposeUnchanged proves the run used the repository's existing Compose
// file as it is. Nothing here writes it, which is exactly the claim the digest
// makes checkable rather than assumed.
func (acceptance Acceptance) confirmComposeUnchanged(request Request, before string) error {
	after, err := ComposeDigest(request.ProjectPath)
	if err != nil {
		return err
	}
	if after != before {
		return fmt.Errorf(
			"the project %s changed while the DEV stack started (%s became %s); the acceptance run must start the repository's existing Compose file unmodified",
			ComposeFileName,
			before,
			after,
		)
	}
	return nil
}

// awaitServices waits for the four DEV services to be running and healthy.
// `compose up -d` returns once the containers are created, and the FastAPI
// backend alone declares a 60-second health start period, so a single check
// would report a healthy stack as broken.
func (acceptance Acceptance) awaitServices(
	ctx context.Context,
	request Request,
	result *Result,
) error {
	tries := acceptance.healthTries()
	var pending error
	for attempt := 0; attempt < tries; attempt++ {
		states, err := acceptance.inspectServices(ctx, request)
		if err == nil {
			result.Services = states
			// A service running the wrong image never becomes the right one, so
			// waiting out the budget would only delay the report.
			if err := verifyImages(states); err != nil {
				return err
			}
			if pending = verifyHealth(states); pending == nil {
				return nil
			}
		} else {
			pending = err
		}
		if err := acceptance.pause(ctx, attempt, tries); err != nil {
			return err
		}
	}
	return acceptance.explainServices(ctx, request, result, pending)
}

// inspectServices reads the state of the four containers the Compose file pins
// by name. A container that does not exist yet fails the whole call, which the
// wait treats as "not started yet" and the final report quotes verbatim.
func (acceptance Acceptance) inspectServices(
	ctx context.Context,
	request Request,
) ([]ServiceState, error) {
	args := []string{
		"docker", "inspect", "--format",
		"{{.Config.Image}} {{.State.Running}} {{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}",
	}
	for _, service := range request.Services {
		args = append(args, service.Container)
	}
	output, err := acceptance.guest(ctx, request.MachineName, args...)
	if err != nil {
		return nil, fmt.Errorf("inspect the Forge DEV containers: %w\n%s", err, output)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != len(request.Services) {
		return nil, fmt.Errorf(
			"the Forge DEV profile reported %d containers, want %d:\n%s",
			len(lines),
			len(request.Services),
			output,
		)
	}
	states := make([]ServiceState, 0, len(request.Services))
	for index, service := range request.Services {
		fields := strings.Fields(lines[index])
		if len(fields) != 3 {
			return nil, fmt.Errorf(
				"could not read the state of %s (%s): %q",
				service.Container,
				service.Description,
				lines[index],
			)
		}
		states = append(states, ServiceState{
			Service: service,
			Image:   fields[0],
			Running: fields[1] == "true",
			Health:  fields[2],
		})
	}
	return states, nil
}

func verifyImages(states []ServiceState) error {
	for _, state := range states {
		if state.ImagePrefix == "" || strings.HasPrefix(state.Image, state.ImagePrefix) {
			continue
		}
		return fmt.Errorf(
			"%s (%s) runs %s, want an image built from %s",
			state.Container,
			state.Description,
			state.Image,
			state.ImagePrefix,
		)
	}
	return nil
}

func verifyHealth(states []ServiceState) error {
	for _, state := range states {
		if !state.Running {
			return fmt.Errorf("%s (%s) is not running", state.Container, state.Description)
		}
		// "none" is what the format reports for a service that declares no health
		// check, which the frontend does not.
		if state.Health != "healthy" && state.Health != "none" {
			return fmt.Errorf(
				"%s (%s) is running but its health is %q",
				state.Container,
				state.Description,
				state.Health,
			)
		}
	}
	return nil
}

// explainServices turns a stack that never came up into a report that names a
// cause. A wrong-architecture image usually pulls and builds cleanly and only
// fails when its entrypoint executes, so the container logs are where that
// failure is, not the startup output.
func (acceptance Acceptance) explainServices(
	ctx context.Context,
	request Request,
	result *Result,
	cause error,
) error {
	failure := fmt.Errorf("the Forge DEV stack did not become healthy: %w", cause)
	return withArchitecture(result, acceptance.serviceLogs(ctx, request, result.Services), failure)
}

// serviceLogs collects the logs of the services that are not ready, or of all of
// them when their state could not be read at all.
func (acceptance Acceptance) serviceLogs(
	ctx context.Context,
	request Request,
	states []ServiceState,
) string {
	containers := make([]string, 0, len(request.Services))
	for _, state := range states {
		if !state.Running || (state.Health != "healthy" && state.Health != "none") {
			containers = append(containers, state.Container)
		}
	}
	if len(containers) == 0 {
		for _, service := range request.Services {
			containers = append(containers, service.Container)
		}
	}

	var collected strings.Builder
	for _, container := range containers {
		output, _ := acceptance.guest(
			ctx, request.MachineName,
			"docker", "logs", "--tail", "100", container,
		)
		collected.WriteString(container + "\n")
		collected.Write(output)
		collected.WriteString("\n")
	}
	return collected.String()
}

// verifyTunnel confirms the managed tunnel carries the declared ports before
// macOS is probed through it, so an unreachable endpoint is reported as the
// missing forward it is rather than as a broken stack.
func (acceptance Acceptance) verifyTunnel(request Request) ([]tunnel.Forward, error) {
	state, err := acceptance.Tunnels.Inspect(request.MachineName)
	if err != nil {
		return nil, fmt.Errorf("inspect the managed tunnel of %q: %w", request.MachineName, err)
	}
	if !state.Running {
		return nil, fmt.Errorf(
			"the managed tunnel for %q is not running, so the Forge DEV stack is not reachable from macOS; run `isolated-dev up %s`",
			request.MachineName,
			request.ProjectPath,
		)
	}

	forwards := make([]tunnel.Forward, 0, len(request.Endpoints))
	for _, endpoint := range request.Endpoints {
		forward, err := matchForward(state, endpoint)
		if err != nil {
			return nil, err
		}
		forwards = append(forwards, forward)
	}
	return forwards, nil
}

func matchForward(state tunnel.State, endpoint Endpoint) (tunnel.Forward, error) {
	for _, blocked := range state.Unforwarded {
		if blocked.Host == endpoint.HostPort {
			return tunnel.Forward{}, fmt.Errorf(
				"macOS port %d is already in use, so the Forge %s is not forwarded; free the port and rerun `isolated-dev up`",
				endpoint.HostPort,
				endpoint.Label,
			)
		}
	}
	for _, forward := range state.Forwards {
		if forward.Host != endpoint.HostPort {
			continue
		}
		if forward.Guest != endpoint.GuestPort {
			return tunnel.Forward{}, fmt.Errorf(
				"macOS port %d is forwarded to guest port %d, but the Forge DEV profile publishes the %s on guest port %d",
				endpoint.HostPort,
				forward.Guest,
				endpoint.Label,
				endpoint.GuestPort,
			)
		}
		return forward, nil
	}
	return tunnel.Forward{}, fmt.Errorf(
		"macOS port %d is not forwarded by the managed tunnel, so the Forge %s cannot be reached from macOS",
		endpoint.HostPort,
		endpoint.Label,
	)
}

func (acceptance Acceptance) probeEndpoints(
	ctx context.Context,
	request Request,
	forwards []tunnel.Forward,
	result *Result,
) error {
	for index, endpoint := range request.Endpoints {
		body, err := acceptance.probe(ctx, endpoint)
		if err != nil {
			return err
		}
		result.Endpoints = append(result.Endpoints, EndpointState{
			Endpoint: endpoint,
			Forward:  forwards[index],
			Body:     recordBody(body),
		})
	}
	return nil
}

// probe reads one endpoint from macOS. A service that is healthy inside the
// guest can still need a moment to answer through a forward, so the read is
// retried before it is called unreachable.
func (acceptance Acceptance) probe(ctx context.Context, endpoint Endpoint) (string, error) {
	tries := acceptance.probeTries()
	var lastErr error
	for attempt := 0; attempt < tries; attempt++ {
		body, err := acceptance.Prober.Get(ctx, endpoint.URL())
		switch {
		case err != nil:
			lastErr = err
		case strings.TrimSpace(body) == "":
			lastErr = errors.New("the response was empty")
		default:
			return body, nil
		}
		if err := acceptance.pause(ctx, attempt, tries); err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf(
		"read the Forge %s at %s from macOS: %w",
		endpoint.Label,
		endpoint.URL(),
		lastErr,
	)
}

// diagnostic is one read-only command whose output explains a failure that the
// failing step alone does not.
type diagnostic struct {
	label string
	args  []string
}

// diagnose captures guest state and returns the original failure unchanged.
// Every command it runs reads: the acceptance workload is a real project, and
// nothing here may change what it left behind.
func (acceptance Acceptance) diagnose(
	ctx context.Context,
	request Request,
	cause error,
) error {
	if acceptance.Diagnostics == nil {
		return cause
	}
	diagnosticCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), diagnosticTimeout)
	defer cancel()

	diagnostics := []diagnostic{
		{label: "container machine list", args: []string{"machine", "list", "--format", "json"}},
		{label: "docker info", args: guestArgs(request.MachineName, "docker", "info")},
		{label: "compose ps", args: guestArgs(request.MachineName, composeArgs(
			request.GuestProjectPath, "ps", "--all",
		)...)},
		{label: "compose logs", args: guestArgs(request.MachineName, composeArgs(
			request.GuestProjectPath, "logs", "--no-color", "--tail", "100",
		)...)},
	}

	fmt.Fprintf(
		acceptance.Diagnostics,
		"Forge DEV acceptance diagnostics for machine %q\nfailure: %v\n",
		request.MachineName,
		cause,
	)
	for _, entry := range diagnostics {
		output, err := acceptance.Runner.Run(diagnosticCtx, "container", entry.args...)
		fmt.Fprintf(
			acceptance.Diagnostics,
			"--- %s ---\n%s\n",
			entry.label,
			strings.TrimRight(string(output), "\n"),
		)
		if err != nil {
			fmt.Fprintf(acceptance.Diagnostics, "(command failed: %v)\n", err)
		}
	}
	return cause
}

// withArchitecture attaches an architecture incompatibility to the result when
// the captured output carries one, and names it in the failure.
func withArchitecture(result *Result, output string, failure error) error {
	issue, found := ClassifyArchitecture(output)
	if !found {
		return failure
	}
	result.Architecture = &issue
	return fmt.Errorf("%w\n%s", failure, issue)
}

// guest runs one read-only command inside the machine as root and returns its
// combined output. Every element is a separate argument: no shell interprets
// any of it.
func (acceptance Acceptance) guest(
	ctx context.Context,
	machineName string,
	args ...string,
) ([]byte, error) {
	return acceptance.Runner.Run(ctx, "container", guestArgs(machineName, args...)...)
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

// composeArgs addresses the project's own stack explicitly, so a diagnostic
// never discovers a different one. The profile is named because the DEV
// services exist only inside it.
func composeArgs(guestProjectPath string, args ...string) []string {
	invocation := []string{
		"docker", "compose",
		"--project-directory", guestProjectPath,
		"--file", path.Join(guestProjectPath, ComposeFileName),
		"--profile", DevProfile,
	}
	return append(invocation, args...)
}

// declaredPort finds the `[[ports]]` entry that carries an endpoint. The
// acceptance criteria name macOS ports 3001 and 8001, so the entry is matched
// by the macOS port rather than by a name the project is free to choose.
func declaredPort(effectiveConfig config.Config, endpoint Endpoint) (config.Port, error) {
	for _, port := range effectiveConfig.Ports {
		if port.Host != endpoint.HostPort {
			continue
		}
		if port.Guest != endpoint.GuestPort {
			return config.Port{}, fmt.Errorf(
				"ports.%s forwards macOS port %d to guest port %d, but the Forge DEV profile publishes the %s on guest port %d",
				port.Name,
				port.Host,
				port.Guest,
				endpoint.Label,
				endpoint.GuestPort,
			)
		}
		return port, nil
	}
	return config.Port{}, fmt.Errorf(
		"no [[ports]] entry forwards macOS port %d, which the Forge %s is reached on; declare it in %s",
		endpoint.HostPort,
		endpoint.Label,
		config.SharedFileName,
	)
}

func (acceptance Acceptance) validate(request Request) error {
	dependencies := []struct {
		name     string
		supplied bool
	}{
		{name: "Forge acceptance command executor", supplied: acceptance.Commands != nil},
		{name: "Forge acceptance host command runner", supplied: acceptance.Runner != nil},
		{name: "Forge acceptance tunnel inspector", supplied: acceptance.Tunnels != nil},
		{name: "Forge acceptance macOS HTTP prober", supplied: acceptance.Prober != nil},
	}
	for _, dependency := range dependencies {
		if !dependency.supplied {
			return fmt.Errorf("%s is not configured", dependency.name)
		}
	}

	if !machineNamePattern.MatchString(request.MachineName) {
		return fmt.Errorf("invalid machine name %q", request.MachineName)
	}
	if !filepath.IsAbs(request.ProjectPath) {
		return fmt.Errorf("the Forge project path %q must be absolute", request.ProjectPath)
	}
	if !filepath.IsAbs(request.GuestProjectPath) {
		return fmt.Errorf("the guest project path %q must be absolute", request.GuestProjectPath)
	}
	if strings.TrimSpace(request.GuestUser) == "" {
		return fmt.Errorf(
			"machine %q has no recorded guest identity; run `isolated-dev up %s` first",
			request.MachineName,
			request.ProjectPath,
		)
	}
	if strings.TrimSpace(request.CommandName) == "" {
		return errors.New("the declared DEV command name is required")
	}
	return nil
}

// recordBody keeps a probed response in the result as evidence without letting
// a whole page of HTML into it.
func recordBody(body string) string {
	trimmed := strings.TrimSpace(body)
	if len(trimmed) <= maxRecordedBody {
		return trimmed
	}
	return trimmed[:maxRecordedBody] + "…"
}

func describeNames(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

func (acceptance Acceptance) healthTries() int {
	if acceptance.HealthTries > 0 {
		return acceptance.HealthTries
	}
	return 90
}

func (acceptance Acceptance) probeTries() int {
	if acceptance.ProbeTries > 0 {
		return acceptance.ProbeTries
	}
	return 30
}

// pause waits between attempts and stops early when the caller gives up.
func (acceptance Acceptance) pause(ctx context.Context, attempt int, tries int) error {
	if attempt+1 >= tries {
		return nil
	}
	delay := acceptance.RetryDelay
	if delay <= 0 {
		delay = 2 * time.Second
	}
	sleep := acceptance.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	sleep(delay)
	// A caller that gave up during the wait should not be made to sit through
	// the rest of the retry budget.
	return ctx.Err()
}

// boundedBuffer keeps the beginning of a stream and discards the rest, so a
// long build cannot grow the run's memory without bound.
type boundedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (bounded *boundedBuffer) Write(data []byte) (int, error) {
	if remaining := bounded.limit - bounded.buffer.Len(); remaining > 0 {
		kept := data
		if len(kept) > remaining {
			kept = kept[:remaining]
		}
		bounded.buffer.Write(kept)
	}
	return len(data), nil
}

func (bounded *boundedBuffer) String() string {
	return bounded.buffer.String()
}
