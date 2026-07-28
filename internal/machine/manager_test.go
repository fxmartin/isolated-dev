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
	saveErr   error
	deleted   []string
	deleteErr error
}

func (store *stateStoreStub) Load(string) (state.Project, error) {
	return store.project, store.loadErr
}

func (store *stateStoreStub) Save(project state.Project) error {
	store.saved = append(store.saved, project)
	return store.saveErr
}

func (store *stateStoreStub) Delete(machineName string) error {
	store.deleted = append(store.deleted, machineName)
	return store.deleteErr
}

type dockerWaiterStub struct {
	machines []string
	err      error
}

type imageEnsurerStub struct {
	references []string
	err        error
}

func (ensurer *imageEnsurerStub) EnsureReference(
	_ context.Context,
	reference string,
) error {
	ensurer.references = append(ensurer.references, reference)
	return ensurer.err
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
	images := &imageEnsurerStub{}
	manager := Manager{
		Runner:       runner,
		StateStore:   store,
		DockerWaiter: docker,
		ImageEnsurer: images,
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
	if !reflect.DeepEqual(images.references, []string{request.BaseImage}) {
		t.Errorf("ensured images = %#v, want %q", images.references, request.BaseImage)
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

func TestUpDoesNotRecordStateWhenBaseImageEnsureFails(t *testing.T) {
	t.Parallel()

	runner := &runnerStub{responses: []response{{output: []byte("[]")}}}
	store := &stateStoreStub{loadErr: state.ErrNotFound}
	images := &imageEnsurerStub{err: errors.New("build failed")}
	manager := Manager{
		Runner:       runner,
		StateStore:   store,
		DockerWaiter: &dockerWaiterStub{},
		ImageEnsurer: images,
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

	_, err := manager.Up(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "ensure base image") {
		t.Fatalf("Up() error = %v, want base-image ensure failure", err)
	}
	if len(store.saved) != 0 {
		t.Fatalf("saved state = %+v, want no state before image is available", store.saved)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %+v, want only machine lookup", runner.calls)
	}
}

func TestUpRemovesNewStateWhenCreateFailsWithoutMachine(t *testing.T) {
	t.Parallel()

	request := validRequest()
	runner := &runnerStub{responses: []response{
		{output: []byte("[]")},
		{output: []byte("create failed"), err: errors.New("exit 1")},
		{output: []byte("[]")},
	}}
	store := &stateStoreStub{loadErr: state.ErrNotFound}
	manager := Manager{
		Runner:       runner,
		StateStore:   store,
		DockerWaiter: &dockerWaiterStub{},
	}

	_, err := manager.Up(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "create machine") {
		t.Fatalf("Up() error = %v, want create failure", err)
	}
	if !reflect.DeepEqual(store.deleted, []string{request.MachineName}) {
		t.Fatalf("deleted state = %#v, want failed creation state removed", store.deleted)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("runner calls = %+v, want lookup, create, and reconciliation lookup", runner.calls)
	}
}

func TestUpRetainsOwnershipStateWhenCreatePartiallyProducesMachine(t *testing.T) {
	t.Parallel()

	request := validRequest()
	runner := &runnerStub{responses: []response{
		{output: []byte("[]")},
		{output: []byte("create failed"), err: errors.New("exit 1")},
		{output: []byte(`[{"id":"isolated-dev-app-abcd1234","status":"unknown"}]`)},
	}}
	store := &stateStoreStub{loadErr: state.ErrNotFound}
	manager := Manager{
		Runner:       runner,
		StateStore:   store,
		DockerWaiter: &dockerWaiterStub{},
	}

	_, err := manager.Up(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "create machine") {
		t.Fatalf("Up() error = %v, want create failure", err)
	}
	if len(store.deleted) != 0 {
		t.Fatalf("deleted state = %#v, want ownership retained for partial machine", store.deleted)
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

func TestUpRejectsUnmanagedImageBeforeMutation(t *testing.T) {
	t.Parallel()

	request := validRequest()
	request.BaseImage = "registry.example/attacker:latest"
	request.BaseImageVersion = "latest"
	runner := &runnerStub{}
	store := &stateStoreStub{loadErr: state.ErrNotFound}
	manager := Manager{
		Runner:       runner,
		StateStore:   store,
		DockerWaiter: &dockerWaiterStub{},
	}

	_, err := manager.Up(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "not a managed isolated-dev image") {
		t.Fatalf("Up() error = %v, want unmanaged image rejection", err)
	}
	if len(runner.calls) != 0 || len(store.saved) != 0 {
		t.Fatalf("runner calls = %+v, saved state = %+v; want no mutation", runner.calls, store.saved)
	}
}

func TestStopIsIdempotentWhenMachineDoesNotExist(t *testing.T) {
	t.Parallel()

	runner := &runnerStub{responses: []response{{output: []byte("[]")}}}
	manager := Manager{Runner: runner}

	if err := manager.Stop(context.Background(), Target{
		ProjectPath: "/Users/fx/dev/app",
		MachineName: "isolated-dev-app-abcd1234",
	}); err != nil {
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
		ProjectPath:   "/Users/fx/dev/app",
		MachineName:   "isolated-dev-app-abcd1234",
	}}
	manager := Manager{Runner: runner, StateStore: store}

	if err := manager.Destroy(context.Background(), Target{
		ProjectPath: "/Users/fx/dev/app",
		MachineName: "isolated-dev-app-abcd1234",
	}); err != nil {
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

	err := manager.Destroy(context.Background(), Target{
		ProjectPath: "/Users/fx/dev/app",
		MachineName: "isolated-dev-app-abcd1234",
	})
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

func TestDestroyRefusesMachineOwnedByCollidingProjectPath(t *testing.T) {
	t.Parallel()

	runner := &runnerStub{responses: []response{{
		output: []byte(`[{"id":"isolated-dev-app-abcd1234","status":"stopped"}]`),
	}}}
	store := &stateStoreStub{project: state.Project{
		SchemaVersion: 1,
		ProjectPath:   "/Users/fx/dev/project-a/app",
		MachineName:   "isolated-dev-app-abcd1234",
	}}
	manager := Manager{Runner: runner, StateStore: store}

	err := manager.Destroy(context.Background(), Target{
		ProjectPath: "/Users/fx/dev/project-b/app",
		MachineName: "isolated-dev-app-abcd1234",
	})
	if err == nil || !strings.Contains(err.Error(), "belongs to project") {
		t.Fatalf("Destroy() error = %v, want project ownership collision", err)
	}
	if len(runner.calls) != 1 || len(store.deleted) != 0 {
		t.Fatalf("runner calls = %+v, deleted state = %+v; want no mutation", runner.calls, store.deleted)
	}
}
