package forge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/fxmartin/isolated-dev/internal/projectcmd"
	"github.com/fxmartin/isolated-dev/internal/tunnel"
)

const (
	testMountpoint = "/var/lib/docker/volumes/rosetta-db-dev/_data"
	testCreatedAt  = "2026-07-27T09:15:04+02:00"
)

// lifecycleStub stands in for the CLI's own `stop` and `up`. It records the
// order of the calls, because a persistence run that probed the ports before it
// stopped the machine would prove nothing.
type lifecycleStub struct {
	calls   []string
	stopErr error
	upErr   error
	// state is shared with the volume and port stubs, which answer differently
	// depending on whether the machine is running.
	state *machineState
	// onUp runs while the machine comes back, so a test can change what the
	// restarted machine reports.
	onUp func()
	// onStop runs once the machine is down, which is the moment the macOS ports
	// start being watched for their closure.
	onStop func()
	// upElapsed advances the fake clock, so the cached machine readiness target is
	// measured rather than assumed.
	upElapsed time.Duration
	clock     *fakeClock
}

func (stub *lifecycleStub) Stop(_ context.Context, projectPath string) error {
	stub.calls = append(stub.calls, "stop "+projectPath)
	if stub.stopErr != nil {
		return stub.stopErr
	}
	stub.state.running = false
	if stub.onStop != nil {
		stub.onStop()
	}
	return nil
}

func (stub *lifecycleStub) Up(_ context.Context, projectPath string, _ io.Writer) error {
	stub.calls = append(stub.calls, "up "+projectPath)
	if stub.upErr != nil {
		return stub.upErr
	}
	if stub.clock != nil {
		stub.clock.advance(stub.upElapsed)
	}
	stub.state.running = true
	if stub.onUp != nil {
		stub.onUp()
	}
	return nil
}

// machineState is what the stubbed machine currently is. The tunnel, the macOS
// prober, and the guest commands all read it, so a stopped machine behaves like
// one everywhere at once.
type machineState struct {
	running bool
	volumes map[string]volumeFixture
}

type volumeFixture struct {
	driver     string
	mountpoint string
	createdAt  string
	entries    []string
}

type fakeClock struct {
	now time.Time
}

func (clock *fakeClock) advance(elapsed time.Duration) {
	clock.now = clock.now.Add(elapsed)
}

// stateProber answers only while the machine is running, which is what makes
// "reachable after the CLI exits" and "unreachable after stop" separable.
type stateProber struct {
	state  *machineState
	bodies map[string]string
	urls   []string
	// answerWhenStopped keeps the endpoint answering after `stop`, which is the
	// failure a persistence run has to catch.
	answerWhenStopped bool
	// stoppedErr replaces the refused connection a released socket answers with,
	// which is how a probe that fails for a reason saying nothing about the
	// socket — an error status, a read that never completed — is simulated.
	stoppedErr error
}

func (prober *stateProber) Get(ctx context.Context, url string) (string, error) {
	prober.urls = append(prober.urls, url)
	// The real prober carries the caller's context into the request, so a run
	// that has been given up on fails here rather than answering.
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("request %s: %w", url, err)
	}
	if !prober.state.running && !prober.answerWhenStopped {
		if prober.stoppedErr != nil {
			return "", prober.stoppedErr
		}
		return "", fmt.Errorf("request %s: %w", url, syscall.ECONNREFUSED)
	}
	if body, ok := prober.bodies[url]; ok {
		return body, nil
	}
	return "ok", nil
}

// stateTunnel reports the managed forwards only while the machine runs, exactly
// as `stop` leaves them.
type stateTunnel struct {
	state *machineState
	// runningWhenStopped keeps the record claiming a live tunnel after `stop`.
	runningWhenStopped bool
	// errWhenStopped fails only the inspection that follows `stop`.
	errWhenStopped error
}

func (stub *stateTunnel) Inspect(string) (tunnel.State, error) {
	if stub.state.running {
		return forwardedTunnel(), nil
	}
	if stub.errWhenStopped != nil {
		return tunnel.State{}, stub.errWhenStopped
	}
	if stub.runningWhenStopped {
		return forwardedTunnel(), nil
	}
	return tunnel.State{}, nil
}

type persistenceHarness struct {
	persistence *Persistence
	lifecycle   *lifecycleStub
	runner      *runnerStub
	executor    *executorStub
	prober      *stateProber
	state       *machineState
	clock       *fakeClock
	diagnostics *bytes.Buffer
	progress    *bytes.Buffer
	request     PersistenceRequest
	projectDir  string
	// stackElapsed is how long the restarted DEV stack takes to become healthy.
	stackElapsed time.Duration
}

func newPersistenceHarness(t *testing.T) *persistenceHarness {
	t.Helper()

	projectDir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(projectDir, ComposeFileName),
		[]byte(composeFileContent),
		0o644,
	); err != nil {
		t.Fatalf("WriteFile(compose) error = %v", err)
	}

	state := &machineState{running: true, volumes: map[string]volumeFixture{
		"rosetta-db-dev": {
			driver:     "local",
			mountpoint: testMountpoint,
			createdAt:  testCreatedAt,
			entries:    []string{"PG_VERSION", "base", "global", "pg_wal"},
		},
		"rosetta-data-dev": {
			driver:     "local",
			mountpoint: "/var/lib/docker/volumes/rosetta-data-dev/_data",
			createdAt:  testCreatedAt,
			entries:    []string{"exports", "uploads"},
		},
	}}
	clock := &fakeClock{now: time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)}
	test := &persistenceHarness{
		state:        state,
		clock:        clock,
		projectDir:   projectDir,
		diagnostics:  &bytes.Buffer{},
		progress:     &bytes.Buffer{},
		executor:     &executorStub{},
		stackElapsed: 45 * time.Second,
	}
	test.runner = &runnerStub{respond: test.respond}
	test.prober = &stateProber{state: state, bodies: map[string]string{
		"http://127.0.0.1:3001/":       "<!doctype html><title>ROSETTA</title>",
		"http://127.0.0.1:8001/health": `{"status":"ok"}`,
	}}
	test.lifecycle = &lifecycleStub{state: state, clock: clock, upElapsed: 12 * time.Second}
	// The restarted DEV stack becomes healthy while its declared command runs,
	// which is the interval the second readiness target measures.
	test.executor.respond = func(projectcmd.Streams) (int, error) {
		clock.advance(test.stackElapsed)
		return 0, nil
	}

	test.persistence = &Persistence{
		Acceptance: Acceptance{
			Runner:      test.runner,
			Commands:    test.executor,
			Tunnels:     &stateTunnel{state: state},
			Prober:      test.prober,
			HealthTries: 3,
			ProbeTries:  3,
			RetryDelay:  time.Millisecond,
			Sleep:       func(time.Duration) {},
			Output:      test.progress,
			Diagnostics: test.diagnostics,
		},
		Lifecycle:    test.lifecycle,
		Now:          func() time.Time { return clock.now },
		ClosureTries: 3,
	}
	test.request = PersistenceRequest{
		Request: Request{
			ProjectPath:      projectDir,
			MachineName:      "isolated-dev-forge-abcd1234",
			GuestUser:        "fx",
			GuestProjectPath: projectDir,
			CommandName:      "dev",
			Config:           forgeConfig(),
		},
		// The identity is what `up` records for the provisioned guest account, not
		// whoever runs the tests: a root test runner would otherwise inject 0:0,
		// which validation rightly refuses. The guest side is stubbed, so the
		// fixed value round-trips unchanged.
		GuestUID: 1000,
		GuestGID: 1000,
	}
	return test
}

// respond answers every guest command a persistence run issues: the container
// states, the named volumes, and the two sides of the mounted-edit round trip.
func (test *persistenceHarness) respond(call recordedCall) ([]byte, error) {
	line := call.line()
	switch {
	case strings.Contains(line, "docker inspect"):
		return healthyStack(runningStates())(call)
	case strings.Contains(line, "docker volume inspect"):
		return test.volumeInspect(call)
	case strings.Contains(line, " ls -A "):
		return test.volumeEntries(call)
	case strings.Contains(line, " cat "):
		return test.guestRead(call)
	case strings.Contains(line, " cp "):
		return test.guestCopy(call)
	case strings.Contains(line, " stat "):
		return []byte(fmt.Sprintf("%d:%d\n", test.request.GuestUID, test.request.GuestGID)), nil
	}
	return nil, nil
}

func (test *persistenceHarness) volumeInspect(call recordedCall) ([]byte, error) {
	var reported []string
	for _, name := range volumeNames(call.args) {
		fixture, ok := test.state.volumes[name]
		if !ok {
			return nil, fmt.Errorf("Error: No such volume: %s", name)
		}
		reported = append(reported, fmt.Sprintf(
			"%s %s %s", fixture.driver, fixture.mountpoint, fixture.createdAt,
		))
	}
	return []byte(strings.Join(reported, "\n") + "\n"), nil
}

func (test *persistenceHarness) volumeEntries(call recordedCall) ([]byte, error) {
	mountpoint := call.args[len(call.args)-1]
	for _, fixture := range test.state.volumes {
		if fixture.mountpoint != mountpoint {
			continue
		}
		entries := slices.Clone(fixture.entries)
		slices.Sort(entries)
		return []byte(strings.Join(entries, "\n") + "\n"), nil
	}
	return nil, fmt.Errorf("ls: cannot access %q", mountpoint)
}

// guestRead reads the macOS-authored marker through the simulated mount, which
// in the unit test is the same directory macOS wrote it to.
func (test *persistenceHarness) guestRead(call recordedCall) ([]byte, error) {
	return os.ReadFile(call.args[len(call.args)-1])
}

// guestCopy is what a Linux-created file is: the copy appears on the mount, and
// macOS has to be able to read it back.
func (test *persistenceHarness) guestCopy(call recordedCall) ([]byte, error) {
	source := call.args[len(call.args)-2]
	destination := call.args[len(call.args)-1]
	data, err := os.ReadFile(source)
	if err != nil {
		return nil, err
	}
	return nil, os.WriteFile(destination, data, 0o644)
}

// volumeNames extracts the volume arguments that follow the inspect format.
func volumeNames(args []string) []string {
	for index, arg := range args {
		if arg == "--format" && index+2 <= len(args) {
			return args[index+2:]
		}
	}
	return nil
}

func (test *persistenceHarness) validate(t *testing.T) (PersistenceReport, error) {
	t.Helper()

	return test.persistence.Validate(context.Background(), test.request)
}

func TestPersistenceValidateProvesForgeSurvivesARestart(t *testing.T) {
	test := newPersistenceHarness(t)

	report, err := test.validate(t)
	if err != nil {
		t.Fatalf("Validate() error = %v\ndiagnostics:\n%s", err, test.diagnostics)
	}

	if len(report.Volumes) != len(DevVolumes()) {
		t.Fatalf("Volumes = %d, want %d", len(report.Volumes), len(DevVolumes()))
	}
	for _, volume := range report.Volumes {
		if !volume.Preserved {
			t.Errorf("%s was not preserved: %s", volume.Name, volume.Difference)
		}
	}
	// The machine was stopped before the ports were called closed, and restarted
	// before they were called reachable again.
	want := []string{"stop " + test.projectDir, "up " + test.projectDir}
	if got := test.lifecycle.calls; !slices.Equal(got, want) {
		t.Errorf("lifecycle calls = %v, want %v", got, want)
	}
	if len(report.Ports.BeforeStop) != len(DevEndpoints()) {
		t.Errorf("BeforeStop = %v, want both endpoints", report.Ports.BeforeStop)
	}
	if len(report.Ports.ClosedAfterStop) != len(DevEndpoints()) {
		t.Errorf("ClosedAfterStop = %v, want both endpoints", report.Ports.ClosedAfterStop)
	}
	if len(report.Ports.AfterRestart) != len(DevEndpoints()) {
		t.Errorf("AfterRestart = %v, want both endpoints", report.Ports.AfterRestart)
	}
	if !report.Edit.HostEditRead || !report.Edit.GuestFileRead {
		t.Errorf("Edit = %+v, want both directions of the mount proven", report.Edit)
	}
	if !report.Edit.OwnershipMatched {
		t.Errorf("Edit ownership = guest %d:%d, macOS %d:%d, want a match",
			report.Edit.GuestUID, report.Edit.GuestGID, report.Edit.HostUID, report.Edit.HostGID)
	}
}

// The marker files are the only things the run writes into a real repository,
// so they may not outlive it.
func TestPersistenceValidateRemovesItsMarkers(t *testing.T) {
	test := newPersistenceHarness(t)

	if _, err := test.validate(t); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	entries, err := os.ReadDir(test.projectDir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, entry := range entries {
		if entry.Name() != ComposeFileName {
			t.Errorf("the persistence run left %s in the project", entry.Name())
		}
	}
}

func TestPersistenceValidateRemovesItsMarkersWhenTheRunFails(t *testing.T) {
	test := newPersistenceHarness(t)
	test.lifecycle.stopErr = errors.New("machine is busy")

	if _, err := test.validate(t); err == nil {
		t.Fatal("Validate() error = nil, want the stop failure")
	}
	if _, err := os.Stat(filepath.Join(test.projectDir, DefaultMarkerName)); !os.IsNotExist(err) {
		t.Errorf("Stat(marker) error = %v, want it removed", err)
	}
}

func TestPersistenceValidateRefusesToOverwriteAnExistingFile(t *testing.T) {
	test := newPersistenceHarness(t)
	marker := filepath.Join(test.projectDir, DefaultMarkerName)
	if err := os.WriteFile(marker, []byte("real content\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := test.validate(t)
	if err == nil || !strings.Contains(err.Error(), DefaultMarkerName) {
		t.Fatalf("Validate() error = %v, want it to name the existing %s", err, DefaultMarkerName)
	}
	data, readErr := os.ReadFile(marker)
	if readErr != nil || string(data) != "real content\n" {
		t.Errorf("marker = %q (%v), want the existing file untouched", data, readErr)
	}
	if len(test.lifecycle.calls) != 0 {
		t.Errorf("lifecycle calls = %v, want the machine untouched", test.lifecycle.calls)
	}
}

// The guest copy is created by `cp`, which overwrites rather than refuses, and
// is then removed with the rest of the run — so a name collision there would
// destroy a repository file in the way the primary marker never can.
func TestPersistenceValidateRefusesToOverwriteAnExistingGuestCopy(t *testing.T) {
	test := newPersistenceHarness(t)
	guestCopy := filepath.Join(test.projectDir, DefaultMarkerName+guestCopySuffix)
	if err := os.WriteFile(guestCopy, []byte("real content\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := test.validate(t)
	if err == nil || !strings.Contains(err.Error(), DefaultMarkerName+guestCopySuffix) {
		t.Fatalf("Validate() error = %v, want it to name the existing guest copy", err)
	}
	data, readErr := os.ReadFile(guestCopy)
	if readErr != nil || string(data) != "real content\n" {
		t.Errorf("guest copy = %q (%v), want the existing file untouched", data, readErr)
	}
	// The refusal comes before anything is written, so the primary marker never
	// reaches the project either.
	if _, err := os.Stat(filepath.Join(test.projectDir, DefaultMarkerName)); !os.IsNotExist(err) {
		t.Errorf("Stat(marker) error = %v, want nothing written", err)
	}
	if len(test.lifecycle.calls) != 0 {
		t.Errorf("lifecycle calls = %v, want the machine untouched", test.lifecycle.calls)
	}
}

func TestPersistenceValidateRejectsARecreatedVolume(t *testing.T) {
	test := newPersistenceHarness(t)
	test.lifecycle.onUp = func() {
		fixture := test.state.volumes["rosetta-db-dev"]
		fixture.createdAt = "2026-07-28T11:00:00+02:00"
		test.state.volumes["rosetta-db-dev"] = fixture
	}

	report, err := test.validate(t)
	if err == nil {
		t.Fatal("Validate() error = nil, want the recreated volume reported")
	}
	if !strings.Contains(err.Error(), "rosetta-db-dev") {
		t.Errorf("Validate() error = %v, want it to name the volume", err)
	}
	// The evidence survives the failure, which is what makes it reviewable.
	if len(report.Volumes) == 0 {
		t.Error("Volumes = none, want the captured identities kept in the report")
	}
}

func TestPersistenceValidateRejectsLostVolumeData(t *testing.T) {
	test := newPersistenceHarness(t)
	test.lifecycle.onUp = func() {
		fixture := test.state.volumes["rosetta-data-dev"]
		fixture.entries = []string{"exports"}
		test.state.volumes["rosetta-data-dev"] = fixture
	}

	_, err := test.validate(t)
	if err == nil || !strings.Contains(err.Error(), "uploads") {
		t.Fatalf("Validate() error = %v, want the lost entry named", err)
	}
}

// A volume that gained files while the stack ran is still preserved: only
// losing what was there is a failure.
func TestPersistenceValidateAcceptsVolumeGrowth(t *testing.T) {
	test := newPersistenceHarness(t)
	test.lifecycle.onUp = func() {
		fixture := test.state.volumes["rosetta-db-dev"]
		fixture.entries = append(fixture.entries, "pg_stat_tmp")
		test.state.volumes["rosetta-db-dev"] = fixture
	}

	if _, err := test.validate(t); err != nil {
		t.Fatalf("Validate() error = %v, want growth accepted", err)
	}
}

func TestPersistenceValidateReportsPortsThatStayOpenAfterStop(t *testing.T) {
	test := newPersistenceHarness(t)
	test.prober.answerWhenStopped = true

	_, err := test.validate(t)
	if err == nil || !strings.Contains(err.Error(), "3001") {
		t.Fatalf("Validate() error = %v, want the still-answering macOS port named", err)
	}
	// The failure happens after `stop`, so the developer's stack is down and the
	// error is the only thing that can say how to bring it back.
	if !strings.Contains(err.Error(), "isolated-dev up "+test.projectDir) {
		t.Errorf("Validate() error = %v, want it to name the `up` that restores the stack", err)
	}
}

// A port that answers with an error status is still bound: something is holding
// the socket a stopped machine was supposed to release. The probe fails either
// way, so only a refused connection may count as proof.
func TestPersistenceValidateRejectsAPortThatAnswersWithAnErrorStatusAfterStop(t *testing.T) {
	test := newPersistenceHarness(t)
	test.prober.stoppedErr = errors.New("http://127.0.0.1:3001/ returned HTTP 503")

	report, err := test.validate(t)
	if err == nil || !strings.Contains(err.Error(), "3001") {
		t.Fatalf("Validate() error = %v, want the still-bound macOS port named", err)
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("Validate() error = %v, want it to quote what the port answered", err)
	}
	if len(report.Ports.ClosedAfterStop) != 0 {
		t.Errorf("ClosedAfterStop = %v, want no endpoint recorded as closed",
			report.Ports.ClosedAfterStop)
	}
}

// A run that is given up on while it waits cannot claim the ports closed: every
// probe after the cancellation fails for a reason that says nothing about the
// socket.
func TestPersistenceValidateDoesNotRecordClosureWhenTheRunIsCancelled(t *testing.T) {
	test := newPersistenceHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	test.lifecycle.onStop = cancel

	report, err := test.persistence.Validate(ctx, test.request)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Validate() error = %v, want the cancellation", err)
	}
	if len(report.Ports.ClosedAfterStop) != 0 {
		t.Errorf("ClosedAfterStop = %v, want no endpoint recorded as closed",
			report.Ports.ClosedAfterStop)
	}
}

// The guest copy is made inside the machine, so macOS can only remove it while
// the mount really is shared — which is the very thing the round trip may be
// about to disprove. It is removed from the guest side too.
func TestPersistenceValidateRemovesTheGuestCopyFromInsideTheMachine(t *testing.T) {
	test := newPersistenceHarness(t)
	// `cp` succeeds in the guest and macOS never sees the file, which is the
	// unshared mount the round trip exists to catch.
	test.interceptGuest(" cp ", func(recordedCall) ([]byte, error) { return nil, nil })

	if _, err := test.validate(t); err == nil {
		t.Fatal("Validate() error = nil, want the invisible guest file reported")
	}
	guestCopy := filepath.Join(test.request.GuestProjectPath, DefaultMarkerName+guestCopySuffix)
	removal := " rm -f " + guestCopy
	if !slices.ContainsFunc(test.runner.lines(), func(line string) bool {
		return strings.Contains(line, removal)
	}) {
		t.Errorf("guest commands = %v, want one removing %s inside the machine",
			test.runner.lines(), guestCopy)
	}
}

func TestPersistenceValidateReportsATunnelThatSurvivesStop(t *testing.T) {
	test := newPersistenceHarness(t)
	test.persistence.Tunnels = &stateTunnel{state: test.state, runningWhenStopped: true}

	_, err := test.validate(t)
	if err == nil || !strings.Contains(err.Error(), "tunnel") {
		t.Fatalf("Validate() error = %v, want the surviving tunnel reported", err)
	}
}

func TestPersistenceValidateReportsAnUnreachablePortBeforeStop(t *testing.T) {
	test := newPersistenceHarness(t)
	test.state.running = true
	test.prober.bodies = map[string]string{}
	test.persistence.Prober = &stateProber{state: &machineState{}}

	_, err := test.validate(t)
	if err == nil {
		t.Fatal("Validate() error = nil, want the unreachable endpoint reported")
	}
	if len(test.lifecycle.calls) != 0 {
		t.Errorf("lifecycle calls = %v, want the machine left alone", test.lifecycle.calls)
	}
}

func TestPersistenceValidateReportsAGuestThatCannotReadAMacOSEdit(t *testing.T) {
	test := newPersistenceHarness(t)
	previous := test.runner.respond
	test.runner.respond = func(call recordedCall) ([]byte, error) {
		if strings.Contains(call.line(), " cat ") {
			return []byte("stale content\n"), nil
		}
		return previous(call)
	}

	_, err := test.validate(t)
	if err == nil || !strings.Contains(err.Error(), "read") {
		t.Fatalf("Validate() error = %v, want the unreadable edit reported", err)
	}
}

func TestPersistenceValidateReportsGuestOwnershipThatDoesNotMatch(t *testing.T) {
	test := newPersistenceHarness(t)
	previous := test.runner.respond
	test.runner.respond = func(call recordedCall) ([]byte, error) {
		if strings.Contains(call.line(), " stat ") {
			return []byte("0:0\n"), nil
		}
		return previous(call)
	}

	_, err := test.validate(t)
	if err == nil || !strings.Contains(err.Error(), "ownership") {
		t.Fatalf("Validate() error = %v, want the ownership mismatch reported", err)
	}
}

func TestPersistenceValidateMeasuresCachedReadinessTargets(t *testing.T) {
	test := newPersistenceHarness(t)

	report, err := test.validate(t)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if len(report.Timings) != 2 {
		t.Fatalf("Timings = %+v, want the machine and stack measurements", report.Timings)
	}
	if got := report.Timings[0].Elapsed; got != 12*time.Second {
		t.Errorf("machine timing = %s, want 12s", got)
	}
	if got := report.Timings[0].Target; got != MachineReadyTarget {
		t.Errorf("machine target = %s, want %s", got, MachineReadyTarget)
	}
	if got := report.Timings[1].Elapsed; got != 45*time.Second {
		t.Errorf("stack timing = %s, want 45s", got)
	}
	if got := report.Timings[1].Target; got != StackReadyTarget {
		t.Errorf("stack target = %s, want %s", got, StackReadyTarget)
	}
	if missed := report.MissedTargets(); len(missed) != 0 {
		t.Errorf("MissedTargets() = %+v, want none", missed)
	}
}

// A slow restart is a finding, not a broken environment: the report says so and
// the run still succeeds.
func TestPersistenceValidateReportsAMissedTargetWithoutFailing(t *testing.T) {
	test := newPersistenceHarness(t)
	test.lifecycle.upElapsed = 90 * time.Second

	report, err := test.validate(t)
	if err != nil {
		t.Fatalf("Validate() error = %v, want a missed target reported rather than failed", err)
	}
	missed := report.MissedTargets()
	if len(missed) != 1 || missed[0].Target != MachineReadyTarget {
		t.Fatalf("MissedTargets() = %+v, want the machine target", missed)
	}
	if !strings.Contains(missed[0].String(), "1m30s") {
		t.Errorf("Timing.String() = %q, want the elapsed time", missed[0].String())
	}
}

func TestPersistenceValidateRequiresALifecycle(t *testing.T) {
	test := newPersistenceHarness(t)
	test.persistence.Lifecycle = nil

	_, err := test.validate(t)
	if err == nil || !strings.Contains(err.Error(), "lifecycle") {
		t.Fatalf("Validate() error = %v, want the missing lifecycle reported", err)
	}
}

func TestPersistenceValidateRequiresARecordedGuestIdentity(t *testing.T) {
	test := newPersistenceHarness(t)
	test.request.GuestUID = 0

	_, err := test.validate(t)
	if err == nil || !strings.Contains(err.Error(), "guest") {
		t.Fatalf("Validate() error = %v, want the missing guest identity reported", err)
	}
}

func TestPersistenceValidateRejectsAMarkerOutsideTheProject(t *testing.T) {
	test := newPersistenceHarness(t)
	test.request.MarkerName = "../escaped"

	_, err := test.validate(t)
	if err == nil || !strings.Contains(err.Error(), "marker") {
		t.Fatalf("Validate() error = %v, want the escaping marker rejected", err)
	}
}

func TestPersistenceValidateReportsAFailedRestart(t *testing.T) {
	test := newPersistenceHarness(t)
	test.lifecycle.upErr = errors.New("machine did not start")

	_, err := test.validate(t)
	if err == nil || !strings.Contains(err.Error(), "machine did not start") {
		t.Fatalf("Validate() error = %v, want the restart failure reported", err)
	}
	// A failed restart is exactly when the guest state has to be captured.
	if test.diagnostics.Len() == 0 {
		t.Error("diagnostics = empty, want the failure explained")
	}
	// The machine is stopped and stays stopped, which the error has to say.
	if !strings.Contains(err.Error(), "isolated-dev up "+test.projectDir) {
		t.Errorf("Validate() error = %v, want it to name the `up` that restores the stack", err)
	}
}
