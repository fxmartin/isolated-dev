package app

import (
	"bytes"
	"context"
	"errors"
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
			_, err := test.application(t).Up(context.Background(), test.projectPath(t))
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

			if err := test.operation(application, context.Background(), repository); err != nil {
				t.Fatalf("%s() error = %v", test.name, err)
			}
			if test.wantStop {
				if len(lifecycle.stopped) != 1 || !strings.HasPrefix(lifecycle.stopped[0], "isolated-dev-") {
					t.Fatalf("stopped = %#v, want one resolved machine", lifecycle.stopped)
				}
				if len(lifecycle.destroyed) != 0 {
					t.Fatalf("destroyed = %#v, want none", lifecycle.destroyed)
				}
			} else {
				if len(lifecycle.destroyed) != 1 || !strings.HasPrefix(lifecycle.destroyed[0], "isolated-dev-") {
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
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("status output missing %q:\n%s", want, output.String())
		}
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
