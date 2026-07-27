package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestRunReportsUsageAndUnavailableCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "no arguments", args: nil, want: "usage:"},
		{name: "unknown command", args: []string{"unknown"}, want: "usage:"},
		{name: "status unavailable", args: []string{"status", "/tmp/project"}, want: "status: command is unavailable"},
		{name: "up unavailable", args: []string{"up", "/tmp/project"}, want: "up: command is unavailable"},
		{name: "stop unavailable", args: []string{"stop", "/tmp/project"}, want: "stop: command is unavailable"},
		{
			name: "destroy misplaced confirmation",
			args: []string{"destroy", "/tmp/project", "confirm"},
			want: "pass --yes",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stderr bytes.Buffer
			if exitCode := Run(test.args, Dependencies{
				Stdout: &bytes.Buffer{},
				Stderr: &stderr,
			}); exitCode == 0 {
				t.Fatalf("Run() exit code = 0, want failure")
			}
			if !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("stderr = %q, want containing %q", stderr.String(), test.want)
			}
		})
	}
}

func TestRunPropagatesCommandFailureAfterMutationHook(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	mutations := 0
	exitCode := Run([]string{"up", "/tmp/project"}, Dependencies{
		Stdout: &bytes.Buffer{},
		Stderr: &stderr,
		Up: func(string) error {
			return errors.New("create failed")
		},
		OnMutate: func() {
			mutations++
		},
	})

	if exitCode != 1 {
		t.Fatalf("Run() exit code = %d, want 1", exitCode)
	}
	if mutations != 1 {
		t.Fatalf("mutation hooks = %d, want 1", mutations)
	}
	if got := stderr.String(); !strings.Contains(got, "up: create failed") {
		t.Fatalf("stderr = %q, want command error", got)
	}
}

func TestDestroyAcceptsConfirmationAfterProject(t *testing.T) {
	t.Parallel()

	destroyed := ""
	exitCode := Run([]string{"destroy", "/tmp/project", "--yes"}, Dependencies{
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		Destroy: func(path string) error {
			destroyed = path
			return nil
		},
	})

	if exitCode != 0 {
		t.Fatalf("Run() exit code = %d, want 0", exitCode)
	}
	if destroyed != "/tmp/project" {
		t.Fatalf("destroyed = %q, want /tmp/project", destroyed)
	}
}
