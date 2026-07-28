package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrNotFound = errors.New("project state not found")

type Project struct {
	SchemaVersion    int    `json:"schema_version"`
	ProjectPath      string `json:"project_path"`
	MachineName      string `json:"machine_name"`
	BaseImage        string `json:"base_image"`
	BaseImageVersion string `json:"base_image_version"`
	MountScope       string `json:"mount_scope"`
	CPUs             int    `json:"cpus"`
	MemoryGB         int    `json:"memory_gb"`
	// Guest identity and mounted-project path are recorded after provisioning,
	// so machines created before this state existed decode without them.
	GuestUser        string `json:"guest_user,omitempty"`
	GuestUID         int    `json:"guest_uid,omitempty"`
	GuestGID         int    `json:"guest_gid,omitempty"`
	GuestProjectPath string `json:"guest_project_path,omitempty"`
	// SSHAddress is the machine address the managed SSH host points at. It is
	// reconciled on every `up`, because a restarted machine can move.
	SSHAddress string `json:"ssh_address,omitempty"`
}

type Store struct {
	Root string
}

func DefaultStore() (Store, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return Store{}, fmt.Errorf("resolve user configuration directory: %w", err)
	}
	return Store{Root: filepath.Join(root, "isolated-dev", "projects")}, nil
}

func (store Store) Save(project Project) error {
	if err := validateMachineName(project.MachineName); err != nil {
		return err
	}
	if project.SchemaVersion != 1 {
		return fmt.Errorf("unsupported state schema version %d", project.SchemaVersion)
	}
	if err := os.MkdirAll(store.Root, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	if err := os.Chmod(store.Root, 0o700); err != nil {
		return fmt.Errorf("secure state directory: %w", err)
	}

	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		return fmt.Errorf("encode project state: %w", err)
	}
	data = append(data, '\n')

	temporary, err := os.CreateTemp(store.Root, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary state file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("secure temporary state file: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write project state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync project state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close project state: %w", err)
	}
	if err := os.Rename(temporaryPath, store.path(project.MachineName)); err != nil {
		return fmt.Errorf("replace project state: %w", err)
	}
	return nil
}

func (store Store) Load(machineName string) (Project, error) {
	if err := validateMachineName(machineName); err != nil {
		return Project{}, err
	}
	data, err := os.ReadFile(store.path(machineName))
	if errors.Is(err, os.ErrNotExist) {
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, fmt.Errorf("read project state: %w", err)
	}

	var project Project
	if err := json.Unmarshal(data, &project); err != nil {
		return Project{}, fmt.Errorf("decode project state: %w", err)
	}
	if project.SchemaVersion != 1 {
		return Project{}, fmt.Errorf("unsupported state schema version %d", project.SchemaVersion)
	}
	return project, nil
}

func (store Store) Delete(machineName string) error {
	if err := validateMachineName(machineName); err != nil {
		return err
	}
	if err := os.Remove(store.path(machineName)); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("delete project state: %w", err)
	}
	return nil
}

func (store Store) path(machineName string) string {
	return filepath.Join(store.Root, machineName+".json")
}

func validateMachineName(value string) error {
	if value == "" || strings.ContainsAny(value, `/\`) || value == "." || value == ".." {
		return fmt.Errorf("invalid machine name %q", value)
	}
	return nil
}
