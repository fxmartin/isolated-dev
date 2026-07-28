package projectcmd

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/fxmartin/isolated-dev/internal/config"
)

// streamRecordingRunner captures the streams it was handed, so a test can show
// the developer's own handles reach the process rather than a copy of them.
type streamRecordingRunner struct {
	streams Streams
	calls   int
}

func (runner *streamRecordingRunner) Run(
	_ context.Context,
	streams Streams,
	_ string,
	_ ...string,
) (int, error) {
	runner.calls++
	runner.streams = streams
	return 0, nil
}

// Declared arguments are an argv, never a script. Characters that a shell would
// act on reach the guest as ordinary text inside single argument elements,
// because no shell runs on either side of the machine boundary.
func TestExecutePassesDeclaredArgumentsAsLiteralArgv(t *testing.T) {
	t.Parallel()

	runner := &runnerStub{}
	declared := []string{
		"npm",
		"run",
		"build; rm -rf /",
		"$(id -u)",
		"`whoami`",
		"a && b || c",
		"*.go",
		"quote\"and'quote",
	}

	if _, err := (Executor{Runner: runner}).Execute(
		context.Background(),
		testRequest(config.Command{Args: declared}),
		Streams{},
	); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	invocation := runner.invocations[0]
	tail := invocation[len(invocation)-len(declared):]
	if !slices.Equal(tail, declared) {
		t.Errorf("trailing argv = %#v,\nwant the declared arguments verbatim %#v", tail, declared)
	}
	// The whole invocation is one flat argv: nothing was joined into a string a
	// shell could later split.
	if slices.Contains(invocation, strings.Join(declared, " ")) {
		t.Errorf("invocation = %#v, want no shell-joined argument string", invocation)
	}
}

// The declared arguments follow the environment assignments `env` consumes, so
// a project cannot reach past its program to reset PATH or HOME for the guest.
func TestExecuteKeepsDeclaredArgumentsAfterTheGuestEnvironment(t *testing.T) {
	t.Parallel()

	runner := &runnerStub{}
	if _, err := (Executor{Runner: runner}).Execute(
		context.Background(),
		testRequest(config.Command{Args: []string{"npm", "test"}}),
		Streams{},
	); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	invocation := runner.invocations[0]
	home := slices.Index(invocation, "HOME=/home/fx")
	path := slices.Index(invocation, "PATH="+guestPath)
	program := slices.Index(invocation, "npm")
	if home < 0 || path < 0 || program < 0 {
		t.Fatalf("invocation = %#v, want the guest environment and the program", invocation)
	}
	if program < home || program < path {
		t.Errorf(
			"invocation = %#v, want the declared program after PATH and HOME",
			invocation,
		)
	}
	// Only one PATH and one HOME are set, so the guest environment is not
	// something a declared argument list can quietly append to.
	if got := strings.Count(strings.Join(invocation, "\x00"), "PATH="); got != 1 {
		t.Errorf("PATH assignments = %d, want exactly 1 in %#v", got, invocation)
	}
}

// A workdir that names the project itself, in any of the ways a project might
// spell it, runs in the mounted project rather than being rejected.
func TestExecuteNormalisesWorkdirsThatNameTheProject(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		workdir string
		want    string
	}{
		{name: "unset", workdir: "", want: "/home/fx/app"},
		{name: "dot", workdir: ".", want: "/home/fx/app"},
		{name: "dot slash", workdir: "./", want: "/home/fx/app"},
		{name: "trailing slash", workdir: "services/", want: "/home/fx/app/services"},
		{name: "dot prefixed", workdir: "./services/api", want: "/home/fx/app/services/api"},
		{name: "redundant descent", workdir: "services/../services", want: "/home/fx/app/services"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			runner := &runnerStub{}
			if _, err := (Executor{Runner: runner}).Execute(
				context.Background(),
				testRequest(config.Command{
					Args:    []string{"npm", "test"},
					Workdir: testCase.workdir,
				}),
				Streams{},
			); err != nil {
				t.Fatalf("Execute() error = %v", err)
			}

			invocation := runner.invocations[0]
			index := slices.Index(invocation, "-C")
			if index < 0 || index+1 >= len(invocation) {
				t.Fatalf("invocation = %#v, want a working directory", invocation)
			}
			if invocation[index+1] != testCase.want {
				t.Errorf("workdir = %q, want %q", invocation[index+1], testCase.want)
			}
		})
	}
}

// Climbing out of the project only after descending into it still leaves the
// project, and is rejected on the resolved path rather than on its spelling.
func TestExecuteRejectsAWorkdirThatEscapesAfterDescending(t *testing.T) {
	t.Parallel()

	for _, workdir := range []string{
		"services/../../etc",
		"services/api/../../../../root",
		"../app-sibling",
	} {
		t.Run(workdir, func(t *testing.T) {
			t.Parallel()

			runner := &runnerStub{}
			_, err := Executor{Runner: runner}.Execute(
				context.Background(),
				testRequest(config.Command{Args: []string{"npm"}, Workdir: workdir}),
				Streams{},
			)
			if err == nil || !strings.Contains(err.Error(), "inside the project") {
				t.Fatalf("Execute() error = %v, want the escaping workdir rejected", err)
			}
			if len(runner.invocations) != 0 {
				t.Errorf("invocations = %#v, want no execution", runner.invocations)
			}
		})
	}
}

// A rejected request costs nothing: the request is checked before the machine
// is touched, so an invalid Compose command never even waits on the daemon.
func TestExecuteRejectsAnInvalidRequestBeforeWaitingForDocker(t *testing.T) {
	t.Parallel()

	runner := &runnerStub{}
	waiter := &waiterStub{}
	executor := Executor{Runner: runner, DockerWaiter: waiter}

	_, err := executor.Execute(
		context.Background(),
		testRequest(config.Command{
			Args:    []string{"docker", "compose", "up"},
			Workdir: "/etc",
			Compose: true,
		}),
		Streams{},
	)
	if err == nil {
		t.Fatal("Execute() error = nil, want the absolute workdir rejected")
	}
	if len(waiter.machines) != 0 {
		t.Errorf("docker waits = %#v, want none for a rejected request", waiter.machines)
	}
	if len(runner.invocations) != 0 {
		t.Errorf("invocations = %#v, want no execution", runner.invocations)
	}
}

// The invoking terminal's own handles are what the command gets, so its input
// and output are live rather than captured and replayed.
func TestExecuteForwardsTheInvokingStreamsUnchanged(t *testing.T) {
	t.Parallel()

	stdin := strings.NewReader("typed\n")
	var stdout, stderr bytes.Buffer
	runner := &streamRecordingRunner{}

	if _, err := (Executor{Runner: runner}).Execute(
		context.Background(),
		testRequest(config.Command{Args: []string{"npm", "test"}}),
		Streams{Stdin: stdin, Stdout: &stdout, Stderr: &stderr},
	); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d, want 1", runner.calls)
	}
	if runner.streams.Stdin != io.Reader(stdin) {
		t.Errorf("stdin = %#v, want the caller's own reader", runner.streams.Stdin)
	}
	if runner.streams.Stdout != io.Writer(&stdout) {
		t.Errorf("stdout = %#v, want the caller's own writer", runner.streams.Stdout)
	}
	if runner.streams.Stderr != io.Writer(&stderr) {
		t.Errorf("stderr = %#v, want the caller's own writer", runner.streams.Stderr)
	}
}

// A guest command's status is its own, whatever it is: the whole range survives
// rather than being flattened to success or a generic failure.
func TestExecutePreservesTheFullRangeOfExitStatuses(t *testing.T) {
	t.Parallel()

	for _, want := range []int{0, 1, 2, 126, 127, 130, 255} {
		runner := &runnerStub{exitCode: want}
		exitCode, err := Executor{Runner: runner}.Execute(
			context.Background(),
			testRequest(config.Command{Args: []string{"npm", "test"}}),
			Streams{},
		)
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if exitCode != want {
			t.Errorf("exit code = %d, want %d", exitCode, want)
		}
	}
}
