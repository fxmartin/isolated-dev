package tunnel

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRejectsUnusableRecords(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		content string
		want    string
	}{
		"unsupported schema": {
			content: `{"schema_version":2,"machine_name":"` + testMachine + `"}`,
			want:    "schema version 2",
		},
		// The recorded name is the marker that keeps a recycled process ID from
		// being signalled, so a record that lost it cannot be acted on.
		"foreign machine": {
			content: `{"schema_version":1,"machine_name":"isolated-dev-other-0000ffff"}`,
			want:    "records machine",
		},
	}
	for name, testCase := range cases {
		testCase := testCase
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			manager := testManager(t, newControllerStub())
			if err := os.WriteFile(
				filepath.Join(manager.Root, testMachine+".json"),
				[]byte(testCase.content),
				0o600,
			); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}

			_, err := manager.Inspect(testMachine)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("Inspect() error = %v, want it to name %q", err, testCase.want)
			}
		})
	}
}

// A mapping that changed target has to replace the tunnel even though the
// number of forwarded ports is unchanged.
func TestReconcileReplacesATunnelWhoseMappingChanged(t *testing.T) {
	t.Parallel()

	controller := newControllerStub()
	manager := testManager(t, controller)
	if _, err := manager.Reconcile(testSpec(webForward)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	moved := Forward{Name: "web", Host: 3000, Guest: 4000}
	if _, err := manager.Reconcile(testSpec(moved)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(controller.starts) != 2 {
		t.Fatalf("starts = %v, want the tunnel replaced", controller.starts)
	}
	if !strings.Contains(strings.Join(controller.starts[1], " "), "127.0.0.1:3000:127.0.0.1:4000") {
		t.Errorf("argv = %v, want the new mapping forwarded", controller.starts[1])
	}
}

// A CLI assembled without an explicit controller still reports honestly: the
// default one asks macOS about the recorded process.
func TestInspectUsesTheDefaultProcessController(t *testing.T) {
	t.Parallel()

	manager := Manager{Root: t.TempDir()}
	if err := manager.save(record{
		SchemaVersion: recordSchemaVersion,
		MachineName:   testMachine,
		PID:           4194303,
		Address:       "192.168.64.5",
		User:          "fx",
		Forwards:      []Forward{webForward},
	}); err != nil {
		t.Fatalf("save() error = %v", err)
	}

	state, err := manager.Inspect(testMachine)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if state.Running {
		t.Errorf("state = %+v, want the absent process reported as stopped", state)
	}
}

// A tunnel that can neither be recorded nor stopped is the worst case, and the
// one the operator most needs named: a process is holding macOS ports that no
// later run can find.
func TestReconcileReportsATunnelItCanNeitherRecordNorStop(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { os.Chmod(root, 0o700) })

	controller := newControllerStub()
	controller.stopErr = errors.New("operation not permitted")
	manager := Manager{
		Root:          root,
		Controller:    controller,
		ProbeHostPort: func(int) error { return nil },
	}

	_, err := manager.Reconcile(testSpec(webForward))
	if err == nil || !strings.Contains(err.Error(), "also survived") {
		t.Fatalf("Reconcile() error = %v, want the surviving process reported", err)
	}
}

func TestEveryOperationRequiresAStateDirectory(t *testing.T) {
	t.Parallel()

	manager := Manager{Controller: newControllerStub()}
	operations := map[string]func() error{
		"reconcile": func() error {
			_, err := manager.Reconcile(testSpec(webForward))
			return err
		},
		"remove": func() error { return manager.Remove(testMachine) },
		"inspect": func() error {
			_, err := manager.Inspect(testMachine)
			return err
		},
	}
	for name, operation := range operations {
		operation := operation
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := operation()
			if err == nil || !strings.Contains(err.Error(), "tunnel state directory") {
				t.Fatalf("%s() error = %v, want the missing directory reported", name, err)
			}
		})
	}
}

func TestRemoveReportsAnUndeletableRecord(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := Manager{
		Root:          root,
		Controller:    newControllerStub(),
		ProbeHostPort: func(int) error { return nil },
	}
	if _, err := manager.Reconcile(testSpec(webForward)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { os.Chmod(root, 0o700) })

	err := manager.Remove(testMachine)
	if err == nil || !strings.Contains(err.Error(), "delete tunnel state") {
		t.Fatalf("Remove() error = %v, want the undeletable record reported", err)
	}
}
