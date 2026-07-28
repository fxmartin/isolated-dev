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
