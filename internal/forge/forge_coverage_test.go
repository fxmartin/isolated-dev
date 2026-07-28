package forge

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fxmartin/isolated-dev/internal/config"
	"github.com/fxmartin/isolated-dev/internal/projectcmd"
	"github.com/fxmartin/isolated-dev/internal/tunnel"
)

func TestRunStreamsTheStartupOutputWhileItRuns(t *testing.T) {
	test := newHarness(t)
	live := &bytes.Buffer{}
	test.acceptance.Output = live
	test.executor.respond = func(streams projectcmd.Streams) (int, error) {
		_, err := streams.Stdout.Write([]byte("[+] Running 4/4\n"))
		return 0, err
	}

	if _, err := test.run(t); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(live.String(), "[+] Running 4/4") {
		t.Errorf("streamed output = %q, want the DEV command's own output", live)
	}
}

func TestRunReportsAComposeFileThatDisappearedDuringTheRun(t *testing.T) {
	test := newHarness(t)
	test.executor.respond = func(projectcmd.Streams) (int, error) {
		return 0, os.Remove(filepath.Join(test.projectDir, ComposeFileName))
	}

	_, err := test.run(t)
	if err == nil {
		t.Fatal("Run() error = nil, want the unreadable Compose file reported")
	}
	if !strings.Contains(err.Error(), ComposeFileName) {
		t.Errorf("error = %q, want it to name the Compose file", err)
	}
}

func TestRunReportsContainersThatCannotBeInspected(t *testing.T) {
	tests := map[string]struct {
		respond func(recordedCall) ([]byte, error)
		want    string
	}{
		"inspect fails": {
			respond: func(call recordedCall) ([]byte, error) {
				if strings.Contains(call.line(), "docker inspect") {
					return []byte("Error: No such object: rosetta-dev-backend"), errors.New("exit status 1")
				}
				return nil, nil
			},
			want: "No such object",
		},
		"fewer containers than services": {
			respond: func(call recordedCall) ([]byte, error) {
				if strings.Contains(call.line(), "docker inspect") {
					return []byte("postgres:16-alpine true healthy\n"), nil
				}
				return nil, nil
			},
			want: "reported 1 containers",
		},
		"unreadable state": {
			respond: func(call recordedCall) ([]byte, error) {
				if strings.Contains(call.line(), "docker inspect") {
					return []byte(strings.Repeat("unreadable\n", 4)), nil
				}
				return nil, nil
			},
			want: "could not read the state",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			harness := newHarness(t)
			harness.runner.respond = test.respond

			_, err := harness.run(t)
			if err == nil {
				t.Fatal("Run() error = nil, want the unreadable stack reported")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("error = %q, want it to mention %q", err, test.want)
			}
			// With no readable state, every service's log is worth reading.
			for _, service := range DevServices() {
				harness.runner.ran(t, "docker logs --tail 100 "+service.Container)
			}
		})
	}
}

func TestRunReportsATunnelForwardingTheWrongGuestPort(t *testing.T) {
	test := newHarness(t)
	test.tunnels.state = tunnel.State{
		Running: true,
		PID:     4242,
		Forwards: []tunnel.Forward{
			{Name: "frontend", Host: 3001, Guest: 3001},
			{Name: "backend", Host: 8001, Guest: 8000},
		},
	}

	_, err := test.run(t)
	if err == nil {
		t.Fatal("Run() error = nil, want the stale forward reported")
	}
	if !strings.Contains(err.Error(), "guest port 8000") {
		t.Errorf("error = %q, want it to name the guest port the tunnel actually reaches", err)
	}
}

func TestRunReportsAnEndpointTheTunnelDoesNotCarry(t *testing.T) {
	test := newHarness(t)
	test.tunnels.state = tunnel.State{
		Running:  true,
		PID:      4242,
		Forwards: []tunnel.Forward{{Name: "backend", Host: 8001, Guest: 8001}},
	}

	_, err := test.run(t)
	if err == nil {
		t.Fatal("Run() error = nil, want the missing frontend forward reported")
	}
	if !strings.Contains(err.Error(), "macOS port 3001 is not forwarded") {
		t.Errorf("error = %q, want it to name the port the tunnel does not carry", err)
	}
}

func TestRunStopsWaitingForTheStackWhenTheCallerGivesUp(t *testing.T) {
	test := newHarness(t)
	test.runner.respond = func(recordedCall) ([]byte, error) {
		return nil, errors.New("machine is busy")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := test.acceptance.Run(ctx, test.request); !errors.Is(err, context.Canceled) {
		t.Errorf("Run() error = %v, want the cancellation to end the health wait", err)
	}
}

func TestRunReportsDiagnosticCommandsThatFail(t *testing.T) {
	test := newHarness(t)
	test.executor.exitCode = 1
	test.runner.respond = func(recordedCall) ([]byte, error) {
		return []byte("machine not found"), errors.New("exit status 1")
	}

	if _, err := test.run(t); err == nil {
		t.Fatal("Run() error = nil, want the failed startup reported")
	}
	if !strings.Contains(test.diagnostics.String(), "command failed") {
		t.Errorf("diagnostics = %q, want a diagnostic that failed to be visible as such", test.diagnostics)
	}
}

func TestRunNamesTheProjectWithNoDeclaredCommands(t *testing.T) {
	test := newHarness(t)
	test.request.Config.Commands = map[string]config.Command{}

	_, err := test.run(t)
	if err == nil {
		t.Fatal("Run() error = nil, want the missing declaration reported")
	}
	if !strings.Contains(err.Error(), "none") {
		t.Errorf("error = %q, want it to report that the project declares no commands", err)
	}
}

func TestProbeStopsWhenTheCallerGivesUp(t *testing.T) {
	test := newHarness(t)
	test.prober.err = errors.New("connection refused")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := test.acceptance.probe(ctx, DevEndpoints()[0])
	if !errors.Is(err, context.Canceled) {
		t.Errorf("probe() error = %v, want the cancellation to end the retries", err)
	}
}

func TestRetryBudgetsAndPausesHaveWorkingDefaults(t *testing.T) {
	unconfigured := Acceptance{}
	if unconfigured.healthTries() < 1 || unconfigured.probeTries() < 1 {
		t.Errorf(
			"defaults = %d health tries and %d probe tries, want a usable budget",
			unconfigured.healthTries(),
			unconfigured.probeTries(),
		)
	}

	// The last attempt never waits, and an unconfigured pause uses the real
	// clock rather than nothing at all.
	if err := (Acceptance{}).pause(context.Background(), 1, 2); err != nil {
		t.Errorf("pause() on the last attempt error = %v", err)
	}
	start := time.Now()
	if err := (Acceptance{RetryDelay: 5 * time.Millisecond}).pause(context.Background(), 0, 2); err != nil {
		t.Errorf("pause() error = %v", err)
	}
	if time.Since(start) < 5*time.Millisecond {
		t.Error("pause() returned before its delay elapsed")
	}

	var waited time.Duration
	unconfiguredPause := Acceptance{Sleep: func(delay time.Duration) { waited = delay }}
	if err := unconfiguredPause.pause(context.Background(), 0, 2); err != nil {
		t.Errorf("pause() error = %v", err)
	}
	if waited <= 0 {
		t.Error("an unconfigured retry delay waited for nothing between attempts")
	}
}

func TestRecordBodyKeepsEvidenceWithoutTheWholePage(t *testing.T) {
	recorded := recordBody("  " + strings.Repeat("a", maxRecordedBody+50) + "  ")
	if len(recorded) != maxRecordedBody+len("…") {
		t.Errorf("recordBody() kept %d characters, want it truncated to %d", len(recorded), maxRecordedBody)
	}
	if got := recordBody("  ok  "); got != "ok" {
		t.Errorf("recordBody() = %q, want the trimmed body", got)
	}
	// A body that is exactly the limit is evidence in full, not evidence with an
	// ellipsis claiming something was left out.
	whole := strings.Repeat("a", maxRecordedBody)
	if got := recordBody(whole); got != whole {
		t.Errorf("recordBody() truncated a body of exactly %d characters", maxRecordedBody)
	}
}

func TestBoundedBufferKeepsTheBeginningOfALongStream(t *testing.T) {
	bounded := &boundedBuffer{limit: 8}
	written, err := bounded.Write([]byte("0123456789"))
	if err != nil || written != 10 {
		t.Fatalf("Write() = %d, %v; want the whole write accepted", written, err)
	}
	if _, err := bounded.Write([]byte("more")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if bounded.String() != "01234567" {
		t.Errorf("boundedBuffer holds %q, want the first 8 bytes", bounded.String())
	}
}

func TestImageBeforeIgnoresWhatIsNotAnImageReference(t *testing.T) {
	if got := imageBefore("no matching manifest", "no matching manifest"); got != "" {
		t.Errorf("imageBefore() = %q, want nothing before the message", got)
	}
	if got := imageBefore("failed to solve: no matching manifest", "unmatched"); got != "" {
		t.Errorf("imageBefore() = %q, want nothing when the fragment is absent", got)
	}
	line := "pulled from https://registry.example.com/v2/ with --platform=linux/amd64: " +
		"no matching manifest for linux/arm64"
	if got := imageBefore(line, "no matching manifest"); got != "" {
		t.Errorf("imageBefore() = %q, want URLs and flags rejected", got)
	}
}

func TestBuildStepFallsBackToTheFailingProcess(t *testing.T) {
	output := strings.Join([]string{
		"0.2 exec /bin/sh: exec format error",
		`ERROR: failed to solve: process "/bin/sh -c npm ci" did not complete successfully: exit code: 1`,
	}, "\n")

	issue, found := ClassifyArchitecture(output)
	if !found {
		t.Fatal("ClassifyArchitecture() found = false, want the emulation requirement")
	}
	if issue.BuildStep != `process "/bin/sh -c npm ci"` {
		t.Errorf("BuildStep = %q, want the failing process", issue.BuildStep)
	}

	unterminated := "exec format error\nERROR: failed to solve: process \"/bin/sh -c npm ci"
	if issue, _ := ClassifyArchitecture(unterminated); issue.BuildStep != "" {
		t.Errorf("BuildStep = %q, want nothing recovered from an unterminated quote", issue.BuildStep)
	}
}

func TestShortenKeepsLongOutputReadable(t *testing.T) {
	issue, found := ClassifyArchitecture(strings.Repeat("x", maxSignatureLength) + " exec format error")
	if !found {
		t.Fatal("ClassifyArchitecture() found = false, want the emulation requirement")
	}
	if !strings.HasSuffix(issue.Signature, "…") {
		t.Errorf("Signature = %q, want a long line truncated", issue.Signature)
	}
}

// TestRunReadsOnlyTheLogsOfTheServicesThatAreNotReady pins the selection the
// failure report makes. A Forge DEV stack prints a lot, and a report that
// dumped all four services' logs would bury the one that explains the failure.
func TestRunReadsOnlyTheLogsOfTheServicesThatAreNotReady(t *testing.T) {
	test := newHarness(t)
	crashed := runningStates()
	crashed["rosetta-dev-worker"] = "rosetta-backend:latest false none"
	test.runner.respond = healthyStack(crashed)

	if _, err := test.run(t); err == nil {
		t.Fatal("Run() error = nil, want the stopped worker reported")
	}

	test.runner.ran(t, "docker logs --tail 100 rosetta-dev-worker")
	for _, ready := range []string{"rosetta-dev-db", "rosetta-dev-backend", "rosetta-dev-frontend"} {
		for _, line := range test.runner.lines() {
			if strings.Contains(line, "docker logs") && strings.Contains(line, ready) {
				t.Errorf("the report read the logs of %s, which was already healthy", ready)
			}
		}
	}
}

// TestRunReportsAWrongImageWithoutWaitingOutTheHealthBudget holds the run to
// the distinction it makes between "not ready yet" and "never will be": a
// service on the wrong image is the second, so waiting would only delay the
// answer by the whole retry budget.
func TestRunReportsAWrongImageWithoutWaitingOutTheHealthBudget(t *testing.T) {
	test := newHarness(t)
	wrong := runningStates()
	wrong["rosetta-dev-db"] = "postgres:15-alpine true healthy"
	inspections := 0
	test.runner.respond = func(call recordedCall) ([]byte, error) {
		if strings.Contains(call.line(), "docker inspect") {
			inspections++
		}
		return healthyStack(wrong)(call)
	}

	if _, err := test.run(t); err == nil {
		t.Fatal("Run() error = nil, want the wrong PostgreSQL version reported")
	}
	if inspections != 1 {
		t.Errorf("docker inspect ran %d times, want the wrong image reported on the first check", inspections)
	}
	if test.slept != 0 {
		t.Errorf("the run paused %d times waiting for an image that never becomes the right one", test.slept)
	}
}

// TestRunInspectsTheGuestWithoutAShellAndWithAnExplicitPath pins how guest
// commands are issued. `container machine run` guarantees no PATH, and passing
// the container names as separate arguments is what keeps a name out of a
// shell's hands.
func TestRunInspectsTheGuestWithoutAShellAndWithAnExplicitPath(t *testing.T) {
	test := newHarness(t)
	if _, err := test.run(t); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	inspect := test.runner.ran(t, "docker inspect")
	if inspect.name != "container" {
		t.Errorf("guest inspection ran %q, want the `container` CLI", inspect.name)
	}
	wantPrefix := []string{
		"machine", "run",
		"--name", test.request.MachineName,
		"--root",
		"--",
		"/usr/bin/env", "PATH=" + guestPath,
		"docker", "inspect",
	}
	if len(inspect.args) < len(wantPrefix) {
		t.Fatalf("guest inspection args = %v, want them to start with %v", inspect.args, wantPrefix)
	}
	if got := strings.Join(inspect.args[:len(wantPrefix)], " "); got != strings.Join(wantPrefix, " ") {
		t.Errorf("guest inspection ran %q, want %q", got, strings.Join(wantPrefix, " "))
	}
	for _, arg := range inspect.args {
		if arg == "-c" || arg == "sh" || arg == "bash" {
			t.Errorf("guest inspection args = %v, want no shell interpreting any of it", inspect.args)
		}
	}
}

// TestDiagnosticsAddressTheProjectsOwnComposeStack keeps a failure report
// pointed at the repository's own Compose file. A diagnostic that discovered a
// stack instead could describe a different one than the run started.
func TestDiagnosticsAddressTheProjectsOwnComposeStack(t *testing.T) {
	test := newHarness(t)
	test.executor.exitCode = 1

	if _, err := test.run(t); err == nil {
		t.Fatal("Run() error = nil, want the failed startup reported")
	}

	composeCalls := 0
	for _, line := range test.runner.lines() {
		if !strings.Contains(line, "docker compose") {
			continue
		}
		composeCalls++
		for _, want := range []string{
			"--project-directory " + test.request.GuestProjectPath,
			"--file " + test.request.GuestProjectPath + "/" + ComposeFileName,
			"--profile " + DevProfile,
		} {
			if !strings.Contains(line, want) {
				t.Errorf("diagnostic %q does not pin %q", line, want)
			}
		}
	}
	if composeCalls != 2 {
		t.Errorf("ran %d Compose diagnostics, want the stack listed and its logs read", composeCalls)
	}
}

// TestRunVerifiesTheServicesAndEndpointsTheRequestNames proves the DEV profile
// is the default rather than the only thing the run can check, which is what
// lets the same acceptance path cover a stack that is not Forge's.
func TestRunVerifiesTheServicesAndEndpointsTheRequestNames(t *testing.T) {
	test := newHarness(t)
	test.request.Services = []Service{
		{Container: "rosetta-dev-docs", Description: "the docs site", ImagePrefix: "nginx:"},
	}
	test.request.Endpoints = []Endpoint{
		{Label: "docs", Path: "/docs", HostPort: 9001, GuestPort: 9001},
	}
	test.request.Config.Ports = []config.Port{{Name: "docs", Guest: 9001, Host: 9001}}
	test.tunnels.state = tunnel.State{
		Running:  true,
		PID:      4242,
		Forwards: []tunnel.Forward{{Name: "docs", Host: 9001, Guest: 9001}},
	}
	test.runner.respond = func(call recordedCall) ([]byte, error) {
		if strings.Contains(call.line(), "docker inspect") {
			return []byte("nginx:1.27 true healthy\n"), nil
		}
		return nil, nil
	}

	result, err := test.run(t)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(result.Services) != 1 || result.Services[0].Container != "rosetta-dev-docs" {
		t.Errorf("Result.Services = %+v, want only the service the request named", result.Services)
	}
	inspect := test.runner.ran(t, "docker inspect")
	for _, service := range DevServices() {
		if strings.Contains(inspect.line(), service.Container) {
			t.Errorf("docker inspect checked %s, which the request did not name", service.Container)
		}
	}
	if len(test.prober.urls) != 1 || test.prober.urls[0] != "http://127.0.0.1:9001/docs" {
		t.Errorf("probed %v, want only the endpoint the request named", test.prober.urls)
	}
}

// TestRunReturnsWhatItProvedWhenALaterStepFails holds Run to its contract of
// reporting how far it got: a run that reached macOS and failed on the second
// endpoint still has to say the stack started and the Compose file was the
// repository's own.
func TestRunReturnsWhatItProvedWhenALaterStepFails(t *testing.T) {
	test := newHarness(t)
	test.prober.err = errors.New("connection refused")
	test.prober.failFor = "8001"

	result, err := test.run(t)
	if err == nil {
		t.Fatal("Run() error = nil, want the unreachable backend reported")
	}
	if result.ComposeFile != filepath.Join(test.projectDir, ComposeFileName) {
		t.Errorf("Result.ComposeFile = %q, want the project's own Compose file", result.ComposeFile)
	}
	if result.ComposeDigest == "" {
		t.Error("Result.ComposeDigest is empty on a run that did start the stack")
	}
	if strings.Join(result.Command, " ") != strings.Join(DevCommandArgs, " ") {
		t.Errorf("Result.Command = %v, want the declared DEV command", result.Command)
	}
	if len(result.Services) != 4 {
		t.Errorf("Result.Services = %d, want the four services the run did verify", len(result.Services))
	}
	if len(result.Endpoints) != 1 || result.Endpoints[0].Label != "frontend" {
		t.Errorf("Result.Endpoints = %+v, want the one endpoint that did answer", result.Endpoints)
	}
}

// TestDefaultRetryBudgetsCoverTheDocumentedStartPeriod checks the defaults
// against the thing they exist for. The FastAPI backend declares a 60-second
// health start period, so a budget shorter than that would report a stack that
// was merely still starting as broken.
func TestDefaultRetryBudgetsCoverTheDocumentedStartPeriod(t *testing.T) {
	const backendHealthStartPeriod = 60 * time.Second

	var delay time.Duration
	defaults := Acceptance{Sleep: func(waited time.Duration) { delay = waited }}
	if err := defaults.pause(context.Background(), 0, 2); err != nil {
		t.Fatalf("pause() error = %v", err)
	}
	if delay <= 0 {
		t.Fatalf("the default retry delay is %v, want a real wait between attempts", delay)
	}

	if health := time.Duration(defaults.healthTries()-1) * delay; health < backendHealthStartPeriod {
		t.Errorf(
			"the default health budget is %v, want it to outlast the %v backend health start period",
			health,
			backendHealthStartPeriod,
		)
	}
	if probe := time.Duration(defaults.probeTries()-1) * delay; probe < 10*time.Second {
		t.Errorf("the default probe budget is %v, want a healthy stack time to answer through the forward", probe)
	}
}

// TestRunListsTheDeclaredCommandsWhenTheDevCommandIsUnknown makes the rejection
// actionable: the project declares the DEV command under a name of its own
// choosing, so the report has to say which names it did find.
func TestRunListsTheDeclaredCommandsWhenTheDevCommandIsUnknown(t *testing.T) {
	test := newHarness(t)
	test.request.Config.Commands["stack"] = config.Command{
		Args:    append([]string(nil), DevCommandArgs...),
		Compose: true,
	}
	test.request.CommandName = "start"

	_, err := test.run(t)
	if err == nil {
		t.Fatal("Run() error = nil, want the undeclared command reported")
	}
	for _, want := range []string{"dev", "stack"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to list the declared command %q", err, want)
		}
	}
}

// TestRunNamesThePortsEntryThatCannotReachTheEndpoint keeps the fix in the
// report: the entry is matched by its macOS port, so the name is the only thing
// that says which line of the configuration to edit.
func TestRunNamesThePortsEntryThatCannotReachTheEndpoint(t *testing.T) {
	test := newHarness(t)
	test.request.Config.Ports[1] = config.Port{Name: "backend-health", Guest: 8000, Host: 8001}

	_, err := test.run(t)
	if err == nil {
		t.Fatal("Run() error = nil, want the mismatched port reported")
	}
	if !strings.Contains(err.Error(), "ports.backend-health") {
		t.Errorf("error = %q, want it to name the [[ports]] entry to fix", err)
	}
}
