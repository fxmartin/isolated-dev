package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreSavesAndLoadsProjectState(t *testing.T) {
	t.Parallel()

	store := Store{Root: t.TempDir()}
	want := Project{
		SchemaVersion:    1,
		ProjectPath:      "/Users/fx/dev/app",
		MachineName:      "isolated-dev-app-abcd1234",
		BaseImage:        "isolated-dev-base:1",
		BaseImageVersion: "1",
		MountScope:       "repository",
	}

	if err := store.Save(want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := store.Load(want.MachineName)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got != want {
		t.Errorf("Load() = %+v, want %+v", got, want)
	}

	info, err := os.Stat(filepath.Join(store.Root, want.MachineName+".json"))
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o600 {
		t.Errorf("state mode = %o, want 600", gotMode)
	}
}
