package forge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fxmartin/isolated-dev/internal/config"
	"github.com/fxmartin/isolated-dev/internal/projectcmd"
	"github.com/fxmartin/isolated-dev/internal/tunnel"
)

const composeFileContent = "# the Forge DEV stack, unchanged\nservices: {}\n"

type recordedCall struct {
	name string
	args []string
}

func (call recordedCall) line() string {
	return call.name + " " + strings.Join(call.args, " ")
}

type runnerStub struct {
	calls   []recordedCall
	respond func(recordedCall) ([]byte, error)
}

func (runner *runnerStub) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := recordedCall{name: name, args: append([]string(nil), args...)}
	runner.calls = append(runner.calls, call)
	if runner.respond == nil {
		return nil, nil
	}
	return runner.respond(call)
}

func (runner *runnerStub) lines() []string {
	lines := make([]string, 0, len(runner.calls))
	for _, call := range runner.calls {
		lines = append(lines, call.line())
	}
	return lines
}

func (runner *runnerStub) ran(t *testing.T, fragment string) recordedCall {
	t.Helper()

	for _, call := range runner.calls {
		if strings.Contains(call.line(), fragment) {
			return call
		}
	}
	t.Fatalf("no command containing %q was run; ran:\n%s", fragment, strings.Join(runner.lines(), "\n"))
	return recordedCall{}
}

type executorStub struct {
	requests []projectcmd.Request
	exitCode int
	err      error
	// respond writes what the guest command would print and returns its status.
	respond func(projectcmd.Streams) (int, error)
}

func (stub *executorStub) Execute(
	_ context.Context,
	request projectcmd.Request,
	streams projectcmd.Streams,
) (int, error) {
	stub.requests = append(stub.requests, request)
	if stub.respond != nil {
		return stub.respond(streams)
	}
	return stub.exitCode, stub.err
}

type tunnelStub struct {
	machines []string
	state    tunnel.State
	err      error
}

func (stub *tunnelStub) Inspect(machineName string) (tunnel.State, error) {
	stub.machines = append(stub.machines, machineName)
	return stub.state, stub.err
}

type proberStub struct {
	urls    []string
	bodies  map[string]string
	err     error
	failFor string
	// failures counts down the attempts that fail before the body is returned.
	failures int
}

func (stub *proberStub) Get(_ context.Context, url string) (string, error) {
	stub.urls = append(stub.urls, url)
	if stub.err != nil && (stub.failFor == "" || strings.Contains(url, stub.failFor)) {
		if stub.failures <= 0 {
			return "", stub.err
		}
		stub.failures--
		return "", stub.err
	}
	if body, ok := stub.bodies[url]; ok {
		return body, nil
	}
	return "ok", nil
}

// healthyStack answers every guest command a run against a healthy DEV stack
// issues: the four containers report their image, running state, and health.
func healthyStack(states map[string]string) func(recordedCall) ([]byte, error) {
	return func(call recordedCall) ([]byte, error) {
		line := call.line()
		if !strings.Contains(line, "docker inspect") {
			return nil, nil
		}
		var reported []string
		for _, service := range DevServices() {
			if !strings.Contains(line, service.Container) {
				continue
			}
			state, ok := states[service.Container]
			if !ok {
				return nil, fmt.Errorf("Error: No such object: %s", service.Container)
			}
			reported = append(reported, state)
		}
		return []byte(strings.Join(reported, "\n") + "\n"), nil
	}
}

func runningStates() map[string]string {
	return map[string]string{
		"rosetta-dev-db":       "postgres:16-alpine true healthy",
		"rosetta-dev-backend":  "rosetta-backend:latest true healthy",
		"rosetta-dev-worker":   "rosetta-backend:latest true healthy",
		"rosetta-dev-frontend": "rosetta-frontend:latest true none",
	}
}

type harness struct {
	acceptance  Acceptance
	runner      *runnerStub
	executor    *executorStub
	tunnels     *tunnelStub
	prober      *proberStub
	diagnostics *bytes.Buffer
	request     Request
	projectDir  string
	slept       int
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	projectDir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(projectDir, ComposeFileName),
		[]byte(composeFileContent),
		0o644,
	); err != nil {
		t.Fatalf("WriteFile(compose) error = %v", err)
	}

	test := &harness{
		runner:      &runnerStub{respond: healthyStack(runningStates())},
		executor:    &executorStub{},
		tunnels:     &tunnelStub{state: forwardedTunnel()},
		prober:      &proberStub{bodies: map[string]string{}},
		diagnostics: &bytes.Buffer{},
		projectDir:  projectDir,
	}
	test.prober.bodies["http://127.0.0.1:3001/"] = "<!doctype html><title>ROSETTA</title>"
	test.prober.bodies["http://127.0.0.1:8001/health"] = `{"status":"ok"}`

	test.acceptance = Acceptance{
		Runner:      test.runner,
		Commands:    test.executor,
		Tunnels:     test.tunnels,
		Prober:      test.prober,
		HealthTries: 3,
		ProbeTries:  3,
		RetryDelay:  time.Millisecond,
		Sleep:       func(time.Duration) { test.slept++ },
		Diagnostics: test.diagnostics,
	}
	test.request = Request{
		ProjectPath:      projectDir,
		MachineName:      "isolated-dev-forge-abcd1234",
		GuestUser:        "fx",
		GuestProjectPath: "/Users/fx/dev/forge",
		CommandName:      "dev",
		Config:           forgeConfig(),
	}
	return test
}

func forgeConfig() config.Config {
	configuration := config.Defaults()
	configuration.Ports = []config.Port{
		{Name: "frontend", Guest: 3001, Host: 3001},
		{Name: "backend", Guest: 8001, Host: 8001},
	}
	configuration.Commands = map[string]config.Command{
		"dev": {Args: append([]string(nil), DevCommandArgs...), Compose: true},
	}
	return configuration
}

func forwardedTunnel() tunnel.State {
	return tunnel.State{
		Running: true,
		PID:     4242,
		Forwards: []tunnel.Forward{
			{Name: "frontend", Host: 3001, Guest: 3001},
			{Name: "backend", Host: 8001, Guest: 8001},
		},
	}
}

func (test *harness) run(t *testing.T) (Result, error) {
	t.Helper()

	return test.acceptance.Run(context.Background(), test.request)
}

// assertNothingDestroyed guards the difference between this acceptance run and
// the disposable baseline: Forge is a real repository with real named volumes,
// so the run may never remove a machine, an image, a container, or a volume.
func (test *harness) assertNothingDestroyed(t *testing.T) {
	t.Helper()

	for _, line := range test.runner.lines() {
		for _, destructive := range []string{
			"machine delete", "machine stop", "image delete", "compose down",
			"volume rm", "container rm", "docker rm", "system prune",
		} {
			if strings.Contains(line, destructive) {
				t.Errorf("the acceptance run issued %q, which removes state it does not own", line)
			}
		}
	}
	data, err := os.ReadFile(filepath.Join(test.projectDir, ComposeFileName))
	if err != nil {
		t.Fatalf("ReadFile(compose) error = %v", err)
	}
	if string(data) != composeFileContent {
		t.Errorf("the Compose file changed to %q, want the repository's own file untouched", data)
	}
	entries, err := os.ReadDir(test.projectDir)
	if err != nil {
		t.Fatalf("ReadDir(project) error = %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("the acceptance run left %d files in the project, want only its own Compose file", len(entries))
	}
}

func TestRunStartsTheUnmodifiedDevStackAndProvesItFromMacOS(t *testing.T) {
	test := newHarness(t)

	result, err := test.run(t)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(test.executor.requests) != 1 {
		t.Fatalf("the DEV command ran %d times, want exactly once", len(test.executor.requests))
	}
	executed := test.executor.requests[0]
	if executed.MachineName != test.request.MachineName ||
		executed.GuestUser != test.request.GuestUser ||
		executed.GuestProjectPath != test.request.GuestProjectPath {
		t.Errorf("executed %+v, want it addressed to the project machine and its mounted project", executed)
	}
	if got := strings.Join(executed.Command.Args, " "); got != strings.Join(DevCommandArgs, " ") {
		t.Errorf("ran %q, want the declared %q", got, strings.Join(DevCommandArgs, " "))
	}
	if executed.Command.Workdir != "" {
		t.Errorf("workdir = %q, want the project root, where the repository's Compose file is", executed.Command.Workdir)
	}

	inspect := test.runner.ran(t, "docker inspect")
	for _, service := range DevServices() {
		if !strings.Contains(inspect.line(), service.Container) {
			t.Errorf("docker inspect did not check %s (%s)", service.Container, service.Description)
		}
	}

	if len(result.Services) != 4 {
		t.Fatalf("Result.Services = %d, want the four DEV services", len(result.Services))
	}
	for _, service := range result.Services {
		if !service.Running {
			t.Errorf("%s is reported as not running", service.Container)
		}
	}
	if result.Services[0].Image != "postgres:16-alpine" {
		t.Errorf("database image = %q, want PostgreSQL 16", result.Services[0].Image)
	}

	if len(result.Endpoints) != 2 {
		t.Fatalf("Result.Endpoints = %d, want the frontend and the backend health endpoint", len(result.Endpoints))
	}
	if result.Endpoints[0].Forward.Host != 3001 || result.Endpoints[1].Forward.Host != 8001 {
		t.Errorf("endpoints = %+v, want them proven through the managed 3001 and 8001 forwards", result.Endpoints)
	}
	if !strings.Contains(result.Endpoints[1].Body, "ok") {
		t.Errorf("backend health body = %q, want the health response recorded", result.Endpoints[1].Body)
	}

	wantURLs := []string{"http://127.0.0.1:3001/", "http://127.0.0.1:8001/health"}
	if strings.Join(test.prober.urls, " ") != strings.Join(wantURLs, " ") {
		t.Errorf("probed %v, want %v from macOS", test.prober.urls, wantURLs)
	}
	if test.tunnels.machines == nil {
		t.Error("the managed tunnel was never inspected")
	}

	digest, err := ComposeDigest(test.projectDir)
	if err != nil {
		t.Fatalf("ComposeDigest() error = %v", err)
	}
	if result.ComposeDigest != digest {
		t.Errorf("Result.ComposeDigest = %q, want the digest %q of the unmodified file", result.ComposeDigest, digest)
	}
	if result.Architecture != nil {
		t.Errorf("Result.Architecture = %+v, want none on a successful run", result.Architecture)
	}
	if test.diagnostics.Len() != 0 {
		t.Errorf("diagnostics were captured for a successful run:\n%s", test.diagnostics)
	}
	test.assertNothingDestroyed(t)
}

func TestRunWaitsForTheStackToBecomeHealthy(t *testing.T) {
	test := newHarness(t)
	starting := runningStates()
	starting["rosetta-dev-backend"] = "rosetta-backend:latest true starting"
	attempts := 0
	test.runner.respond = func(call recordedCall) ([]byte, error) {
		if strings.Contains(call.line(), "docker inspect") {
			attempts++
			if attempts == 1 {
				return healthyStack(starting)(call)
			}
		}
		return healthyStack(runningStates())(call)
	}

	if _, err := test.run(t); err != nil {
		t.Fatalf("Run() error = %v, want the run to wait for the backend health check", err)
	}
	if attempts != 2 {
		t.Errorf("docker inspect ran %d times, want it retried until the stack was healthy", attempts)
	}
	if test.slept == 0 {
		t.Error("the run did not pause between health checks")
	}
}

func TestRunReportsAServiceThatNeverBecomesHealthy(t *testing.T) {
	test := newHarness(t)
	stuck := runningStates()
	stuck["rosetta-dev-worker"] = "rosetta-backend:latest true starting"
	test.runner.respond = healthyStack(stuck)

	result, err := test.run(t)
	if err == nil {
		t.Fatal("Run() error = nil, want the unhealthy worker reported")
	}
	if !strings.Contains(err.Error(), "rosetta-dev-worker") || !strings.Contains(err.Error(), "starting") {
		t.Errorf("error = %q, want it to name the worker and its health", err)
	}
	if len(result.Services) != 4 {
		t.Errorf("Result.Services = %d, want the observed state of every service", len(result.Services))
	}
	if !strings.Contains(test.diagnostics.String(), "docker info") {
		t.Errorf("diagnostics = %q, want the guest state captured", test.diagnostics)
	}
	test.assertNothingDestroyed(t)
}

func TestRunReportsAServiceThatIsNotTheExpectedImage(t *testing.T) {
	test := newHarness(t)
	wrong := runningStates()
	wrong["rosetta-dev-db"] = "postgres:15-alpine true healthy"
	test.runner.respond = healthyStack(wrong)

	_, err := test.run(t)
	if err == nil {
		t.Fatal("Run() error = nil, want the wrong PostgreSQL version reported")
	}
	if !strings.Contains(err.Error(), "postgres:15-alpine") || !strings.Contains(err.Error(), "postgres:16") {
		t.Errorf("error = %q, want it to compare the running image with PostgreSQL 16", err)
	}
}

func TestRunReportsAnArchitectureIncompatibilityFromTheStartupOutput(t *testing.T) {
	test := newHarness(t)
	test.executor.respond = func(streams projectcmd.Streams) (int, error) {
		fmt.Fprintln(
			streams.Stderr,
			"ERROR: failed to solve: postgres:16-alpine: no matching manifest for linux/arm64/v8 in the manifest list entries",
		)
		return 1, nil
	}

	result, err := test.run(t)
	if err == nil {
		t.Fatal("Run() error = nil, want the failed startup reported")
	}
	if result.Architecture == nil {
		t.Fatal("Result.Architecture = nil, want the incompatibility identified")
	}
	if result.Architecture.Image != "postgres:16-alpine" {
		t.Errorf("Architecture.Image = %q, want the affected image", result.Architecture.Image)
	}
	if result.Architecture.Requirement != RequirementPlatform {
		t.Errorf("Architecture.Requirement = %q, want %q", result.Architecture.Requirement, RequirementPlatform)
	}
	if !strings.Contains(err.Error(), "postgres:16-alpine") ||
		!strings.Contains(err.Error(), string(RequirementPlatform)) {
		t.Errorf("error = %q, want it to name the image and the support it requires", err)
	}
	if result.ExitCode != 1 {
		t.Errorf("Result.ExitCode = %d, want the status the DEV command produced", result.ExitCode)
	}
	test.assertNothingDestroyed(t)
}

func TestRunReportsAnArchitectureIncompatibilityFromServiceLogs(t *testing.T) {
	test := newHarness(t)
	crashed := runningStates()
	crashed["rosetta-dev-worker"] = "rosetta-backend:latest false none"
	test.runner.respond = func(call recordedCall) ([]byte, error) {
		if strings.Contains(call.line(), "docker logs") {
			return []byte("exec /usr/local/bin/python: exec format error\n"), nil
		}
		return healthyStack(crashed)(call)
	}

	result, err := test.run(t)
	if err == nil {
		t.Fatal("Run() error = nil, want the stopped worker reported")
	}
	if result.Architecture == nil {
		t.Fatal("Result.Architecture = nil, want the incompatibility identified from the service logs")
	}
	if result.Architecture.Requirement != RequirementBinfmt {
		t.Errorf("Architecture.Requirement = %q, want %q", result.Architecture.Requirement, RequirementBinfmt)
	}
	if !strings.Contains(err.Error(), "rosetta-dev-worker") {
		t.Errorf("error = %q, want it to name the affected service", err)
	}
	test.runner.ran(t, "docker logs")
}

func TestRunFailsWhenTheStartupCommandCannotRun(t *testing.T) {
	test := newHarness(t)
	test.executor.err = errors.New("machine is not running")

	_, err := test.run(t)
	if err == nil {
		t.Fatal("Run() error = nil, want the unrunnable command reported")
	}
	if !strings.Contains(err.Error(), "machine is not running") {
		t.Errorf("error = %q, want the underlying failure preserved", err)
	}
	if len(test.prober.urls) != 0 {
		t.Errorf("macOS was probed at %v after the stack never started", test.prober.urls)
	}
}

func TestRunRejectsAComposeFileThatChangedWhileTheStackStarted(t *testing.T) {
	test := newHarness(t)
	test.executor.respond = func(projectcmd.Streams) (int, error) {
		path := filepath.Join(test.projectDir, ComposeFileName)
		return 0, os.WriteFile(path, []byte(composeFileContent+"# rewritten\n"), 0o644)
	}

	_, err := test.run(t)
	if err == nil {
		t.Fatal("Run() error = nil, want the modified Compose file reported")
	}
	if !strings.Contains(err.Error(), ComposeFileName) {
		t.Errorf("error = %q, want it to name the file that changed", err)
	}
}

func TestRunRequiresTheDeclaredDevCommand(t *testing.T) {
	tests := map[string]struct {
		mutate func(*Request)
		want   string
	}{
		"undeclared": {
			mutate: func(request *Request) { request.CommandName = "start" },
			want:   `"start" is not declared`,
		},
		"declared as something else": {
			mutate: func(request *Request) {
				request.Config.Commands["dev"] = config.Command{
					Args:    []string{"make", "dev"},
					Compose: true,
				}
			},
			want: "docker compose --profile dev up -d",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			harness := newHarness(t)
			test.mutate(&harness.request)

			_, err := harness.run(t)
			if err == nil {
				t.Fatal("Run() error = nil, want the declaration rejected")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("error = %q, want it to mention %q", err, test.want)
			}
			if len(harness.executor.requests) != 0 {
				t.Error("the DEV command ran despite an unusable declaration")
			}
		})
	}
}

func TestRunRequiresTheDeclaredMacOSPortsBeforeStartingAnything(t *testing.T) {
	test := newHarness(t)
	test.request.Config.Ports = []config.Port{{Name: "frontend", Guest: 3001, Host: 3001}}

	_, err := test.run(t)
	if err == nil {
		t.Fatal("Run() error = nil, want the undeclared backend port reported")
	}
	if !strings.Contains(err.Error(), "8001") {
		t.Errorf("error = %q, want it to name the missing macOS port", err)
	}
	if len(test.executor.requests) != 0 {
		t.Error("the DEV command ran before the declared ports were checked")
	}
}

func TestRunRequiresTheDeclaredPortToReachTheGuestPortForgePublishes(t *testing.T) {
	test := newHarness(t)
	test.request.Config.Ports[1] = config.Port{Name: "backend", Guest: 8000, Host: 8001}

	_, err := test.run(t)
	if err == nil {
		t.Fatal("Run() error = nil, want the mismatched guest port reported")
	}
	if !strings.Contains(err.Error(), "8000") || !strings.Contains(err.Error(), "8001") {
		t.Errorf("error = %q, want it to compare the declared and published guest ports", err)
	}
}

func TestRunReportsAManagedTunnelThatCannotCarryTheEndpoints(t *testing.T) {
	tests := map[string]struct {
		state tunnel.State
		err   error
		want  string
	}{
		"stopped": {
			state: tunnel.State{Forwards: forwardedTunnel().Forwards},
			want:  "is not running",
		},
		"port taken on macOS": {
			state: tunnel.State{
				Running:     true,
				PID:         4242,
				Forwards:    []tunnel.Forward{{Name: "backend", Host: 8001, Guest: 8001}},
				Unforwarded: []tunnel.Forward{{Name: "frontend", Host: 3001, Guest: 3001}},
			},
			want: "macOS port 3001",
		},
		"not inspectable": {
			err:  errors.New("tunnel record is unreadable"),
			want: "tunnel record is unreadable",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			harness := newHarness(t)
			harness.tunnels.state = test.state
			harness.tunnels.err = test.err

			_, err := harness.run(t)
			if err == nil {
				t.Fatal("Run() error = nil, want the tunnel failure reported")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("error = %q, want it to mention %q", err, test.want)
			}
			if len(harness.prober.urls) != 0 {
				t.Errorf("macOS was probed at %v through a tunnel that cannot carry the ports", harness.prober.urls)
			}
		})
	}
}

func TestRunRetriesAnEndpointBeforeReportingItUnreachable(t *testing.T) {
	test := newHarness(t)
	test.prober.err = errors.New("connection refused")
	test.prober.failFor = "8001"
	test.prober.failures = 1

	_, err := test.run(t)
	if err == nil {
		t.Fatal("Run() error = nil, want the unreachable backend reported")
	}
	if !strings.Contains(err.Error(), "http://127.0.0.1:8001/health") ||
		!strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error = %q, want the URL and the reason", err)
	}
	backendProbes := 0
	for _, url := range test.prober.urls {
		if strings.Contains(url, "8001") {
			backendProbes++
		}
	}
	if backendProbes != test.acceptance.ProbeTries {
		t.Errorf("probed the backend %d times, want %d attempts", backendProbes, test.acceptance.ProbeTries)
	}
}

func TestRunRejectsAnEmptyResponseFromAnEndpoint(t *testing.T) {
	test := newHarness(t)
	test.prober.bodies["http://127.0.0.1:3001/"] = "   "

	_, err := test.run(t)
	if err == nil {
		t.Fatal("Run() error = nil, want the empty frontend response reported")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error = %q, want it to report the empty response", err)
	}
}

func TestRunValidatesItsDependenciesAndRequest(t *testing.T) {
	tests := map[string]struct {
		mutate func(*harness)
		want   string
	}{
		"no command executor": {
			mutate: func(test *harness) { test.acceptance.Commands = nil },
			want:   "command executor",
		},
		"no host command runner": {
			mutate: func(test *harness) { test.acceptance.Runner = nil },
			want:   "host command runner",
		},
		"no tunnel inspector": {
			mutate: func(test *harness) { test.acceptance.Tunnels = nil },
			want:   "tunnel",
		},
		"no macOS prober": {
			mutate: func(test *harness) { test.acceptance.Prober = nil },
			want:   "prober",
		},
		"invalid machine name": {
			mutate: func(test *harness) { test.request.MachineName = "-not a machine" },
			want:   "invalid machine name",
		},
		"relative project path": {
			mutate: func(test *harness) { test.request.ProjectPath = "forge" },
			want:   "must be absolute",
		},
		"relative guest project path": {
			mutate: func(test *harness) { test.request.GuestProjectPath = "forge" },
			want:   "must be absolute",
		},
		"no guest user": {
			mutate: func(test *harness) { test.request.GuestUser = "" },
			want:   "guest identity",
		},
		"no command name": {
			mutate: func(test *harness) { test.request.CommandName = "  " },
			want:   "command name",
		},
		"missing compose file": {
			mutate: func(test *harness) { test.request.ProjectPath = t.TempDir() },
			want:   ComposeFileName,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			harness := newHarness(t)
			test.mutate(harness)

			_, err := harness.run(t)
			if err == nil {
				t.Fatal("Run() error = nil, want the request rejected")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("error = %q, want it to mention %q", err, test.want)
			}
			if len(harness.executor.requests) != 0 {
				t.Error("the DEV command ran despite an invalid request")
			}
		})
	}
}

func TestRunCapturesGuestDiagnosticsWhenTheStackFails(t *testing.T) {
	test := newHarness(t)
	test.executor.exitCode = 1

	if _, err := test.run(t); err == nil {
		t.Fatal("Run() error = nil, want the failed startup reported")
	}
	captured := test.diagnostics.String()
	for _, want := range []string{"machine list", "docker info", "compose ps", "compose logs"} {
		if !strings.Contains(captured, want) {
			t.Errorf("diagnostics do not include %q:\n%s", want, captured)
		}
	}
	// Diagnostics read state; they never change it.
	for _, call := range test.runner.lines() {
		if strings.Contains(call, "compose") &&
			!strings.Contains(call, " ps ") && !strings.Contains(call, " logs ") {
			t.Errorf("diagnostic command %q does more than read the stack state", call)
		}
	}
}

func TestRunReportsTheStackWithoutDiagnosticsWhenNoneAreConfigured(t *testing.T) {
	test := newHarness(t)
	test.acceptance.Diagnostics = nil
	test.executor.exitCode = 2

	result, err := test.run(t)
	if err == nil {
		t.Fatal("Run() error = nil, want the failed startup reported")
	}
	if result.ExitCode != 2 {
		t.Errorf("Result.ExitCode = %d, want 2", result.ExitCode)
	}
}
