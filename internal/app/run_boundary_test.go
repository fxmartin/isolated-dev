package app

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fxmartin/isolated-dev/internal/cli"
	"github.com/fxmartin/isolated-dev/internal/projectcmd"
)

// recordingRunner stands in for the `container` CLI, so a test can drive the
// real Executor and still see exactly what the host was asked to run.
type recordingRunner struct {
	invocations [][]string
	stdout      string
	exitCode    int
}

func (runner *recordingRunner) Run(
	_ context.Context,
	streams projectcmd.Streams,
	name string,
	args ...string,
) (int, error) {
	runner.invocations = append(runner.invocations, append([]string{name}, args...))
	if runner.stdout != "" && streams.Stdout != nil {
		if _, err := io.WriteString(streams.Stdout, runner.stdout); err != nil {
			return 0, err
		}
	}
	return runner.exitCode, nil
}

// composeRepository builds a provisioned project that both holds Compose files
// and declares a Compose command, which is the case where discovery and
// explicit declaration are easiest to confuse.
func composeRepository(t *testing.T, home string) (App, string, *lifecycleStub) {
	t.Helper()

	repository := filepath.Join(home, "app")
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	for _, name := range []string{"docker-compose.yml", "compose.yaml", "Makefile"} {
		if err := os.WriteFile(
			filepath.Join(repository, name),
			[]byte("services:\n  web:\n    image: nginx\n"),
			0o600,
		); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}
	if err := os.WriteFile(
		filepath.Join(repository, ".isolated-dev.toml"),
		[]byte(composeConfiguration),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	lifecycle := &lifecycleStub{}
	application := upApp(t, home, repository, lifecycle)
	application.GuestProvisioner = &guestStub{guestPath: "/home/fx/app"}
	return application, repository, lifecycle
}

// Convenience never becomes execution: no lifecycle verb reads the repository
// for something to start, even when the repository is full of things another
// tool would happily run and the project declares a Compose command.
func TestLifecycleVerbsNeverExecuteProjectCommands(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	application, repository, _ := composeRepository(t, home)
	commands := &projectCommandStub{}
	application.ProjectCommands = commands

	ctx := context.Background()
	var output bytes.Buffer
	if err := application.Up(ctx, repository, &output); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if err := application.Status(ctx, repository, &output); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if err := application.Open(ctx, repository, &output); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := application.Stop(ctx, repository); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := application.Destroy(ctx, repository); err != nil {
		t.Fatalf("Destroy() error = %v", err)
	}

	if len(commands.requests) != 0 {
		t.Errorf("project command executions = %#v, want none from lifecycle verbs", commands.requests)
	}
	for _, leaked := range []string{"compose", "docker-compose", "makefile"} {
		if strings.Contains(strings.ToLower(output.String()), leaked) {
			t.Errorf("lifecycle output = %q, want no mention of %q", output.String(), leaked)
		}
	}
}

// The whole path a developer actually invokes — CLI verb, declaration lookup,
// guest execution — hands back the command's own result: its output on the
// developer's stdout and its exit status as the CLI's own, with nothing added
// to stderr because a failing command is not a CLI failure.
func TestRunReportsTheGuestResultThroughTheCLI(t *testing.T) {
	t.Parallel()

	application, repository, _ := runRepository(t, t.TempDir(), composeConfiguration)
	runner := &recordingRunner{stdout: "compose up\n", exitCode: 7}
	application.ProjectCommands = projectcmd.Executor{
		Runner:       runner,
		DockerWaiter: readyDockerWaiter{},
	}

	var stdout, stderr bytes.Buffer
	exitCode := cli.Run([]string{"run", repository, "dev"}, cli.Dependencies{
		Stdout: &stdout,
		Stderr: &stderr,
		Run: func(path string, name string) (int, error) {
			return application.Run(context.Background(), path, name, projectcmd.Streams{
				Stdout: &stdout,
				Stderr: &stderr,
			})
		},
	})

	if exitCode != 7 {
		t.Errorf("CLI exit code = %d, want the guest exit status 7", exitCode)
	}
	if stdout.String() != "compose up\n" {
		t.Errorf("stdout = %q, want the guest output", stdout.String())
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want nothing: a failing command is not a CLI error", stderr.String())
	}
	if len(runner.invocations) != 1 {
		t.Fatalf("invocations = %#v, want one", runner.invocations)
	}
	invocation := runner.invocations[0]
	for _, want := range []string{"/home/fx/app", "docker", "compose"} {
		if !containsElementWith(invocation, want) {
			t.Errorf("invocation = %#v, want it to carry %q", invocation, want)
		}
	}
	user := slices.Index(invocation, "-u")
	if user < 0 || user+1 >= len(invocation) {
		t.Fatalf("invocation = %#v, want the command to drop to a guest user", invocation)
	}
	if invocation[user+1] != "fx" {
		t.Errorf(
			"guest user = %q, want the provisioned non-root user fx",
			invocation[user+1],
		)
	}
}

// A name the project does not declare stops at the declaration lookup: the
// machine is never asked to run anything, so no repository content executes.
func TestRunRejectsAnUndeclaredNameThroughTheCLIWithoutExecuting(t *testing.T) {
	t.Parallel()

	application, repository, _ := runRepository(t, t.TempDir(), composeConfiguration)
	runner := &recordingRunner{}
	application.ProjectCommands = projectcmd.Executor{
		Runner:       runner,
		DockerWaiter: readyDockerWaiter{},
	}

	var stdout, stderr bytes.Buffer
	exitCode := cli.Run([]string{"run", repository, "compose"}, cli.Dependencies{
		Stdout: &stdout,
		Stderr: &stderr,
		Run: func(path string, name string) (int, error) {
			return application.Run(context.Background(), path, name, projectcmd.Streams{
				Stdout: &stdout,
				Stderr: &stderr,
			})
		},
	})

	if exitCode != 1 {
		t.Errorf("CLI exit code = %d, want 1 for a rejected name", exitCode)
	}
	if len(runner.invocations) != 0 {
		t.Fatalf("invocations = %#v, want nothing executed", runner.invocations)
	}
	for _, want := range []string{"compose", "not declared", "dev"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr = %q, want it to mention %q", stderr.String(), want)
		}
	}
}

// readyDockerWaiter answers as a guest daemon that is already up, so a Compose
// command's readiness check is satisfied without a real machine.
type readyDockerWaiter struct{}

func (readyDockerWaiter) WaitDocker(_ context.Context, _ string) error { return nil }

// containsElementWith reports whether any argv element contains the substring,
// which lets a test look for a path fragment inside a composed argument.
func containsElementWith(argv []string, substring string) bool {
	for _, element := range argv {
		if strings.Contains(element, substring) {
			return true
		}
	}
	return false
}
