package projectcmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestExecRunnerStreamsOutputAndReportsTheExitStatus(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	exitCode, err := ExecRunner{}.Run(
		context.Background(),
		Streams{Stdout: &stdout, Stderr: &stderr},
		"/bin/sh",
		"-c", "printf 'out\\n'; printf 'err\\n' >&2; exit 7",
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if exitCode != 7 {
		t.Errorf("exit code = %d, want 7", exitCode)
	}
	if stdout.String() != "out\n" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "out\n")
	}
	if stderr.String() != "err\n" {
		t.Errorf("stderr = %q, want %q", stderr.String(), "err\n")
	}
}

func TestExecRunnerForwardsStandardInput(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	exitCode, err := ExecRunner{}.Run(
		context.Background(),
		Streams{Stdin: strings.NewReader("piped\n"), Stdout: &stdout},
		"/bin/cat",
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0", exitCode)
	}
	if stdout.String() != "piped\n" {
		t.Errorf("stdout = %q, want the forwarded input", stdout.String())
	}
}

func TestExecRunnerReportsASignalledCommandAsAFailure(t *testing.T) {
	t.Parallel()

	exitCode, err := ExecRunner{}.Run(
		context.Background(),
		Streams{},
		"/bin/sh",
		"-c", "kill -TERM $$",
	)
	if err == nil {
		t.Fatalf("Run() exit code = %d, error = nil; want a termination failure", exitCode)
	}
	if !strings.Contains(err.Error(), "terminated") {
		t.Errorf("error = %v, want it to report the termination", err)
	}
}

func TestExecRunnerReportsAnUnstartableCommandAsAnError(t *testing.T) {
	t.Parallel()

	_, err := ExecRunner{}.Run(
		context.Background(),
		Streams{},
		"/nonexistent/isolated-dev-missing-binary",
	)
	if err == nil {
		t.Fatal("Run() error = nil, want a start failure")
	}
}
