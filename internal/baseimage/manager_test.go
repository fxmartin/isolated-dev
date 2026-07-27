package baseimage

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type recordedCall struct {
	name string
	args []string
}

type fakeRunner struct {
	calls     []recordedCall
	responses []fakeResponse
}

type fakeResponse struct {
	output []byte
	err    error
}

func (runner *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	runner.calls = append(runner.calls, recordedCall{name: name, args: append([]string(nil), args...)})
	if len(runner.responses) == 0 {
		return nil, nil
	}
	response := runner.responses[0]
	runner.responses = runner.responses[1:]
	return response.output, response.err
}

func TestEnsureReusesExistingVersionedImage(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{responses: []fakeResponse{{output: []byte(`{"config":{}}`)}}}
	manager := Manager{Runner: runner}

	result, err := manager.Ensure(context.Background(), "1", "/repo/images/base")
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if result.Built {
		t.Fatal("Ensure() Built = true, want cached image reuse")
	}
	if result.Reference != "local/isolated-dev-base:1" {
		t.Errorf("Reference = %q", result.Reference)
	}
	if len(runner.calls) != 1 || runner.calls[0].name != "container" {
		t.Fatalf("calls = %+v, want one container inspect", runner.calls)
	}
}

func TestEnsureBuildsMissingImageOnce(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{responses: []fakeResponse{
		{err: errors.New("image not found")},
		{},
	}}
	manager := Manager{Runner: runner}

	result, err := manager.Ensure(context.Background(), "2", "/repo/images/base")
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if !result.Built {
		t.Fatal("Ensure() Built = false, want image build")
	}
	wantBuildArgs := []string{
		"build",
		"--tag", "local/isolated-dev-base:2",
		"--label", "dev.isolated.base-version=2",
		"--build-arg", "BASE_VERSION=2",
		"--file", "/repo/images/base/Dockerfile",
		"/repo/images/base",
	}
	if got := runner.calls[1].args; !reflect.DeepEqual(got, wantBuildArgs) {
		t.Errorf("build args = %#v, want %#v", got, wantBuildArgs)
	}
}

func TestWaitDockerUsesDirectDaemonFallback(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{responses: []fakeResponse{
		{err: errors.New("daemon unavailable")},
		{},
		{},
	}}
	manager := Manager{
		Runner:         runner,
		ReadinessTries: 1,
		FallbackTries:  1,
		RetryDelay:     time.Nanosecond,
		Sleep:          func(time.Duration) {},
	}

	if err := manager.WaitDocker(context.Background(), "project-machine"); err != nil {
		t.Fatalf("WaitDocker() error = %v", err)
	}

	if len(runner.calls) != 3 {
		t.Fatalf("calls = %+v, want readiness, fallback, readiness", runner.calls)
	}
	fallback := runner.calls[1]
	if fallback.name != "container" {
		t.Errorf("fallback name = %q, want container", fallback.name)
	}
	wantFallback := []string{
		"machine", "run", "--name", "project-machine", "--root", "--detach", "--",
		"/usr/local/sbin/isolated-dev-dockerd",
	}
	if !reflect.DeepEqual(fallback.args, wantFallback) {
		t.Errorf("fallback args = %#v, want %#v", fallback.args, wantFallback)
	}
}

func TestWaitDockerAllowsSystemServiceToBecomeReady(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{responses: []fakeResponse{
		{err: errors.New("daemon unavailable")},
		{},
	}}
	manager := Manager{
		Runner:         runner,
		ReadinessTries: 2,
		FallbackTries:  1,
		RetryDelay:     time.Nanosecond,
		Sleep:          func(time.Duration) {},
	}

	if err := manager.WaitDocker(context.Background(), "project-machine"); err != nil {
		t.Fatalf("WaitDocker() error = %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("calls = %+v, want readiness retries without fallback", runner.calls)
	}
	for _, call := range runner.calls {
		if reflect.DeepEqual(call.args, []string{
			"machine", "run", "--name", "project-machine", "--root", "--detach", "--",
			"/usr/local/sbin/isolated-dev-dockerd",
		}) {
			t.Fatal("WaitDocker() used fallback while system service became ready")
		}
	}
}
