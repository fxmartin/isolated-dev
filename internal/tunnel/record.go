package tunnel

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// record is the persisted description of one machine's tunnel. It outlives the
// command that wrote it, which is what lets a later run recognise, replace, or
// clean up a process it did not start itself.
type record struct {
	SchemaVersion int       `json:"schema_version"`
	MachineName   string    `json:"machine_name"`
	PID           int       `json:"pid"`
	Address       string    `json:"address"`
	User          string    `json:"user"`
	Forwards      []Forward `json:"forwards"`
	// Unforwarded holds the mappings a macOS port conflict kept out of the
	// tunnel, so `status` can report them long after `up` finished.
	Unforwarded []Forward `json:"unforwarded,omitempty"`
}

// matches reports whether the recorded tunnel already is what the spec asks
// for. A conflict is never a match: the macOS port it blocked may have been
// freed since, and the next reconciliation is what discovers that.
func (existing record) matches(spec Spec) bool {
	return existing.Address == spec.Address &&
		existing.User == spec.User &&
		len(existing.Unforwarded) == 0 &&
		sameForwards(existing.Forwards, spec.Forwards)
}

func (existing record) state(running bool) State {
	return State{
		Running:     running,
		PID:         existing.PID,
		Address:     existing.Address,
		Forwards:    existing.Forwards,
		Unforwarded: existing.Unforwarded,
	}
}

func sameForwards(recorded []Forward, wanted []Forward) bool {
	if len(recorded) != len(wanted) {
		return false
	}
	for index, forward := range recorded {
		if forward != wanted[index] {
			return false
		}
	}
	return true
}

func (manager Manager) load(machineName string) (*record, error) {
	data, err := os.ReadFile(manager.path(machineName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read tunnel state: %w", err)
	}

	var existing record
	if err := json.Unmarshal(data, &existing); err != nil {
		return nil, fmt.Errorf("decode tunnel state for %q: %w", machineName, err)
	}
	if existing.SchemaVersion != recordSchemaVersion {
		return nil, fmt.Errorf(
			"unsupported tunnel state schema version %d for %q",
			existing.SchemaVersion,
			machineName,
		)
	}
	// The recorded name is the marker that keeps a recycled process ID from
	// being signalled, so a record that lost it is not usable.
	if existing.MachineName != machineName {
		return nil, fmt.Errorf(
			"tunnel state for %q records machine %q",
			machineName,
			existing.MachineName,
		)
	}
	return &existing, nil
}

func (manager Manager) save(saved record) error {
	if err := os.MkdirAll(manager.Root, 0o700); err != nil {
		return fmt.Errorf("create tunnel state directory: %w", err)
	}
	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return fmt.Errorf("encode tunnel state: %w", err)
	}
	data = append(data, '\n')

	temporary, err := os.CreateTemp(manager.Root, ".tunnel-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary tunnel state file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("secure temporary tunnel state file: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write tunnel state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync tunnel state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close tunnel state: %w", err)
	}
	if err := os.Rename(temporaryPath, manager.path(saved.MachineName)); err != nil {
		return fmt.Errorf("replace tunnel state: %w", err)
	}
	return nil
}

func (manager Manager) delete(machineName string) error {
	if err := os.Remove(manager.path(machineName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete tunnel state: %w", err)
	}
	return nil
}

func (manager Manager) path(machineName string) string {
	return filepath.Join(manager.Root, machineName+".json")
}
