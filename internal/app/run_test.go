package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fxmartin/isolated-dev/internal/project"
	"github.com/fxmartin/isolated-dev/internal/projectcmd"
)

type projectCommandStub struct {
	requests []projectcmd.Request
	exitCode int
	err      error
	stdout   string
}

func (stub *projectCommandStub) Execute(
	_ context.Context,
	request projectcmd.Request,
	streams projectcmd.Streams,
) (int, error) {
	stub.requests = append(stub.requests, request)
	if stub.stdout != "" && streams.Stdout != nil {
		if _, err := io.WriteString(streams.Stdout, stub.stdout); err != nil {
			return 0, err
		}
	}
	return stub.exitCode, stub.err
}

// runRepository builds a provisioned project: a repository with the given
// configuration and the guest identity that `up` records.
func runRepository(t *testing.T, home string, configuration string) (App, string, *projectCommandStub) {
	t.Helper()

	repository := filepath.Join(home, "app")
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if configuration != "" {
		if err := os.WriteFile(
			filepath.Join(repository, ".isolated-dev.toml"),
			[]byte(configuration),
			0o600,
		); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}
	commands := &projectCommandStub{}
	application := upApp(t, home, repository, &lifecycleStub{})
	application.GuestProvisioner = &guestStub{guestPath: "/home/fx/app"}
	application.ProjectCommands = commands
	if err := application.Up(context.Background(), repository, io.Discard); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	return application, repository, commands
}

const composeConfiguration = `version = 1

[commands.dev]
args = ["docker", "compose", "--profile", "dev", "up", "-d"]
compose = true
`

func TestUpNeitherDiscoversNorStartsProjectServices(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository := filepath.Join(home, "app")
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	for _, name := range []string{"docker-compose.yml", "compose.yaml"} {
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
	commands := &projectCommandStub{}
	application := upApp(t, home, repository, &lifecycleStub{})
	application.ProjectCommands = commands

	var summary bytes.Buffer
	if err := application.Up(context.Background(), repository, &summary); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if len(commands.requests) != 0 {
		t.Errorf("project command executions = %#v, want none from up", commands.requests)
	}
	if strings.Contains(strings.ToLower(summary.String()), "compose") {
		t.Errorf("up summary = %q, want no service discovery", summary.String())
	}
}

func TestRunExecutesAnExplicitlyDeclaredCommand(t *testing.T) {
	t.Parallel()

	application, repository, commands := runRepository(t, t.TempDir(), composeConfiguration)
	commands.exitCode = 2
	commands.stdout = "compose up\n"
	var stdout bytes.Buffer

	exitCode, err := application.Run(
		context.Background(),
		repository,
		"dev",
		projectcmd.Streams{Stdout: &stdout},
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if exitCode != 2 {
		t.Errorf("exit code = %d, want the guest exit status 2", exitCode)
	}
	if stdout.String() != "compose up\n" {
		t.Errorf("stdout = %q, want the guest output", stdout.String())
	}
	if len(commands.requests) != 1 {
		t.Fatalf("executions = %#v, want one", commands.requests)
	}
	request := commands.requests[0]
	resolved, err := project.Resolve(repository)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if request.MachineName != resolved.MachineName {
		t.Errorf("MachineName = %q, want %q", request.MachineName, resolved.MachineName)
	}
	if request.GuestUser != "fx" {
		t.Errorf("GuestUser = %q, want the provisioned non-root guest user", request.GuestUser)
	}
	if request.GuestProjectPath != "/home/fx/app" {
		t.Errorf("GuestProjectPath = %q, want the mounted project", request.GuestProjectPath)
	}
	if request.Name != "dev" {
		t.Errorf("Name = %q, want dev", request.Name)
	}
	if !request.Command.Compose {
		t.Errorf("Command = %+v, want the declared Compose flag", request.Command)
	}
}

func TestRunRejectsACommandTheProjectDoesNotDeclare(t *testing.T) {
	t.Parallel()

	application, repository, commands := runRepository(t, t.TempDir(), composeConfiguration)

	_, err := application.Run(context.Background(), repository, "deploy", projectcmd.Streams{})
	if err == nil {
		t.Fatal("Run() error = nil, want an undeclared command rejection")
	}
	for _, want := range []string{"deploy", "not declared", "dev"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %q", err, want)
		}
	}
	if len(commands.requests) != 0 {
		t.Errorf("executions = %#v, want nothing executed", commands.requests)
	}
}

func TestRunReportsThatAProjectDeclaresNoCommands(t *testing.T) {
	t.Parallel()

	application, repository, commands := runRepository(t, t.TempDir(), "")

	_, err := application.Run(context.Background(), repository, "dev", projectcmd.Streams{})
	if err == nil || !strings.Contains(err.Error(), "declares no commands") {
		t.Fatalf("Run() error = %v, want an empty-configuration rejection", err)
	}
	if len(commands.requests) != 0 {
		t.Errorf("executions = %#v, want nothing executed", commands.requests)
	}
}

func TestRunRequiresAProvisionedMachine(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository := filepath.Join(home, "app")
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(repository, ".isolated-dev.toml"),
		[]byte(composeConfiguration),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	commands := &projectCommandStub{}
	application := upApp(t, home, repository, &lifecycleStub{})
	application.ProjectCommands = commands
	resolved, err := project.Resolve(repository)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if err := application.StateStore.Delete(resolved.MachineName); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	_, err = application.Run(context.Background(), repository, "dev", projectcmd.Streams{})
	if err == nil || !strings.Contains(err.Error(), "isolated-dev up") {
		t.Fatalf("Run() error = %v, want guidance to create the machine first", err)
	}
	if len(commands.requests) != 0 {
		t.Errorf("executions = %#v, want nothing executed", commands.requests)
	}
}

func TestRunRequiresAConfiguredExecutor(t *testing.T) {
	t.Parallel()

	application, repository, _ := runRepository(t, t.TempDir(), composeConfiguration)
	application.ProjectCommands = nil

	_, err := application.Run(context.Background(), repository, "dev", projectcmd.Streams{})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("Run() error = %v, want a missing-executor rejection", err)
	}
}

func TestRunPropagatesAnExecutionFailure(t *testing.T) {
	t.Parallel()

	application, repository, commands := runRepository(t, t.TempDir(), composeConfiguration)
	commands.err = errors.New("Docker never became ready")

	_, err := application.Run(context.Background(), repository, "dev", projectcmd.Streams{})
	if err == nil || !strings.Contains(err.Error(), "Docker never became ready") {
		t.Fatalf("Run() error = %v, want the execution failure", err)
	}
}

func TestRunRejectsAnUnnamedCommandBeforeTouchingTheProject(t *testing.T) {
	t.Parallel()

	application, repository, commands := runRepository(t, t.TempDir(), composeConfiguration)

	_, err := application.Run(context.Background(), repository, "  ", projectcmd.Streams{})
	if err == nil || !strings.Contains(err.Error(), "command name") {
		t.Fatalf("Run() error = %v, want a missing-name rejection", err)
	}
	if len(commands.requests) != 0 {
		t.Errorf("executions = %#v, want nothing executed", commands.requests)
	}
}
