package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fxmartin/isolated-dev/internal/host"
	"github.com/fxmartin/isolated-dev/internal/project"
	"github.com/fxmartin/isolated-dev/internal/state"
)

func appRepository(t *testing.T) string {
	t.Helper()

	repository := t.TempDir()
	if err := os.Mkdir(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	return repository
}

func failingHostChecker() host.Checker {
	return host.Checker{}
}

func TestUpReportsBoundaryFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		projectPath func(*testing.T) string
		application func(*testing.T) App
		want        string
	}{
		{
			name:        "project resolution",
			projectPath: func(*testing.T) string { return "" },
			application: func(*testing.T) App { return App{} },
			want:        "project path",
		},
		{
			name: "configuration",
			projectPath: func(t *testing.T) string {
				repository := appRepository(t)
				if err := os.WriteFile(
					filepath.Join(repository, ".isolated-dev.toml"),
					[]byte("version = 2\n"),
					0o600,
				); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
				return repository
			},
			application: func(*testing.T) App { return App{} },
			want:        "unsupported value",
		},
		{
			name:        "host prerequisites",
			projectPath: appRepository,
			application: func(*testing.T) App { return App{HostChecker: failingHostChecker()} },
			want:        "not configured",
		},
		{
			name:        "missing lifecycle",
			projectPath: appRepository,
			application: func(*testing.T) App { return App{HostChecker: passingHostChecker()} },
			want:        "lifecycle is not configured",
		},
		{
			name:        "manager failure",
			projectPath: appRepository,
			application: func(*testing.T) App {
				return App{
					HostChecker: passingHostChecker(),
					MachineManager: &lifecycleStub{
						upErr: errors.New("machine create failed"),
					},
				}
			},
			want: "machine create failed",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := test.projectPath(t)
			application := test.application(t)
			if path != "" {
				application.HomeDir = filepath.Dir(path)
			}
			err := application.Up(context.Background(), path, io.Discard)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Up() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// The shipped binary never injects HomeDir, so the os.UserHomeDir() fallback is
// the branch that actually gates the full-home mount in production.
func TestUpFallsBackToTheOperatingSystemHomeDirectory(t *testing.T) {
	home := t.TempDir()
	repository := filepath.Join(home, "app")
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	t.Setenv("HOME", home)

	lifecycle := &lifecycleStub{upExisting: true}
	var summary bytes.Buffer
	application := App{
		HostChecker:    passingHostChecker(),
		MachineManager: lifecycle,
	}

	if err := application.Up(context.Background(), repository, &summary); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if len(lifecycle.upRequests) != 1 {
		t.Fatalf("up requests = %+v, want one", lifecycle.upRequests)
	}
	if !strings.HasPrefix(summary.String(), "ready ") {
		t.Errorf("summary = %q, want converged machine reported as ready", summary.String())
	}
}

func TestUpRejectsRepositoryOutsideTheOperatingSystemHome(t *testing.T) {
	repository := appRepository(t)
	t.Setenv("HOME", t.TempDir())

	lifecycle := &lifecycleStub{}
	application := App{
		HostChecker:    passingHostChecker(),
		MachineManager: lifecycle,
	}

	err := application.Up(context.Background(), repository, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "outside the mounted home directory") {
		t.Fatalf("Up() error = %v, want out-of-home rejection", err)
	}
	if len(lifecycle.upRequests) != 0 {
		t.Fatalf("up requests = %+v, want no lifecycle mutation", lifecycle.upRequests)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("broken pipe")
}

func TestUpReportsOutputFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		warningOutput io.Writer
		output        io.Writer
		want          string
	}{
		{
			name:          "warning",
			warningOutput: failingWriter{},
			output:        io.Discard,
			want:          "write full-home mount warning",
		},
		{
			name:   "summary",
			output: failingWriter{},
			want:   "write up summary",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repository := appRepository(t)
			application := App{
				HostChecker:    passingHostChecker(),
				MachineManager: &lifecycleStub{},
				HomeDir:        filepath.Dir(repository),
				WarningOutput:  test.warningOutput,
			}

			err := application.Up(context.Background(), repository, test.output)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Up() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestStopAndDestroyRouteOnlyResolvedMachine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		operation func(App, context.Context, string) error
		wantStop  bool
	}{
		{
			name: "stop",
			operation: func(application App, ctx context.Context, path string) error {
				return application.Stop(ctx, path)
			},
			wantStop: true,
		},
		{
			name: "destroy",
			operation: func(application App, ctx context.Context, path string) error {
				return application.Destroy(ctx, path)
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repository := appRepository(t)
			lifecycle := &lifecycleStub{}
			application := App{
				HostChecker:    passingHostChecker(),
				MachineManager: lifecycle,
			}
			resolved, err := project.Resolve(repository)
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}

			if err := test.operation(application, context.Background(), repository); err != nil {
				t.Fatalf("%s() error = %v", test.name, err)
			}
			if test.wantStop {
				if len(lifecycle.stopped) != 1 ||
					!strings.HasPrefix(lifecycle.stopped[0].MachineName, "isolated-dev-") ||
					lifecycle.stopped[0].ProjectPath != resolved.Path {
					t.Fatalf("stopped = %#v, want one resolved machine", lifecycle.stopped)
				}
				if len(lifecycle.destroyed) != 0 {
					t.Fatalf("destroyed = %#v, want none", lifecycle.destroyed)
				}
			} else {
				if len(lifecycle.destroyed) != 1 ||
					!strings.HasPrefix(lifecycle.destroyed[0].MachineName, "isolated-dev-") ||
					lifecycle.destroyed[0].ProjectPath != resolved.Path {
					t.Fatalf("destroyed = %#v, want one resolved machine", lifecycle.destroyed)
				}
				if len(lifecycle.stopped) != 0 {
					t.Fatalf("stopped = %#v, want none", lifecycle.stopped)
				}
			}
		})
	}
}

func TestStopReportsMutationBoundaryFailures(t *testing.T) {
	t.Parallel()

	repository := appRepository(t)
	tests := []struct {
		name        string
		path        string
		application App
		want        string
	}{
		{
			name: "resolve",
			path: "",
			want: "project path",
		},
		{
			name:        "host",
			path:        repository,
			application: App{HostChecker: failingHostChecker()},
			want:        "not configured",
		},
		{
			name:        "manager missing",
			path:        repository,
			application: App{HostChecker: passingHostChecker()},
			want:        "lifecycle is not configured",
		},
		{
			name: "manager error",
			path: repository,
			application: App{
				HostChecker: passingHostChecker(),
				MachineManager: &lifecycleStub{
					stopErr: errors.New("stop failed"),
				},
			},
			want: "stop failed",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.application.Stop(context.Background(), test.path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Stop() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestDestroyPropagatesManagerFailure(t *testing.T) {
	t.Parallel()

	application := App{
		HostChecker: passingHostChecker(),
		MachineManager: &lifecycleStub{
			destroyErr: errors.New("delete failed"),
		},
	}

	err := application.Destroy(context.Background(), appRepository(t))
	if err == nil || !strings.Contains(err.Error(), "delete failed") {
		t.Fatalf("Destroy() error = %v, want manager failure", err)
	}
}

func TestStatusUsesStoredLifecycleState(t *testing.T) {
	t.Parallel()

	repository := appRepository(t)
	if err := os.WriteFile(
		filepath.Join(repository, ".isolated-dev.toml"),
		[]byte("version = 1\n[resources]\ncpus = 6\nmemory_gb = 12\n"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	resolved, err := project.Resolve(repository)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	store := state.Store{Root: t.TempDir()}
	if err := store.Save(state.Project{
		SchemaVersion: 1,
		ProjectPath:   resolved.Path,
		MachineName:   resolved.MachineName,
		BaseImage:     "local/custom-base:7",
		MountScope:    "repository",
		CPUs:          4,
		MemoryGB:      8,
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	application := App{
		Version:     "test",
		HostChecker: passingHostChecker(),
		StateStore:  store,
	}
	var output bytes.Buffer

	if err := application.Status(context.Background(), repository, &output); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	for _, want := range []string{
		"(unknown)",
		"Base image: local/custom-base:7",
		"Mount scope: repository",
		"Tunnel: unknown",
		"Resources: 4 CPU, 8 GB memory",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("status output missing %q:\n%s", want, output.String())
		}
	}
	if strings.Contains(output.String(), "Resources: 6 CPU, 12 GB memory") {
		t.Errorf("status reports desired resources as applied:\n%s", output.String())
	}
}

func TestStatusReportsBoundaryFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		projectPath func(*testing.T) string
		application func(*testing.T, string) App
		want        string
	}{
		{
			name:        "resolve",
			projectPath: func(*testing.T) string { return "" },
			application: func(*testing.T, string) App { return App{} },
			want:        "project path",
		},
		{
			name: "configuration",
			projectPath: func(t *testing.T) string {
				repository := appRepository(t)
				if err := os.WriteFile(
					filepath.Join(repository, ".isolated-dev.toml"),
					[]byte("version = 2\n"),
					0o600,
				); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
				return repository
			},
			application: func(*testing.T, string) App { return App{} },
			want:        "unsupported value",
		},
		{
			name:        "host",
			projectPath: appRepository,
			application: func(*testing.T, string) App { return App{HostChecker: failingHostChecker()} },
			want:        "not configured",
		},
		{
			name:        "state load",
			projectPath: appRepository,
			application: func(t *testing.T, _ string) App {
				rootFile := filepath.Join(t.TempDir(), "not-a-directory")
				if err := os.WriteFile(rootFile, []byte("x"), 0o600); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
				return App{
					HostChecker: passingHostChecker(),
					StateStore:  state.Store{Root: rootFile},
				}
			},
			want: "load project state",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := test.projectPath(t)
			err := test.application(t, path).Status(context.Background(), path, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Status() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestImageVersion(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"registry.example/base:2":        "2",
		"registry.example:5000/base":     "registry.example:5000/base",
		"registry.example/base:":         "registry.example/base:",
		"registry.example/base@sha256:x": "x",
	}
	for reference, want := range tests {
		if got := imageVersion(reference); got != want {
			t.Errorf("imageVersion(%q) = %q, want %q", reference, got, want)
		}
	}
}
