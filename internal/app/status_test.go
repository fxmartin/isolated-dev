package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fxmartin/isolated-dev/internal/host"
	"github.com/fxmartin/isolated-dev/internal/state"
)

func TestStatusReportsUninitializedProjectWithoutWritingState(t *testing.T) {
	t.Parallel()

	repository := t.TempDir()
	if err := os.Mkdir(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	stateRoot := filepath.Join(t.TempDir(), "state")
	var output bytes.Buffer
	application := App{
		Version: "1.2.3",
		HostChecker: host.Checker{
			LookPath: func(name string) (string, error) {
				return "/usr/bin/" + name, nil
			},
			Run: func(context.Context, string, ...string) ([]byte, error) {
				return []byte("container CLI version 1.1.0"), nil
			},
		},
		StateStore: state.Store{Root: stateRoot},
	}

	if err := application.Status(context.Background(), repository, &output); err != nil {
		t.Fatalf("Status() error = %v", err)
	}

	got := output.String()
	if !strings.Contains(got, "Machine: isolated-dev-") || !strings.Contains(got, "(not-created)") {
		t.Fatalf("status output missing uninitialized machine:\n%s", got)
	}
	if _, err := os.Stat(stateRoot); !os.IsNotExist(err) {
		t.Fatalf("Status() wrote state directory; Stat() error = %v", err)
	}
}
