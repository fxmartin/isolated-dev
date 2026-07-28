package smoke

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fxmartin/isolated-dev/internal/machine"
)

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

func (runner *runnerStub) index(fragment string) int {
	for position, call := range runner.calls {
		if strings.Contains(call.line(), fragment) {
			return position
		}
	}
	return -1
}

type machineStub struct {
	upRequests []machine.Request
	upErr      error
	destroyed  []machine.Target
	destroyErr error
}

func (stub *machineStub) Up(_ context.Context, request machine.Request) (machine.UpResult, error) {
	stub.upRequests = append(stub.upRequests, request)
	if stub.upErr != nil {
		return machine.UpResult{}, stub.upErr
	}
	return machine.UpResult{Created: true}, nil
}

func (stub *machineStub) Destroy(_ context.Context, target machine.Target) error {
	stub.destroyed = append(stub.destroyed, target)
	return stub.destroyErr
}

type imageStub struct {
	references []string
	err        error
}

func (stub *imageStub) EnsureReference(_ context.Context, reference string) error {
	stub.references = append(stub.references, reference)
	return stub.err
}

type dockerStub struct {
	machines []string
	err      error
}

func (stub *dockerStub) WaitDocker(_ context.Context, machineName string) error {
	stub.machines = append(stub.machines, machineName)
	return stub.err
}

type proberStub struct {
	urls []string
	body string
	err  error
}

func (stub *proberStub) Get(_ context.Context, url string) (string, error) {
	stub.urls = append(stub.urls, url)
	return stub.body, stub.err
}

type addressStub struct {
	address string
	err     error
}

func (stub *addressStub) Address(_ context.Context, _ machine.Target) (string, error) {
	return stub.address, stub.err
}

// baselineRunner answers every guest command the successful baseline issues.
func baselineRunner(marker string) *runnerStub {
	return &runnerStub{respond: func(call recordedCall) ([]byte, error) {
		line := call.line()
		switch {
		case strings.Contains(line, "docker network inspect") &&
			strings.Contains(line, "{{.Driver}}"):
			return []byte("bridge\n"), nil
		case strings.Contains(line, "docker network inspect") &&
			strings.Contains(line, "{{len .Containers}}"):
			return []byte("2\n"), nil
		case strings.Contains(line, "docker inspect"):
			return []byte(OriginImage + " true\n" + ProxyImage + " true\n"), nil
		case strings.Contains(line, "curl"):
			return []byte(marker + "\n"), nil
		default:
			return nil, nil
		}
	}}
}

type harness struct {
	test    Test
	runner  *runnerStub
	machine *machineStub
	image   *imageStub
	docker  *dockerStub
	prober  *proberStub
	logs    *bytes.Buffer
	request Request
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	const marker = "baseline-marker-0f0f"
	home := t.TempDir()
	runner := baselineRunner(marker)
	machines := &machineStub{}
	image := &imageStub{}
	docker := &dockerStub{}
	prober := &proberStub{body: marker + "\n"}
	logs := &bytes.Buffer{}

	return &harness{
		test: Test{
			Runner:       runner,
			Machines:     machines,
			ImageEnsurer: image,
			DockerWaiter: docker,
			Address:      &addressStub{address: "192.168.64.7"},
			Prober:       prober,
			ProbeTries:   2,
			Sleep:        func(time.Duration) {},
			Diagnostics:  logs,
		},
		runner:  runner,
		machine: machines,
		image:   image,
		docker:  docker,
		prober:  prober,
		logs:    logs,
		request: Request{
			MachineName:      "isolated-dev-baseline-0f0f",
			BaseImageVersion: "baseline-0f0f",
			HomeDir:          home,
			FixtureDir:       filepath.Join(home, "baseline-0f0f"),
			GuestUser:        "fx",
			CPUs:             2,
			MemoryGB:         4,
			HostPort:         18080,
			Marker:           marker,
		},
	}
}

func TestRunProvesTheBaselineAndRemovesEverythingItCreated(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	result, err := harness.test.Run(context.Background(), harness.request)
	if err != nil {
		t.Fatalf("Run() error = %v\n%s", err, strings.Join(harness.runner.lines(), "\n"))
	}

	if result.BaseImage != "local/isolated-dev-base:baseline-0f0f" {
		t.Errorf("BaseImage = %q", result.BaseImage)
	}
	if result.GuestMarker != harness.request.Marker || result.HostMarker != harness.request.Marker {
		t.Errorf(
			"markers = %q/%q, want the marker returned from both the guest and macOS",
			result.GuestMarker,
			result.HostMarker,
		)
	}
	if result.HostURL != "http://192.168.64.7:18080/"+MarkerFileName {
		t.Errorf("HostURL = %q", result.HostURL)
	}

	// The two pinned images run on the private Compose network, started from
	// the mounted fixture directory.
	composeUp := harness.runner.ran(t, "docker compose")
	if !strings.Contains(composeUp.line(), "up --detach --wait") {
		t.Errorf("compose was not started with a readiness wait: %s", composeUp.line())
	}
	if !strings.Contains(composeUp.line(), harness.request.FixtureDir) {
		t.Errorf("compose did not run from the mounted fixture directory: %s", composeUp.line())
	}
	harness.runner.ran(t, "docker network inspect isolated-dev-baseline-0f0f_"+NetworkName)
	harness.runner.ran(t, "isolated-dev-baseline-0f0f-origin-1")

	// Docker readiness is confirmed again immediately before Compose, which is
	// where the Apple Container 1.1.0 startup race would otherwise surface.
	if len(harness.docker.machines) != 1 {
		t.Errorf("WaitDocker calls = %d, want one before Compose", len(harness.docker.machines))
	}

	// Teardown removes exactly what the baseline created.
	if index := harness.runner.index("docker compose"); index >= harness.runner.index("down --remove-orphans") {
		t.Error("Compose was not torn down after it was started")
	}
	harness.runner.ran(t, "image delete local/isolated-dev-base:baseline-0f0f")
	if len(harness.machine.destroyed) != 1 ||
		harness.machine.destroyed[0].MachineName != harness.request.MachineName {
		t.Errorf("destroyed = %+v, want the baseline machine", harness.machine.destroyed)
	}
	if _, err := os.Stat(harness.request.FixtureDir); !os.IsNotExist(err) {
		t.Errorf("Stat(fixtures) error = %v, want the temporary fixtures removed", err)
	}
	if harness.logs.Len() != 0 {
		t.Errorf("diagnostics were captured for a passing run:\n%s", harness.logs.String())
	}
}

func TestRunFailsAndDiagnosesWhenDockerNeverBecomesReady(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.docker.err = errors.New("docker info did not succeed after the fallback")

	if _, err := harness.test.Run(context.Background(), harness.request); err == nil {
		t.Fatal("Run() error = nil, want the unreadable Docker daemon reported")
	}

	diagnostics := harness.logs.String()
	for _, want := range []string{"machine list", "systemctl is-system-running", "docker info"} {
		if !strings.Contains(diagnostics, want) {
			t.Errorf("diagnostics do not report %q:\n%s", want, diagnostics)
		}
	}
	// Compose never started, so nothing claims to tear it down, but the machine,
	// image, and fixtures the run did create are still removed.
	if harness.runner.index("docker compose") != -1 {
		t.Errorf("Compose ran despite an unready Docker daemon:\n%s", strings.Join(harness.runner.lines(), "\n"))
	}
	if len(harness.machine.destroyed) != 1 {
		t.Errorf("destroyed = %+v, want the baseline machine removed anyway", harness.machine.destroyed)
	}
	harness.runner.ran(t, "image delete local/isolated-dev-base:baseline-0f0f")
	if _, err := os.Stat(harness.request.FixtureDir); !os.IsNotExist(err) {
		t.Errorf("Stat(fixtures) error = %v, want the temporary fixtures removed", err)
	}
}

func TestRunFailsWhenTheMarkerDoesNotSurviveTheProxy(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.prober.body = "welcome to nginx"

	_, err := harness.test.Run(context.Background(), harness.request)
	if err == nil {
		t.Fatal("Run() error = nil, want the missing marker reported")
	}
	if !strings.Contains(err.Error(), harness.request.Marker) {
		t.Errorf("error = %v, want it to name the expected marker", err)
	}
	if harness.logs.Len() == 0 {
		t.Error("no diagnostics were captured for a failing probe")
	}
	if len(harness.machine.destroyed) != 1 {
		t.Errorf("destroyed = %+v, want teardown to run after a failed probe", harness.machine.destroyed)
	}
}

func TestRunFailsWhenComposeStartsAnUnpinnedImage(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	inner := harness.runner.respond
	harness.runner.respond = func(call recordedCall) ([]byte, error) {
		if strings.Contains(call.line(), "docker inspect") {
			return []byte("busybox:latest true\n" + ProxyImage + " true\n"), nil
		}
		return inner(call)
	}

	_, err := harness.test.Run(context.Background(), harness.request)
	if err == nil {
		t.Fatal("Run() error = nil, want the unpinned image reported")
	}
	if !strings.Contains(err.Error(), OriginImage) {
		t.Errorf("error = %v, want it to name the pinned image", err)
	}
}

func TestRunFailsWhenTheServicesShareTheHostNetwork(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	inner := harness.runner.respond
	harness.runner.respond = func(call recordedCall) ([]byte, error) {
		if strings.Contains(call.line(), "{{.Driver}}") {
			return []byte("host\n"), nil
		}
		return inner(call)
	}

	_, err := harness.test.Run(context.Background(), harness.request)
	if err == nil {
		t.Fatal("Run() error = nil, want the non-private network reported")
	}
	if !strings.Contains(err.Error(), "bridge") {
		t.Errorf("error = %v, want it to name the expected driver", err)
	}
}

func TestRunRefusesToBuildOnTheSharedBaseImageVersion(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.request.BaseImageVersion = "1"

	_, err := harness.test.Run(context.Background(), harness.request)
	if err == nil {
		t.Fatal("Run() error = nil, want the shared base image protected")
	}
	if len(harness.image.references) != 0 || len(harness.machine.upRequests) != 0 {
		t.Error("Run() mutated host state before rejecting the shared base-image version")
	}
	if harness.runner.index("image delete") != -1 {
		t.Error("Run() attempted to delete the shared base image")
	}
}

func TestRunRejectsAFixtureOutsideTheHomeMount(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.request.FixtureDir = filepath.Join(t.TempDir(), "outside")

	_, err := harness.test.Run(context.Background(), harness.request)
	if err == nil {
		t.Fatal("Run() error = nil, want a fixture outside the home mount rejected")
	}
	if len(harness.machine.upRequests) != 0 {
		t.Error("Run() created a machine for an unmountable fixture directory")
	}
}

func TestRunLocatesTheFixtureThroughTheGuestHomeMount(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	inner := harness.runner.respond
	// Apple Container Machine 1.1.0 does not document the guest path of the
	// macOS home mount, so the host path may not exist inside the guest.
	harness.runner.respond = func(call recordedCall) ([]byte, error) {
		line := call.line()
		if strings.Contains(line, "test -f "+harness.request.FixtureDir) {
			return nil, errors.New("No such file or directory")
		}
		return inner(call)
	}

	result, err := harness.test.Run(context.Background(), harness.request)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.GuestDir != "/home/fx/baseline-0f0f" {
		t.Errorf("GuestDir = %q, want the guest home mount candidate", result.GuestDir)
	}
}

func TestRunReportsAnUnconfiguredDependency(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.test.Prober = nil

	if _, err := harness.test.Run(context.Background(), harness.request); err == nil {
		t.Fatal("Run() error = nil, want the missing prober reported")
	}
}
