package main_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// builtBinary is the CLI these tests drive. The wiring in main is only real
// once it is linked and executed, so the tests run the actual binary rather
// than a re-assembled copy of its dependencies.
var builtBinary string

func TestMain(m *testing.M) {
	directory, err := os.MkdirTemp("", "isolated-dev-build-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create build directory: %v\n", err)
		os.Exit(1)
	}
	builtBinary = filepath.Join(directory, "isolated-dev")
	if output, err := exec.Command("go", "build", "-o", builtBinary, ".").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build isolated-dev: %v\n%s", err, output)
		os.RemoveAll(directory)
		os.Exit(1)
	}

	status := m.Run()
	if err := os.RemoveAll(directory); err != nil {
		fmt.Fprintf(os.Stderr, "remove build directory: %v\n", err)
	}
	os.Exit(status)
}

type invocation struct {
	exitCode int
	stdout   string
	stderr   string
}

// invoke runs the built CLI against a private HOME, so nothing a test does can
// reach the developer's own state directory or SSH configuration.
func invoke(t *testing.T, args ...string) invocation {
	t.Helper()

	home := t.TempDir()
	command := exec.Command(builtBinary, args...)
	command.Env = append(
		os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, "config"),
	)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	exitCode := 0
	if err := command.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run isolated-dev %v: %v\n%s", args, err, stderr.String())
		}
		exitCode = exitErr.ExitCode()
	}
	return invocation{exitCode: exitCode, stdout: stdout.String(), stderr: stderr.String()}
}

// gitRepository writes a repository the CLI will resolve, with whatever
// configuration the test needs.
func gitRepository(t *testing.T, configuration string) string {
	t.Helper()

	repository := t.TempDir()
	if err := os.Mkdir(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	// A repository full of things another tool would run: none of it may become
	// something this CLI executes.
	for _, name := range []string{"docker-compose.yml", "Makefile", "package.json"} {
		if err := os.WriteFile(filepath.Join(repository, name), []byte("{}\n"), 0o600); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
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
	return repository
}

const declaredCommands = `version = 1

[commands.dev]
args = ["docker", "compose", "--profile", "dev", "up", "-d"]
compose = true

[commands.test]
args = ["npm", "test"]
`

// The built binary offers `run` as a verb of its own, so a developer learns the
// command must be named rather than discovered.
func TestBinaryDocumentsTheRunVerb(t *testing.T) {
	t.Parallel()

	result := invoke(t)
	if result.exitCode != 2 {
		t.Errorf("exit code = %d, want 2 for usage", result.exitCode)
	}
	if !strings.Contains(result.stderr, "run PROJECT COMMAND") {
		t.Errorf("usage = %q, want it to document `run PROJECT COMMAND`", result.stderr)
	}
}

func TestBinaryReportsItsVersion(t *testing.T) {
	t.Parallel()

	result := invoke(t, "--version")
	if result.exitCode != 0 {
		t.Errorf("exit code = %d, want 0", result.exitCode)
	}
	if !strings.HasPrefix(result.stdout, "isolated-dev ") {
		t.Errorf("stdout = %q, want the version line", result.stdout)
	}
}

// `run` without a name is a forgotten command name, not a request to work out
// what the project probably meant.
func TestBinaryRequiresACommandName(t *testing.T) {
	t.Parallel()

	repository := gitRepository(t, declaredCommands)

	result := invoke(t, "run", repository)
	if result.exitCode != 2 {
		t.Errorf("exit code = %d, want 2", result.exitCode)
	}
	if !strings.Contains(result.stderr, "isolated-dev run PROJECT COMMAND") {
		t.Errorf("stderr = %q, want guidance to name the command", result.stderr)
	}
}

// An undeclared name is rejected against the project's declarations before the
// binary touches a machine, so nothing in the repository is read for meaning
// and nothing is executed.
func TestBinaryRejectsAnUndeclaredCommandWithoutExecuting(t *testing.T) {
	t.Parallel()

	repository := gitRepository(t, declaredCommands)

	result := invoke(t, "run", repository, "compose")
	if result.exitCode != 1 {
		t.Errorf("exit code = %d, want 1", result.exitCode)
	}
	for _, want := range []string{"compose", "not declared", "dev", "test"} {
		if !strings.Contains(result.stderr, want) {
			t.Errorf("stderr = %q, want it to mention %q", result.stderr, want)
		}
	}
	if result.stdout != "" {
		t.Errorf("stdout = %q, want nothing produced", result.stdout)
	}
}

// A project that declares nothing can run nothing, however much the repository
// contains.
func TestBinaryReportsAProjectThatDeclaresNoCommands(t *testing.T) {
	t.Parallel()

	repository := gitRepository(t, "")

	result := invoke(t, "run", repository, "dev")
	if result.exitCode != 1 {
		t.Errorf("exit code = %d, want 1", result.exitCode)
	}
	if !strings.Contains(result.stderr, "declares no commands") {
		t.Errorf("stderr = %q, want an empty-configuration rejection", result.stderr)
	}
}

// The `run` verb is wired to the same project resolution as every other verb,
// so a path that is not a repository is refused there too.
func TestBinaryRejectsRunOutsideARepository(t *testing.T) {
	t.Parallel()

	result := invoke(t, "run", t.TempDir(), "dev")
	if result.exitCode != 1 {
		t.Errorf("exit code = %d, want 1", result.exitCode)
	}
	if !strings.Contains(result.stderr, "not a Git repository") {
		t.Errorf("stderr = %q, want a repository rejection", result.stderr)
	}
}

// A declaration the CLI cannot honour is refused while loading configuration,
// before any machine work, so a malformed project fails fast and safely.
func TestBinaryRejectsAnUnusableDeclarationBeforeRunning(t *testing.T) {
	t.Parallel()

	repository := gitRepository(t, "version = 1\n\n[commands.dev]\nargs = [\"npm\"]\nworkdir = \"../escape\"\n")

	result := invoke(t, "run", repository, "dev")
	if result.exitCode != 1 {
		t.Errorf("exit code = %d, want 1", result.exitCode)
	}
	if !strings.Contains(result.stderr, "must stay inside the project") {
		t.Errorf("stderr = %q, want the escaping workdir rejection", result.stderr)
	}
}

// The exit statuses the CLI produces for its own refusals stay distinct from a
// guest command's, so a script can tell "was not run" from "ran and failed".
func TestBinaryUsesDistinctStatusesForItsOwnRefusals(t *testing.T) {
	t.Parallel()

	usage := invoke(t, "run")
	rejected := invoke(t, "run", gitRepository(t, declaredCommands), "unknown")
	if usage.exitCode == rejected.exitCode {
		t.Errorf(
			"usage and rejection both exited %s, want distinct statuses",
			strconv.Itoa(usage.exitCode),
		)
	}
}
