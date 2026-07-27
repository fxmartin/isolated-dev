package machine

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fxmartin/isolated-dev/internal/state"
)

type call struct {
	name string
	args []string
}

type response struct {
	output []byte
	err    error
}

type runnerStub struct {
	calls     []call
	responses []response
}

func (runner *runnerStub) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	runner.calls = append(runner.calls, call{name: name, args: append([]string(nil), args...)})
	if len(runner.responses) == 0 {
		return nil, nil
	}
	result := runner.responses[0]
	runner.responses = runner.responses[1:]
	return result.output, result.err
}

type stateStoreStub struct {
	project   state.Project
	loadErr   error
	saved     []state.Project
	deleted   []string
	deleteErr error
}

func (store *stateStoreStub) Load(string) (state.Project, error) {
	return store.project, store.loadErr
}

func (store *stateStoreStub) Save(project state.Project) error {
	store.saved = append(store.saved, project)
	return nil
}

func (store *stateStoreStub) Delete(machineName string) error {
	store.deleted = append(store.deleted, machineName)
	return store.deleteErr
}

type dockerWaiterStub struct {
	machines []string
	err      error
}

func (waiter *dockerWaiterStub) WaitDocker(_ context.Context, machineName string) error {
	waiter.machines = append(waiter.machines, machineName)
	return waiter.err
}

func TestUpCreatesMissingMachineAndRetriesTransientBoot(t *testing.T) {
	t.Parallel()

	runner := &runnerStub{responses: []response{
		{output: []byte("[]")},
		{},
		{err: errors.New("operation not supported by device")},
		{},
	}}
	store := &stateStoreStub{loadErr: state.ErrNotFound}
	docker := &dockerWaiterStub{}
	manager := Manager{
		Runner:       runner,
		StateStore:   store,
		DockerWaiter: docker,
		BootTries:    2,
		RetryDelay:   time.Nanosecond,
		Sleep:        func(time.Duration) {},
	}
	request := Request{
		ProjectPath:      "/Users/fx/dev/app",
		MachineName:      "isolated-dev-app-abcd1234",
		BaseImage:        "local/isolated-dev-base:1",
		BaseImageVersion: "1",
		CPUs:             4,
		MemoryGB:         8,
		MountScope:       "home",
	}

	result, err := manager.Up(context.Background(), request)
	if err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if !result.Created {
		t.Fatal("Up() Created = false, want true")
	}
	if len(store.saved) != 1 || store.saved[0].MachineName != request.MachineName {
		t.Errorf("saved state = %+v", store.saved)
	}
	if !reflect.DeepEqual(docker.machines, []string{request.MachineName}) {
		t.Errorf("Docker waiter machines = %#v", docker.machines)
	}

	wantCreate := []string{
		"machine", "create",
		"--name", request.MachineName,
		"--cpus", "4",
		"--memory", "8G",
		"--home-mount", "rw",
		request.BaseImage,
	}
	if got := runner.calls[1].args; !reflect.DeepEqual(got, wantCreate) {
		t.Errorf("create args = %#v, want %#v", got, wantCreate)
	}
}

func TestUpRejectsImmutableConfigurationDrift(t *testing.T) {
	t.Parallel()

	store := &stateStoreStub{project: state.Project{
		SchemaVersion:    1,
		ProjectPath:      "/Users/fx/dev/app",
		MachineName:      "isolated-dev-app-abcd1234",
		BaseImage:        "local/isolated-dev-base:1",
		BaseImageVersion: "1",
		MountScope:       "home",
		CPUs:             4,
		MemoryGB:         8,
	}}
	manager := Manager{
		Runner:       &runnerStub{},
		StateStore:   store,
		DockerWaiter: &dockerWaiterStub{},
	}

	_, err := manager.Up(context.Background(), Request{
		ProjectPath:      store.project.ProjectPath,
		MachineName:      store.project.MachineName,
		BaseImage:        store.project.BaseImage,
		BaseImageVersion: store.project.BaseImageVersion,
		MountScope:       store.project.MountScope,
		CPUs:             6,
		MemoryGB:         8,
	})
	if err == nil || !strings.Contains(err.Error(), "CPUs") || !strings.Contains(err.Error(), "recreate") {
		t.Fatalf("Up() error = %v, want CPU drift and recreation guidance", err)
	}
}

func TestUpRefusesToAdoptMachineWithoutProjectState(t *testing.T) {
	t.Parallel()

	runner := &runnerStub{responses: []response{{
		output: []byte(`[{"id":"isolated-dev-app-abcd1234","status":"running"}]`),
	}}}
	store := &stateStoreStub{loadErr: state.ErrNotFound}
	manager := Manager{
		Runner:       runner,
		StateStore:   store,
		DockerWaiter: &dockerWaiterStub{},
	}

	_, err := manager.Up(context.Background(), Request{
		ProjectPath:      "/Users/fx/dev/app",
		MachineName:      "isolated-dev-app-abcd1234",
		BaseImage:        "local/isolated-dev-base:1",
		BaseImageVersion: "1",
		CPUs:             4,
		MemoryGB:         8,
		MountScope:       "home",
	})
	if err == nil || !strings.Contains(err.Error(), "not managed") {
		t.Fatalf("Up() error = %v, want unmanaged collision error", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %+v, want only read-only machine list", runner.calls)
	}
}

func TestStopIsIdempotentWhenMachineDoesNotExist(t *testing.T) {
	t.Parallel()

	runner := &runnerStub{responses: []response{{output: []byte("[]")}}}
	manager := Manager{Runner: runner}

	if err := manager.Stop(context.Background(), "isolated-dev-app-abcd1234"); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if len(runner.calls) != 1 {
		t.Errorf("calls = %+v, want only list", runner.calls)
	}
}

func TestDestroyDeletesOnlyResolvedMachineAndState(t *testing.T) {
	t.Parallel()

	runner := &runnerStub{responses: []response{
		{output: []byte(`[{"id":"isolated-dev-app-abcd1234","status":"stopped"}]`)},
		{},
	}}
	store := &stateStoreStub{project: state.Project{
		SchemaVersion: 1,
		MachineName:   "isolated-dev-app-abcd1234",
	}}
	manager := Manager{Runner: runner, StateStore: store}

	if err := manager.Destroy(context.Background(), "isolated-dev-app-abcd1234"); err != nil {
		t.Fatalf("Destroy() error = %v", err)
	}
	wantDelete := []string{"machine", "delete", "isolated-dev-app-abcd1234"}
	if got := runner.calls[1].args; !reflect.DeepEqual(got, wantDelete) {
		t.Errorf("delete args = %#v, want %#v", got, wantDelete)
	}
	if !reflect.DeepEqual(store.deleted, []string{"isolated-dev-app-abcd1234"}) {
		t.Errorf("deleted state = %#v", store.deleted)
	}
}

func TestDestroyRefusesMachineWithoutProjectState(t *testing.T) {
	t.Parallel()

	runner := &runnerStub{responses: []response{{
		output: []byte(`[{"id":"isolated-dev-app-abcd1234","status":"stopped"}]`),
	}}}
	store := &stateStoreStub{loadErr: state.ErrNotFound}
	manager := Manager{Runner: runner, StateStore: store}

	err := manager.Destroy(context.Background(), "isolated-dev-app-abcd1234")
	if err == nil || !strings.Contains(err.Error(), "not managed") {
		t.Fatalf("Destroy() error = %v, want unmanaged collision error", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %+v, want only read-only machine list", runner.calls)
	}
	if len(store.deleted) != 0 {
		t.Fatalf("deleted state = %#v, want none", store.deleted)
	}
}
