package baseimage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestExecRunnerRunsCommand(t *testing.T) {
	t.Parallel()

	output, err := (ExecRunner{}).Run(context.Background(), "sh", "-c", "printf success")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if string(output) != "success" {
		t.Fatalf("Run() output = %q, want success", output)
	}
}

func TestReferenceRejectsUnsafeVersion(t *testing.T) {
	t.Parallel()

	if _, err := Reference("../latest"); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("Reference() error = %v, want invalid version", err)
	}
}

func TestEnsureReportsConfigurationAndBuildFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		manager Manager
		version string
		want    string
	}{
		{
			name:    "missing runner",
			manager: Manager{},
			version: "1",
			want:    "runner",
		},
		{
			name:    "invalid version",
			manager: Manager{Runner: &fakeRunner{}},
			version: "../latest",
			want:    "invalid base-image version",
		},
		{
			name: "build",
			manager: Manager{Runner: &fakeRunner{responses: []fakeResponse{
				{err: errors.New("not found")},
				{output: []byte("build log"), err: errors.New("exit 1")},
			}}},
			version: "1",
			want:    "build base image",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := test.manager.Ensure(context.Background(), test.version, "/context")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Ensure() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestWaitDockerReportsFallbackFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		manager Manager
		want    string
	}{
		{
			name:    "missing runner",
			manager: Manager{},
			want:    "runner",
		},
		{
			name: "fallback start",
			manager: Manager{
				Runner: &fakeRunner{responses: []fakeResponse{
					{output: []byte("not ready"), err: errors.New("exit 1")},
					{output: []byte("start failed"), err: errors.New("exit 2")},
				}},
				ReadinessTries: 1,
				FallbackTries:  1,
				Sleep:          func(time.Duration) {},
			},
			want: "start Docker fallback",
		},
		{
			name: "fallback readiness",
			manager: Manager{
				Runner: &fakeRunner{responses: []fakeResponse{
					{output: []byte("not ready"), err: errors.New("exit 1")},
					{},
					{output: []byte("still not ready"), err: errors.New("exit 2")},
				}},
				ReadinessTries: 1,
				FallbackTries:  1,
				Sleep:          func(time.Duration) {},
			},
			want: "did not become ready after fallback",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.manager.WaitDocker(context.Background(), "project-machine")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("WaitDocker() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestWaitDockerUsesDefaultsAndHonorsCancellation(t *testing.T) {
	t.Parallel()

	t.Run("defaults", func(t *testing.T) {
		t.Parallel()
		manager := Manager{Runner: &fakeRunner{}}
		if err := manager.WaitDocker(context.Background(), "project-machine"); err != nil {
			t.Fatalf("WaitDocker() error = %v", err)
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		manager := Manager{
			Runner: &fakeRunner{responses: []fakeResponse{{err: errors.New("not ready")}}},
		}
		err := manager.waitForDocker(ctx, "project-machine", 2, time.Nanosecond, func(time.Duration) {})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waitForDocker() error = %v, want context canceled", err)
		}
	})
}
