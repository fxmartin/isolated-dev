package projectcmd

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// A real process's status is handed back as-is across the whole range, so a
// command that fails in its own way is not flattened into a generic failure.
func TestExecRunnerPreservesUnusualExitStatuses(t *testing.T) {
	t.Parallel()

	for _, want := range []int{0, 1, 2, 42, 255} {
		exitCode, err := ExecRunner{}.Run(
			context.Background(),
			Streams{},
			"/bin/sh",
			"-c", "exit "+strconv.Itoa(want),
		)
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if exitCode != want {
			t.Errorf("exit code = %d, want %d", exitCode, want)
		}
	}
}

// A command invoked with no streams writes nowhere rather than failing, so an
// caller that does not care about output still gets the exit status.
func TestExecRunnerRunsWithoutStreams(t *testing.T) {
	t.Parallel()

	exitCode, err := ExecRunner{}.Run(
		context.Background(),
		Streams{},
		"/bin/sh",
		"-c", "printf 'out\\n'; printf 'err\\n' >&2; exit 4",
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if exitCode != 4 {
		t.Errorf("exit code = %d, want 4", exitCode)
	}
}

// A cancelled invocation never produces an exit status of its own, so it is
// reported as a failure rather than as a command that quietly succeeded.
func TestExecRunnerReportsACancelledInvocationAsAnError(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	exitCode, err := ExecRunner{}.Run(ctx, Streams{}, "/bin/sh", "-c", "exit 0")
	if err == nil {
		t.Fatal("Run() error = nil, want a cancelled invocation reported as a failure")
	}
	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0 alongside the error", exitCode)
	}
	if !strings.Contains(err.Error(), "/bin/sh") {
		t.Errorf("error = %v, want it to name the process", err)
	}
}
