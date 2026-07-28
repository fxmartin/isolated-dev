package state

import (
	"os"
	"path/filepath"
	"strings"
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

// Machines created before guest provisioning existed have no guest fields;
// their state must still decode and must not report a fabricated identity.
func TestStoreRoundTripsGuestIdentityAndToleratesItsAbsence(t *testing.T) {
	t.Parallel()

	store := Store{Root: t.TempDir()}
	want := Project{
		SchemaVersion:    1,
		ProjectPath:      "/Users/fx/dev/app",
		MachineName:      "isolated-dev-app-abcd1234",
		BaseImage:        "isolated-dev-base:1",
		BaseImageVersion: "1",
		MountScope:       "home",
		GuestUser:        "fx",
		GuestUID:         501,
		GuestGID:         20,
		GuestProjectPath: "/Users/fx/dev/app",
	}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := store.Load(want.MachineName)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got != want {
		t.Fatalf("Load() = %+v, want %+v", got, want)
	}

	legacy := `{"schema_version":1,"project_path":"/Users/fx/dev/app",` +
		`"machine_name":"isolated-dev-legacy","base_image":"isolated-dev-base:1",` +
		`"base_image_version":"1","mount_scope":"home","cpus":4,"memory_gb":8}`
	if err := os.WriteFile(
		filepath.Join(store.Root, "isolated-dev-legacy.json"),
		[]byte(legacy),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	loaded, err := store.Load("isolated-dev-legacy")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.GuestUser != "" || loaded.GuestUID != 0 || loaded.GuestProjectPath != "" {
		t.Errorf("legacy state = %+v, want no guest identity", loaded)
	}
}

// The managed SSH host is rebuilt from the recorded address on every `up`, so
// the address has to survive a round trip. Machines created before SSH access
// existed carry no address and must still load.
func TestStoreRoundTripsTheSSHAddressAndToleratesItsAbsence(t *testing.T) {
	t.Parallel()

	store := Store{Root: t.TempDir()}
	want := Project{
		SchemaVersion: 1,
		ProjectPath:   "/Users/fx/dev/app",
		MachineName:   "isolated-dev-app-abcd1234",
		BaseImage:     "isolated-dev-base:1",
		MountScope:    "repository",
		SSHAddress:    "192.168.64.5",
	}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := store.Load(want.MachineName)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got != want {
		t.Fatalf("Load() = %+v, want %+v", got, want)
	}

	unreachable := want
	unreachable.MachineName = "isolated-dev-app-eeee0000"
	unreachable.SSHAddress = ""
	if err := store.Save(unreachable); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	// An address that was never resolved is omitted rather than recorded as an
	// empty host name that would render an unusable SSH block.
	encoded, err := os.ReadFile(filepath.Join(store.Root, unreachable.MachineName+".json"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if strings.Contains(string(encoded), "ssh_address") {
		t.Errorf("state = %s, want no recorded address", encoded)
	}
	loaded, err := store.Load(unreachable.MachineName)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.SSHAddress != "" {
		t.Errorf("SSHAddress = %q, want it absent", loaded.SSHAddress)
	}
}

func TestStoreDeleteIsIdempotent(t *testing.T) {
	t.Parallel()

	store := Store{Root: t.TempDir()}
	project := Project{
		SchemaVersion: 1,
		ProjectPath:   "/Users/fx/dev/app",
		MachineName:   "isolated-dev-app-abcd1234",
	}
	if err := store.Save(project); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if err := store.Delete(project.MachineName); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if err := store.Delete(project.MachineName); err != nil {
		t.Fatalf("second Delete() error = %v, want idempotent cleanup", err)
	}
	if _, err := store.Load(project.MachineName); err != ErrNotFound {
		t.Fatalf("Load() error = %v, want ErrNotFound", err)
	}
}
