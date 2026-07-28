package machine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fxmartin/isolated-dev/internal/state"
)

func validRequest() Request {
	return Request{
		ProjectPath:      "/Users/fx/dev/app",
		MachineName:      "isolated-dev-app-abcd1234",
		BaseImage:        "local/isolated-dev-base:1",
		BaseImageVersion: "1",
		CPUs:             4,
		MemoryGB:         8,
		MountScope:       "home",
	}
}

func validTarget() Target {
	request := validRequest()
	return Target{
		ProjectPath: request.ProjectPath,
		MachineName: request.MachineName,
	}
}

func storedProject(request Request) state.Project {
	return request.projectState()
}

func TestUpRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	valid := validRequest()
	tests := []struct {
		name    string
		manager Manager
		mutate  func(*Request)
		want    string
	}{
		{
			name:    "runner",
			manager: Manager{StateStore: &stateStoreStub{}, DockerWaiter: &dockerWaiterStub{}},
			mutate:  func(*Request) {},
			want:    "runner",
		},
		{
			name:    "state store",
			manager: Manager{Runner: &runnerStub{}, DockerWaiter: &dockerWaiterStub{}},
			mutate:  func(*Request) {},
			want:    "state store",
		},
		{
			name:    "Docker waiter",
			manager: Manager{Runner: &runnerStub{}, StateStore: &stateStoreStub{}},
			mutate:  func(*Request) {},
			want:    "Docker",
		},
		{
			name:    "machine name",
			manager: Manager{Runner: &runnerStub{}, StateStore: &stateStoreStub{}, DockerWaiter: &dockerWaiterStub{}},
			mutate:  func(request *Request) { request.MachineName = "../unsafe" },
			want:    "invalid machine name",
		},
		{
			name:    "project path",
			manager: Manager{Runner: &runnerStub{}, StateStore: &stateStoreStub{}, DockerWaiter: &dockerWaiterStub{}},
			mutate:  func(request *Request) { request.ProjectPath = "relative" },
			want:    "absolute",
		},
		{
			name:    "base image",
			manager: Manager{Runner: &runnerStub{}, StateStore: &stateStoreStub{}, DockerWaiter: &dockerWaiterStub{}},
			mutate:  func(request *Request) { request.BaseImage = " " },
			want:    "base image",
		},
		{
			name:    "base image version",
			manager: Manager{Runner: &runnerStub{}, StateStore: &stateStoreStub{}, DockerWaiter: &dockerWaiterStub{}},
			mutate:  func(request *Request) { request.BaseImageVersion = "" },
			want:    "base-image version",
		},
		{
			name:    "CPUs",
			manager: Manager{Runner: &runnerStub{}, StateStore: &stateStoreStub{}, DockerWaiter: &dockerWaiterStub{}},
			mutate:  func(request *Request) { request.CPUs = 0 },
			want:    "CPUs",
		},
		{
			name:    "memory",
			manager: Manager{Runner: &runnerStub{}, StateStore: &stateStoreStub{}, DockerWaiter: &dockerWaiterStub{}},
			mutate:  func(request *Request) { request.MemoryGB = 0 },
			want:    "memory",
		},
		{
			name:    "mount scope",
			manager: Manager{Runner: &runnerStub{}, StateStore: &stateStoreStub{}, DockerWaiter: &dockerWaiterStub{}},
			mutate:  func(request *Request) { request.MountScope = "system" },
			want:    "unsupported mount scope",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := valid
			test.mutate(&request)

			_, err := test.manager.Up(context.Background(), request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Up() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestUpReportsLifecycleFailures(t *testing.T) {
	t.Parallel()

	request := validRequest()
	tests := []struct {
		name    string
		runner  *runnerStub
		store   *stateStoreStub
		waiter  *dockerWaiterStub
		context func() context.Context
		want    string
	}{
		{
			name:    "load state",
			runner:  &runnerStub{},
			store:   &stateStoreStub{loadErr: errors.New("storage unavailable")},
			waiter:  &dockerWaiterStub{},
			context: context.Background,
			want:    "load project state",
		},
		{
			name:    "list machines",
			runner:  &runnerStub{responses: []response{{output: []byte("denied"), err: errors.New("exit 1")}}},
			store:   &stateStoreStub{project: storedProject(request)},
			waiter:  &dockerWaiterStub{},
			context: context.Background,
			want:    "list machines",
		},
		{
			name:    "decode machine list",
			runner:  &runnerStub{responses: []response{{output: []byte("not-json")}}},
			store:   &stateStoreStub{project: storedProject(request)},
			waiter:  &dockerWaiterStub{},
			context: context.Background,
			want:    "decode machine list",
		},
		{
			name:    "save state",
			runner:  &runnerStub{responses: []response{{output: []byte("[]")}}},
			store:   &stateStoreStub{loadErr: state.ErrNotFound, saveErr: errors.New("disk full")},
			waiter:  &dockerWaiterStub{},
			context: context.Background,
			want:    "record project state",
		},
		{
			name: "create machine",
			runner: &runnerStub{responses: []response{
				{output: []byte("[]")},
				{output: []byte("create failed"), err: errors.New("exit 1")},
				{output: []byte("[]")},
			}},
			store:   &stateStoreStub{loadErr: state.ErrNotFound},
			waiter:  &dockerWaiterStub{},
			context: context.Background,
			want:    "create machine",
		},
		{
			name: "boot timeout",
			runner: &runnerStub{responses: []response{
				{output: []byte("[]")},
				{},
				{output: []byte("not ready"), err: errors.New("exit 1")},
			}},
			store:   &stateStoreStub{loadErr: state.ErrNotFound},
			waiter:  &dockerWaiterStub{},
			context: context.Background,
			want:    "did not become ready",
		},
		{
			name: "Docker timeout",
			runner: &runnerStub{responses: []response{
				{output: []byte(`[{"name":"isolated-dev-app-abcd1234","status":"running"}]`)},
				{},
			}},
			store:   &stateStoreStub{project: storedProject(request)},
			waiter:  &dockerWaiterStub{err: errors.New("daemon unavailable")},
			context: context.Background,
			want:    "wait for Docker",
		},
		{
			name: "cancelled boot",
			runner: &runnerStub{responses: []response{
				{output: []byte(`[{"id":"isolated-dev-app-abcd1234","status":"running"}]`)},
			}},
			store:  &stateStoreStub{project: storedProject(request)},
			waiter: &dockerWaiterStub{},
			context: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			want: "context canceled",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			manager := Manager{
				Runner:       test.runner,
				StateStore:   test.store,
				DockerWaiter: test.waiter,
				BootTries:    1,
				Sleep:        func(_ time.Duration) {},
			}

			_, err := manager.Up(test.context(), request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Up() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestUpReusesManagedMachineWithDefaultReadinessSettings(t *testing.T) {
	t.Parallel()

	request := validRequest()
	runner := &runnerStub{responses: []response{
		{output: []byte(`[{"name":"isolated-dev-app-abcd1234","status":"running"}]`)},
		{},
	}}
	waiter := &dockerWaiterStub{}
	manager := Manager{
		Runner:       runner,
		StateStore:   &stateStoreStub{project: storedProject(request)},
		DockerWaiter: waiter,
	}

	result, err := manager.Up(context.Background(), request)
	if err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if result.Created {
		t.Fatal("Up() Created = true, want existing machine reuse")
	}
	if len(runner.calls) != 2 {
		t.Fatalf("calls = %+v, want list and readiness probe", runner.calls)
	}
}

func TestUpRejectsRepositoryOnlyMountScope(t *testing.T) {
	t.Parallel()

	request := validRequest()
	request.MountScope = "repository"
	runner := &runnerStub{}
	manager := Manager{
		Runner:       runner,
		StateStore:   &stateStoreStub{loadErr: state.ErrNotFound},
		DockerWaiter: &dockerWaiterStub{},
	}

	// `container machine create` has no bind-mount flag, so a repository-only
	// scope would silently mount nothing rather than the project.
	_, err := manager.Up(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "unsupported mount scope") {
		t.Fatalf("Up() error = %v, want unsupported mount scope", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("calls = %+v, want no host mutation", runner.calls)
	}
}

func TestStopHandlesOwnedMachineStatesAndFailures(t *testing.T) {
	t.Parallel()

	target := validTarget()
	tests := []struct {
		name    string
		manager Manager
		want    string
		calls   int
	}{
		{
			name:    "missing runner",
			manager: Manager{},
			want:    "runner",
		},
		{
			name:    "invalid name",
			manager: Manager{Runner: &runnerStub{}},
			want:    "invalid machine name",
		},
		{
			name: "already stopped",
			manager: Manager{Runner: &runnerStub{responses: []response{{
				output: []byte(`[{"id":"isolated-dev-app-abcd1234","status":"STOPPED"}]`),
			}}}},
			calls: 1,
		},
		{
			// `container machine stop` rejects every status but running and
			// stopped, so an unbootable machine must not be stopped.
			name: "never booted",
			manager: Manager{Runner: &runnerStub{responses: []response{{
				output: []byte(`[{"id":"isolated-dev-app-abcd1234","status":"unknown"}]`),
			}}}},
			calls: 1,
		},
		{
			name: "unmanaged",
			manager: Manager{
				Runner: &runnerStub{responses: []response{{
					output: []byte(`[{"id":"isolated-dev-app-abcd1234","status":"running"}]`),
				}}},
				StateStore: &stateStoreStub{loadErr: state.ErrNotFound},
			},
			want:  "not managed",
			calls: 1,
		},
		{
			name: "state load failure",
			manager: Manager{
				Runner: &runnerStub{responses: []response{{
					output: []byte(`[{"id":"isolated-dev-app-abcd1234","status":"running"}]`),
				}}},
				StateStore: &stateStoreStub{loadErr: errors.New("unreadable")},
			},
			want:  "load project state",
			calls: 1,
		},
		{
			name: "state mismatch",
			manager: Manager{
				Runner: &runnerStub{responses: []response{{
					output: []byte(`[{"id":"isolated-dev-app-abcd1234","status":"running"}]`),
				}}},
				StateStore: &stateStoreStub{project: state.Project{MachineName: "another-machine"}},
			},
			want:  "instead of",
			calls: 1,
		},
		{
			name: "stop failure",
			manager: Manager{
				Runner: &runnerStub{responses: []response{
					{output: []byte(`[{"id":"isolated-dev-app-abcd1234","status":"running"}]`)},
					{output: []byte("busy"), err: errors.New("exit 1")},
				}},
				StateStore: &stateStoreStub{project: state.Project{
					ProjectPath: target.ProjectPath,
					MachineName: target.MachineName,
				}},
			},
			want:  "stop machine",
			calls: 2,
		},
		{
			name: "success",
			manager: Manager{
				Runner: &runnerStub{responses: []response{
					{output: []byte(`[{"id":"isolated-dev-app-abcd1234","status":"running"}]`)},
					{},
				}},
				StateStore: &stateStoreStub{project: state.Project{
					ProjectPath: target.ProjectPath,
					MachineName: target.MachineName,
				}},
			},
			calls: 2,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			operationTarget := target
			if test.name == "invalid name" {
				operationTarget.MachineName = "../unsafe"
			}
			err := test.manager.Stop(context.Background(), operationTarget)
			if test.want == "" && err != nil {
				t.Fatalf("Stop() error = %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("Stop() error = %v, want containing %q", err, test.want)
			}
			if test.calls > 0 {
				runner := test.manager.Runner.(*runnerStub)
				if len(runner.calls) != test.calls {
					t.Fatalf("calls = %+v, want %d", runner.calls, test.calls)
				}
			}
		})
	}
}

func TestDestroyHandlesCleanupFailures(t *testing.T) {
	t.Parallel()

	target := validTarget()
	tests := []struct {
		name      string
		manager   Manager
		target    Target
		want      string
		wantCalls int
	}{
		{
			name:    "missing runner",
			manager: Manager{StateStore: &stateStoreStub{}},
			target:  target,
			want:    "runner",
		},
		{
			name:    "missing store",
			manager: Manager{Runner: &runnerStub{}},
			target:  target,
			want:    "state store",
		},
		{
			name:    "invalid name",
			manager: Manager{Runner: &runnerStub{}, StateStore: &stateStoreStub{}},
			target: Target{
				ProjectPath: target.ProjectPath,
				MachineName: "../unsafe",
			},
			want: "invalid machine name",
		},
		{
			name: "absent machine deletes stale state",
			manager: Manager{
				Runner: &runnerStub{responses: []response{{output: []byte("[]")}}},
				StateStore: &stateStoreStub{project: state.Project{
					ProjectPath: target.ProjectPath,
					MachineName: target.MachineName,
				}},
			},
			target:    target,
			wantCalls: 1,
		},
		{
			// A machine that never booted is deleted directly: `machine stop`
			// would fail on it, while `machine delete` refuses only running.
			name: "never booted machine skips stop",
			manager: Manager{
				Runner: &runnerStub{responses: []response{
					{output: []byte(`[{"id":"isolated-dev-app-abcd1234","status":"unknown"}]`)},
					{},
				}},
				StateStore: &stateStoreStub{project: state.Project{
					ProjectPath: target.ProjectPath,
					MachineName: target.MachineName,
				}},
			},
			target:    target,
			wantCalls: 2,
		},
		{
			name: "stop failure still deletes",
			manager: Manager{
				Runner: &runnerStub{responses: []response{
					{output: []byte(`[{"id":"isolated-dev-app-abcd1234","status":"running"}]`)},
					{output: []byte("busy"), err: errors.New("exit 1")},
					{},
				}},
				StateStore: &stateStoreStub{project: state.Project{
					ProjectPath: target.ProjectPath,
					MachineName: target.MachineName,
				}},
			},
			target:    target,
			wantCalls: 3,
		},
		{
			name: "stop failure surfaces the delete blocker",
			manager: Manager{
				Runner: &runnerStub{responses: []response{
					{output: []byte(`[{"id":"isolated-dev-app-abcd1234","status":"running"}]`)},
					{output: []byte("busy"), err: errors.New("exit 1")},
					{output: []byte("machine is running"), err: errors.New("exit 1")},
				}},
				StateStore: &stateStoreStub{project: state.Project{
					ProjectPath: target.ProjectPath,
					MachineName: target.MachineName,
				}},
			},
			target:    target,
			want:      "delete machine",
			wantCalls: 3,
		},
		{
			name: "delete machine failure",
			manager: Manager{
				Runner: &runnerStub{responses: []response{
					{output: []byte(`[{"id":"isolated-dev-app-abcd1234","status":"stopped"}]`)},
					{output: []byte("busy"), err: errors.New("exit 1")},
				}},
				StateStore: &stateStoreStub{project: state.Project{
					ProjectPath: target.ProjectPath,
					MachineName: target.MachineName,
				}},
			},
			target:    target,
			want:      "delete machine",
			wantCalls: 2,
		},
		{
			name: "delete state failure",
			manager: Manager{
				Runner: &runnerStub{responses: []response{
					{output: []byte("[]")},
				}},
				StateStore: &stateStoreStub{
					project: state.Project{
						ProjectPath: target.ProjectPath,
						MachineName: target.MachineName,
					},
					deleteErr: errors.New("disk error"),
				},
			},
			target:    target,
			want:      "delete project state",
			wantCalls: 1,
		},
		{
			name: "running machine success",
			manager: Manager{
				Runner: &runnerStub{responses: []response{
					{output: []byte(`[{"id":"isolated-dev-app-abcd1234","status":"running"}]`)},
					{},
					{},
				}},
				StateStore: &stateStoreStub{project: state.Project{
					ProjectPath: target.ProjectPath,
					MachineName: target.MachineName,
				}},
			},
			target:    target,
			wantCalls: 3,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.manager.Destroy(context.Background(), test.target)
			if test.want == "" && err != nil {
				t.Fatalf("Destroy() error = %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("Destroy() error = %v, want containing %q", err, test.want)
			}
			if test.wantCalls > 0 {
				runner := test.manager.Runner.(*runnerStub)
				if len(runner.calls) != test.wantCalls {
					t.Fatalf("calls = %+v, want %d", runner.calls, test.wantCalls)
				}
			}
		})
	}
}

// Both Stop and Destroy inspect the machine list before mutating anything, so a
// failed listing must surface instead of being read as "no machine exists".
func TestStopAndDestroyReportMachineListingFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		invoke func(Manager) error
	}{
		{
			name: "stop",
			invoke: func(manager Manager) error {
				return manager.Stop(context.Background(), validTarget())
			},
		},
		{
			name: "destroy",
			invoke: func(manager Manager) error {
				return manager.Destroy(context.Background(), validTarget())
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			manager := Manager{
				Runner: &runnerStub{responses: []response{
					{err: errors.New("daemon unavailable")},
				}},
				StateStore: &stateStoreStub{},
			}

			err := test.invoke(manager)
			if err == nil || !strings.Contains(err.Error(), "list machines") {
				t.Fatalf("%s() error = %v, want a listing failure", test.name, err)
			}
		})
	}
}

// A relative project path is rejected before any machine command runs, so a
// mistyped target can never reach an unrelated machine.
func TestStopRejectsRelativeProjectPath(t *testing.T) {
	t.Parallel()

	runner := &runnerStub{}
	target := validTarget()
	target.ProjectPath = "relative/app"

	err := Manager{Runner: runner, StateStore: &stateStoreStub{}}.
		Stop(context.Background(), target)
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("Stop() error = %v, want an absolute-path failure", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("calls = %+v, want no machine command", runner.calls)
	}
}

// Destroying a project that was never brought up is a no-op rather than an
// error: there is no machine and no state to remove.
func TestDestroyIsSilentForAnUnknownUnownedMachine(t *testing.T) {
	t.Parallel()

	runner := &runnerStub{responses: []response{{output: []byte("[]")}}}
	manager := Manager{
		Runner:     runner,
		StateStore: &stateStoreStub{loadErr: state.ErrNotFound},
	}

	if err := manager.Destroy(context.Background(), validTarget()); err != nil {
		t.Fatalf("Destroy() error = %v, want no failure", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %+v, want only the machine listing", runner.calls)
	}
}

// A failed creation must not leave project state pointing at a machine that
// does not exist. When that cleanup itself fails, both failures are reported so
// the recorded state is never silently wrong.
func TestUpReportsReconciliationFailuresAfterAFailedCreate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		responses []response
		store     *stateStoreStub
		want      string
	}{
		{
			name: "listing fails during reconciliation",
			responses: []response{
				{output: []byte("[]")},
				{err: errors.New("create refused")},
				{err: errors.New("daemon unavailable")},
			},
			store: &stateStoreStub{loadErr: state.ErrNotFound},
			want:  "reconcile failed machine creation",
		},
		{
			name: "state removal fails during reconciliation",
			responses: []response{
				{output: []byte("[]")},
				{err: errors.New("create refused")},
				{output: []byte("[]")},
			},
			store: &stateStoreStub{
				loadErr:   state.ErrNotFound,
				deleteErr: errors.New("state locked"),
			},
			want: "remove project state after failed machine creation",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			manager := Manager{
				Runner:       &runnerStub{responses: test.responses},
				StateStore:   test.store,
				DockerWaiter: &dockerWaiterStub{},
			}

			_, err := manager.Up(context.Background(), validRequest())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Up() error = %v, want containing %q", err, test.want)
			}
			if !strings.Contains(err.Error(), "create machine") {
				t.Fatalf("Up() error = %v, want the original creation failure preserved", err)
			}
		})
	}
}
