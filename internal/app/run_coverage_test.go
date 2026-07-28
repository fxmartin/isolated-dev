package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fxmartin/isolated-dev/internal/project"
	"github.com/fxmartin/isolated-dev/internal/projectcmd"
)

func TestRunRejectsAPathThatIsNotARepository(t *testing.T) {
	t.Parallel()

	commands := &projectCommandStub{}
	application := App{ProjectCommands: commands}

	_, err := application.Run(context.Background(), t.TempDir(), "dev", projectcmd.Streams{})
	if err == nil {
		t.Fatal("Run() error = nil, want an unresolved repository")
	}
	if len(commands.requests) != 0 {
		t.Errorf("executions = %#v, want nothing executed", commands.requests)
	}
}

func TestRunRejectsInvalidConfigurationBeforeExecuting(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository := filepath.Join(home, "app")
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(repository, ".isolated-dev.toml"),
		[]byte("version = 1\n[commands.dev]\nargs = [\"npm\"]\nworkdir = \"/etc\"\n"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	commands := &projectCommandStub{}
	application := App{ProjectCommands: commands, HostChecker: passingHostChecker()}

	_, err := application.Run(context.Background(), repository, "dev", projectcmd.Streams{})
	if err == nil || !strings.Contains(err.Error(), "commands.dev.workdir") {
		t.Fatalf("Run() error = %v, want the configuration rejected", err)
	}
	if len(commands.requests) != 0 {
		t.Errorf("executions = %#v, want nothing executed", commands.requests)
	}
}

func TestRunReportsMissingHostPrerequisites(t *testing.T) {
	t.Parallel()

	application, repository, commands := runRepository(t, t.TempDir(), composeConfiguration)
	application.HostChecker = failingHostChecker()

	_, err := application.Run(context.Background(), repository, "dev", projectcmd.Streams{})
	if err == nil {
		t.Fatal("Run() error = nil, want the host prerequisite failure")
	}
	if len(commands.requests) != 0 {
		t.Errorf("executions = %#v, want nothing executed", commands.requests)
	}
}

func TestRunReportsUnreadableProjectState(t *testing.T) {
	t.Parallel()

	application, repository, commands := runRepository(t, t.TempDir(), composeConfiguration)
	resolved, err := project.Resolve(repository)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(application.StateStore.Root, resolved.MachineName+".json"),
		[]byte("{not json"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err = application.Run(context.Background(), repository, "dev", projectcmd.Streams{})
	if err == nil || !strings.Contains(err.Error(), "load project state") {
		t.Fatalf("Run() error = %v, want the state failure reported", err)
	}
	if len(commands.requests) != 0 {
		t.Errorf("executions = %#v, want nothing executed", commands.requests)
	}
}
