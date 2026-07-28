package projectcmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/fxmartin/isolated-dev/internal/config"
)

type runnerStub struct {
	invocations [][]string
	stdout      string
	stderr      string
	exitCode    int
	err         error
}

func (stub *runnerStub) Run(
	_ context.Context,
	streams Streams,
	name string,
	args ...string,
) (int, error) {
	stub.invocations = append(stub.invocations, append([]string{name}, args...))
	if stub.stdout != "" && streams.Stdout != nil {
		if _, err := io.WriteString(streams.Stdout, stub.stdout); err != nil {
			return 0, err
		}
	}
	if stub.stderr != "" && streams.Stderr != nil {
		if _, err := io.WriteString(streams.Stderr, stub.stderr); err != nil {
			return 0, err
		}
	}
	return stub.exitCode, stub.err
}

type waiterStub struct {
	machines []string
	err      error
}

func (stub *waiterStub) WaitDocker(_ context.Context, machineName string) error {
	stub.machines = append(stub.machines, machineName)
	return stub.err
}

func testRequest(command config.Command) Request {
	return Request{
		MachineName:      "isolated-dev-app-abcd1234",
		GuestUser:        "fx",
		GuestProjectPath: "/home/fx/app",
		Name:             "dev",
		Command:          command,
	}
}

func TestExecuteRunsTheDeclaredCommandAsTheGuestUserInTheProject(t *testing.T) {
	t.Parallel()

	runner := &runnerStub{}
	executor := Executor{Runner: runner}

	exitCode, err := executor.Execute(
		context.Background(),
		testRequest(config.Command{Args: []string{"npm", "test"}}),
		Streams{},
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0", exitCode)
	}
	if len(runner.invocations) != 1 {
		t.Fatalf("invocations = %#v, want one", runner.invocations)
	}
	want := []string{
		"container",
		"machine", "run",
		"--name", "isolated-dev-app-abcd1234",
		"--root",
		"--",
		"/usr/sbin/runuser", "-u", "fx", "--",
		"/usr/bin/env", "-C", "/home/fx/app",
		"PATH=" + guestPath,
		"HOME=/home/fx",
		"npm", "test",
	}
	if !slices.Equal(runner.invocations[0], want) {
		t.Errorf("invocation = %#v,\nwant %#v", runner.invocations[0], want)
	}
}

func TestExecuteRunsInTheDeclaredWorkdirBeneathTheProject(t *testing.T) {
	t.Parallel()

	runner := &runnerStub{}
	executor := Executor{Runner: runner}

	if _, err := executor.Execute(
		context.Background(),
		testRequest(config.Command{Args: []string{"npm", "test"}, Workdir: "services/api"}),
		Streams{},
	); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !slices.Contains(runner.invocations[0], "/home/fx/app/services/api") {
		t.Errorf("invocation = %#v, want the declared workdir", runner.invocations[0])
	}
}

func TestExecutePreservesOutputAndExitStatus(t *testing.T) {
	t.Parallel()

	runner := &runnerStub{stdout: "build ok\n", stderr: "warning: slow\n", exitCode: 3}
	executor := Executor{Runner: runner}
	var stdout, stderr bytes.Buffer

	exitCode, err := executor.Execute(
		context.Background(),
		testRequest(config.Command{Args: []string{"npm", "test"}}),
		Streams{Stdout: &stdout, Stderr: &stderr},
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if exitCode != 3 {
		t.Errorf("exit code = %d, want the guest exit status 3", exitCode)
	}
	if stdout.String() != "build ok\n" {
		t.Errorf("stdout = %q, want the guest stdout", stdout.String())
	}
	if stderr.String() != "warning: slow\n" {
		t.Errorf("stderr = %q, want the guest stderr", stderr.String())
	}
}

func TestExecuteWaitsForDockerBeforeAComposeCommand(t *testing.T) {
	t.Parallel()

	runner := &runnerStub{}
	waiter := &waiterStub{}
	executor := Executor{Runner: runner, DockerWaiter: waiter}

	if _, err := executor.Execute(
		context.Background(),
		testRequest(config.Command{
			Args:    []string{"docker", "compose", "up", "-d"},
			Compose: true,
		}),
		Streams{},
	); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(waiter.machines) != 1 || waiter.machines[0] != "isolated-dev-app-abcd1234" {
		t.Errorf("docker waits = %#v, want the project machine", waiter.machines)
	}
	if len(runner.invocations) != 1 {
		t.Errorf("invocations = %#v, want the command to run after the wait", runner.invocations)
	}
}

func TestExecuteReportsDaemonDiagnosticsWhenDockerNeverBecomesReady(t *testing.T) {
	t.Parallel()

	runner := &runnerStub{}
	waiter := &waiterStub{err: errors.New("cannot connect to the Docker daemon")}
	executor := Executor{Runner: runner, DockerWaiter: waiter}

	_, err := executor.Execute(
		context.Background(),
		testRequest(config.Command{
			Args:    []string{"docker", "compose", "up", "-d"},
			Compose: true,
		}),
		Streams{},
	)
	if err == nil {
		t.Fatal("Execute() error = nil, want Docker daemon diagnostics")
	}
	for _, want := range []string{"docker info", "isolated-dev-app-abcd1234", "cannot connect"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %q", err, want)
		}
	}
	if len(runner.invocations) != 0 {
		t.Errorf("invocations = %#v, want no execution without Docker", runner.invocations)
	}
}

func TestExecuteDoesNotWaitForDockerForANonComposeCommand(t *testing.T) {
	t.Parallel()

	waiter := &waiterStub{}
	executor := Executor{Runner: &runnerStub{}, DockerWaiter: waiter}

	if _, err := executor.Execute(
		context.Background(),
		testRequest(config.Command{Args: []string{"npm", "test"}}),
		Streams{},
	); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(waiter.machines) != 0 {
		t.Errorf("docker waits = %#v, want none for a non-Compose command", waiter.machines)
	}
}

func TestExecuteRequiresAConfiguredDockerWaiterForAComposeCommand(t *testing.T) {
	t.Parallel()

	runner := &runnerStub{}
	executor := Executor{Runner: runner}

	_, err := executor.Execute(
		context.Background(),
		testRequest(config.Command{Args: []string{"docker", "compose", "up"}, Compose: true}),
		Streams{},
	)
	if err == nil || !strings.Contains(err.Error(), "Docker readiness") {
		t.Fatalf("Execute() error = %v, want a missing-waiter rejection", err)
	}
	if len(runner.invocations) != 0 {
		t.Errorf("invocations = %#v, want no execution", runner.invocations)
	}
}

func TestExecuteRejectsInvalidRequests(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		mutate  func(*Request)
		wantErr string
	}{
		{
			name:    "unusable machine name",
			mutate:  func(request *Request) { request.MachineName = "../escape" },
			wantErr: "invalid machine name",
		},
		{
			name:    "unprovisioned guest user",
			mutate:  func(request *Request) { request.GuestUser = "" },
			wantErr: "isolated-dev up",
		},
		{
			name:    "root guest user",
			mutate:  func(request *Request) { request.GuestUser = "root" },
			wantErr: "non-root",
		},
		{
			name:    "unusable guest user name",
			mutate:  func(request *Request) { request.GuestUser = "../root" },
			wantErr: "invalid guest user name",
		},
		{
			name:    "unprovisioned project path",
			mutate:  func(request *Request) { request.GuestProjectPath = "" },
			wantErr: "isolated-dev up",
		},
		{
			name:    "relative project path",
			mutate:  func(request *Request) { request.GuestProjectPath = "app" },
			wantErr: "absolute",
		},
		{
			name:    "empty argument list",
			mutate:  func(request *Request) { request.Command.Args = nil },
			wantErr: "commands.dev.args",
		},
		{
			name:    "workdir escaping the project",
			mutate:  func(request *Request) { request.Command.Workdir = "../../etc" },
			wantErr: "inside the project",
		},
		{
			name:    "absolute workdir",
			mutate:  func(request *Request) { request.Command.Workdir = "/etc" },
			wantErr: "project-relative",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			runner := &runnerStub{}
			request := testRequest(config.Command{Args: []string{"npm", "test"}})
			testCase.mutate(&request)

			_, err := Executor{Runner: runner}.Execute(context.Background(), request, Streams{})
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("Execute() error = %v, want it to mention %q", err, testCase.wantErr)
			}
			if len(runner.invocations) != 0 {
				t.Errorf("invocations = %#v, want no execution", runner.invocations)
			}
		})
	}
}

func TestExecuteRequiresAConfiguredRunner(t *testing.T) {
	t.Parallel()

	_, err := Executor{}.Execute(
		context.Background(),
		testRequest(config.Command{Args: []string{"npm", "test"}}),
		Streams{},
	)
	if err == nil || !strings.Contains(err.Error(), "runner is not configured") {
		t.Fatalf("Execute() error = %v, want a missing-runner rejection", err)
	}
}

func TestExecuteReportsAHostFailureSeparatelyFromAGuestExitStatus(t *testing.T) {
	t.Parallel()

	runner := &runnerStub{err: errors.New("container CLI is unavailable")}
	executor := Executor{Runner: runner}

	_, err := executor.Execute(
		context.Background(),
		testRequest(config.Command{Args: []string{"npm", "test"}}),
		Streams{},
	)
	if err == nil || !strings.Contains(err.Error(), "run project command \"dev\"") {
		t.Fatalf("Execute() error = %v, want the named command in the failure", err)
	}
}
