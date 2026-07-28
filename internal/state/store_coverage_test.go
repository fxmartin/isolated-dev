package state

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultStoreUsesUserConfigurationDirectory(t *testing.T) {
	store, err := DefaultStore()
	if err != nil {
		t.Fatalf("DefaultStore() error = %v", err)
	}
	if !strings.HasSuffix(store.Root, filepath.Join("isolated-dev", "projects")) {
		t.Fatalf("DefaultStore() Root = %q, want isolated-dev/projects suffix", store.Root)
	}
}

func TestStoreRejectsInvalidNamesAndSchemas(t *testing.T) {
	t.Parallel()

	store := Store{Root: t.TempDir()}
	invalidNames := []string{"", ".", "..", "../escape", `folder\escape`}
	for _, name := range invalidNames {
		name := name
		t.Run("name "+name, func(t *testing.T) {
			t.Parallel()
			if err := store.Save(Project{SchemaVersion: 1, MachineName: name}); err == nil {
				t.Fatalf("Save(%q) error = nil, want invalid name", name)
			}
			if _, err := store.Load(name); err == nil {
				t.Fatalf("Load(%q) error = nil, want invalid name", name)
			}
			if err := store.Delete(name); err == nil {
				t.Fatalf("Delete(%q) error = nil, want invalid name", name)
			}
		})
	}

	if err := store.Save(Project{SchemaVersion: 2, MachineName: "safe-machine"}); err == nil {
		t.Fatal("Save() error = nil, want unsupported schema")
	}
}

func TestStoreReportsFilesystemFailures(t *testing.T) {
	t.Parallel()

	rootFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(rootFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	store := Store{Root: rootFile}
	project := Project{SchemaVersion: 1, MachineName: "safe-machine"}

	if err := store.Save(project); err == nil || !strings.Contains(err.Error(), "create state directory") {
		t.Fatalf("Save() error = %v, want state directory failure", err)
	}
	if _, err := store.Load(project.MachineName); err == nil || !strings.Contains(err.Error(), "read project state") {
		t.Fatalf("Load() error = %v, want read failure", err)
	}
	if err := store.Delete(project.MachineName); err == nil || !strings.Contains(err.Error(), "delete project state") {
		t.Fatalf("Delete() error = %v, want delete failure", err)
	}
}

func TestStoreRejectsCorruptAndUnsupportedState(t *testing.T) {
	t.Parallel()

	store := Store{Root: t.TempDir()}
	machineName := "safe-machine"
	statePath := store.path(machineName)

	if err := os.WriteFile(statePath, []byte("{"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := store.Load(machineName); err == nil || !strings.Contains(err.Error(), "decode project state") {
		t.Fatalf("Load() error = %v, want decode failure", err)
	}

	if err := os.WriteFile(statePath, []byte(`{"schema_version":2}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := store.Load(machineName); err == nil || !strings.Contains(err.Error(), "unsupported state schema") {
		t.Fatalf("Load() error = %v, want schema failure", err)
	}
}

func TestStoreDeleteReportsNonMissingRemoveError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := Store{Root: root}
	machineName := "safe-machine"
	if err := os.Mkdir(store.path(machineName), 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.path(machineName), "child"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := store.Delete(machineName)
	if err == nil || !strings.Contains(err.Error(), "delete project state") {
		t.Fatalf("Delete() error = %v, want non-empty directory failure", err)
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete() error = %v, must not report missing state", err)
	}
}

// The atomic rename is the last step of a save. When the destination cannot be
// replaced, the failure must surface and the temporary file must not be left
// behind for a later load to pick up.
func TestStoreSaveReportsReplacementFailure(t *testing.T) {
	t.Parallel()

	store := Store{Root: t.TempDir()}
	machineName := "safe-machine"
	// A non-empty directory at the destination cannot be replaced by a rename.
	if err := os.Mkdir(store.path(machineName), 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(store.path(machineName), "blocker"),
		[]byte("x"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := store.Save(Project{SchemaVersion: 1, MachineName: machineName})
	if err == nil || !strings.Contains(err.Error(), "replace project state") {
		t.Fatalf("Save() error = %v, want a replacement failure", err)
	}

	entries, err := os.ReadDir(store.Root)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("state directory entries = %d, want only the untouched destination", len(entries))
	}
}

// Without a resolvable configuration directory there is nowhere to record
// project state, and that must be reported rather than defaulting to the
// working directory.
func TestDefaultStoreReportsAnUnresolvableConfigurationDirectory(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	if _, err := DefaultStore(); err == nil ||
		!strings.Contains(err.Error(), "resolve user configuration directory") {
		t.Fatalf("DefaultStore() error = %v, want a configuration directory failure", err)
	}
}
