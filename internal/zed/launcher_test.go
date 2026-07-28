package zed

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type runnerStub struct {
	names  []string
	args   [][]string
	output []byte
	err    error
}

func (runner *runnerStub) Run(
	_ context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	runner.names = append(runner.names, name)
	runner.args = append(runner.args, append([]string(nil), args...))
	return runner.output, runner.err
}

func lookPathStub(path string, err error) func(string) (string, error) {
	return func(string) (string, error) { return path, err }
}

func TestOpenLaunchesZedInRemoteMode(t *testing.T) {
	t.Parallel()

	runner := &runnerStub{}
	launcher := Launcher{Runner: runner, LookPath: lookPathStub("/opt/homebrew/bin/zed", nil)}

	if err := launcher.Open(
		context.Background(),
		"isolated-dev-app-abcd1234",
		"/Users/fx/dev/app",
	); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if len(runner.names) != 1 || runner.names[0] != "/opt/homebrew/bin/zed" {
		t.Fatalf("commands = %v, want the resolved zed binary", runner.names)
	}
	want := []string{"ssh://isolated-dev-app-abcd1234/Users/fx/dev/app"}
	if !reflect.DeepEqual(runner.args[0], want) {
		t.Errorf("arguments = %v, want %v", runner.args[0], want)
	}
}

func TestOpenEscapesTheGuestPath(t *testing.T) {
	t.Parallel()

	runner := &runnerStub{}
	launcher := Launcher{Runner: runner, LookPath: lookPathStub("/usr/local/bin/zed", nil)}

	if err := launcher.Open(
		context.Background(),
		"isolated-dev-my-app-abcd1234",
		"/home/fx/my projects/app",
	); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	want := "ssh://isolated-dev-my-app-abcd1234/home/fx/my%20projects/app"
	if runner.args[0][0] != want {
		t.Errorf("target = %q, want %q", runner.args[0][0], want)
	}
}

func TestOpenExplainsAMissingZedCommand(t *testing.T) {
	t.Parallel()

	runner := &runnerStub{}
	launcher := Launcher{
		Runner:   runner,
		LookPath: lookPathStub("", errors.New("executable file not found in $PATH")),
	}

	err := launcher.Open(context.Background(), "isolated-dev-app-abcd1234", "/Users/fx/dev/app")
	if err == nil || !strings.Contains(err.Error(), "cli: install") {
		t.Fatalf("Open() error = %v, want actionable Zed CLI guidance", err)
	}
	if len(runner.names) != 0 {
		t.Errorf("commands = %v, want no launch attempt", runner.names)
	}
}

func TestOpenReportsALaunchFailure(t *testing.T) {
	t.Parallel()

	launcher := Launcher{
		Runner:   &runnerStub{output: []byte("could not connect"), err: errors.New("exit status 1")},
		LookPath: lookPathStub("/usr/local/bin/zed", nil),
	}

	err := launcher.Open(context.Background(), "isolated-dev-app-abcd1234", "/Users/fx/dev/app")
	if err == nil || !strings.Contains(err.Error(), "could not connect") {
		t.Fatalf("Open() error = %v, want the Zed output reported", err)
	}
}

func TestOpenRejectsAnUnusableTarget(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		launcher  Launcher
		alias     string
		guestPath string
		want      string
	}{
		"runner missing": {
			launcher:  Launcher{LookPath: lookPathStub("/usr/local/bin/zed", nil)},
			alias:     "isolated-dev-app-abcd1234",
			guestPath: "/Users/fx/dev/app",
			want:      "runner is not configured",
		},
		"alias invalid": {
			launcher:  Launcher{Runner: &runnerStub{}, LookPath: lookPathStub("zed", nil)},
			alias:     "isolated dev/app",
			guestPath: "/Users/fx/dev/app",
			want:      "invalid SSH host alias",
		},
		"guest path relative": {
			launcher:  Launcher{Runner: &runnerStub{}, LookPath: lookPathStub("zed", nil)},
			alias:     "isolated-dev-app-abcd1234",
			guestPath: "app",
			want:      "must be absolute",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := testCase.launcher.Open(
				context.Background(),
				testCase.alias,
				testCase.guestPath,
			)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("Open() error = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestLauncherFindsZedOnPathByDefault(t *testing.T) {
	t.Parallel()

	// Without an override the launcher resolves `zed` through the host PATH,
	// which reports the same actionable guidance when Zed's CLI is absent.
	launcher := Launcher{Runner: &runnerStub{}}

	err := launcher.Open(context.Background(), "isolated-dev-app-abcd1234", "/Users/fx/dev/app")
	if err != nil && !strings.Contains(err.Error(), "cli: install") {
		t.Fatalf("Open() error = %v, want either a launch or the missing-CLI guidance", err)
	}
}
