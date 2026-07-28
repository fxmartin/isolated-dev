package forge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fxmartin/isolated-dev/internal/projectcmd"
)

// interceptGuest replaces the answer to one guest command while leaving the
// rest of the machine behaving normally.
func (test *persistenceHarness) interceptGuest(
	fragment string,
	respond func(recordedCall) ([]byte, error),
) {
	previous := test.runner.respond
	test.runner.respond = func(call recordedCall) ([]byte, error) {
		if strings.Contains(call.line(), fragment) {
			return respond(call)
		}
		return previous(call)
	}
}

func TestPersistenceValidateRejectsAnUndeclaredDevCommand(t *testing.T) {
	test := newPersistenceHarness(t)
	test.request.CommandName = "start"

	_, err := test.validate(t)
	if err == nil || !strings.Contains(err.Error(), "start") {
		t.Fatalf("Validate() error = %v, want the undeclared command reported", err)
	}
}

func TestPersistenceValidateRequiresVolumesToCheck(t *testing.T) {
	test := newPersistenceHarness(t)
	test.request.Volumes = []Volume{}

	_, err := test.validate(t)
	if err == nil || !strings.Contains(err.Error(), "named volumes") {
		t.Fatalf("Validate() error = %v, want the empty volume list reported", err)
	}
}

func TestPersistenceValidateReportsVolumesThatCannotBeRead(t *testing.T) {
	tests := map[string]struct {
		respond func(recordedCall) ([]byte, error)
		want    string
	}{
		"inspect fails": {
			respond: func(recordedCall) ([]byte, error) {
				return []byte("Error: No such volume: rosetta-db-dev"), errors.New("exit status 1")
			},
			want: "No such volume",
		},
		"a volume is missing from the answer": {
			respond: func(recordedCall) ([]byte, error) {
				return []byte("local /var/lib/docker/volumes/rosetta-db-dev/_data " + testCreatedAt + "\n"), nil
			},
			want: "want 2",
		},
		"the identity cannot be read": {
			respond: func(recordedCall) ([]byte, error) {
				return []byte("local\nlocal\n"), nil
			},
			want: "could not read the identity",
		},
	}
	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			test := newPersistenceHarness(t)
			test.interceptGuest("docker volume inspect", testCase.respond)

			_, err := test.validate(t)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("Validate() error = %v, want it to contain %q", err, testCase.want)
			}
		})
	}
}

func TestPersistenceValidateReportsAVolumeThatCannotBeListed(t *testing.T) {
	test := newPersistenceHarness(t)
	test.interceptGuest(" ls -A ", func(recordedCall) ([]byte, error) {
		return []byte("ls: cannot open directory"), errors.New("exit status 2")
	})

	_, err := test.validate(t)
	if err == nil || !strings.Contains(err.Error(), "rosetta-db-dev") {
		t.Fatalf("Validate() error = %v, want the unlistable volume named", err)
	}
}

func TestPersistenceValidateReportsAVolumeThatMoved(t *testing.T) {
	test := newPersistenceHarness(t)
	test.lifecycle.onUp = func() {
		fixture := test.state.volumes["rosetta-db-dev"]
		fixture.driver = "overlay"
		test.state.volumes["rosetta-db-dev"] = fixture
	}

	_, err := test.validate(t)
	if err == nil || !strings.Contains(err.Error(), "different volume") {
		t.Fatalf("Validate() error = %v, want the replaced volume reported", err)
	}
}

func TestPersistenceValidateReportsAStackThatDoesNotComeBack(t *testing.T) {
	test := newPersistenceHarness(t)
	test.lifecycle.onUp = func() {
		test.executor.respond = func(projectcmd.Streams) (int, error) {
			return 0, errors.New("container machine run failed")
		}
	}

	_, err := test.validate(t)
	if err == nil || !strings.Contains(err.Error(), "did not come back") {
		t.Fatalf("Validate() error = %v, want the stack that stayed down reported", err)
	}
}

func TestPersistenceValidateReportsAGuestThatCannotReadTheMarkerAtAll(t *testing.T) {
	test := newPersistenceHarness(t)
	test.interceptGuest(" cat ", func(recordedCall) ([]byte, error) {
		return []byte("cat: Permission denied"), errors.New("exit status 1")
	})

	_, err := test.validate(t)
	if err == nil || !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("Validate() error = %v, want the failed guest read reported", err)
	}
}

func TestPersistenceValidateReportsAGuestThatCannotCreateAFile(t *testing.T) {
	test := newPersistenceHarness(t)
	test.interceptGuest(" cp ", func(recordedCall) ([]byte, error) {
		return []byte("cp: cannot create regular file"), errors.New("exit status 1")
	})

	_, err := test.validate(t)
	if err == nil || !strings.Contains(err.Error(), "cannot create regular file") {
		t.Fatalf("Validate() error = %v, want the failed guest write reported", err)
	}
}

// A guest file macOS cannot see at all is the mount failing in the direction
// the developer notices last.
func TestPersistenceValidateReportsAGuestFileMacOSCannotSee(t *testing.T) {
	test := newPersistenceHarness(t)
	test.interceptGuest(" cp ", func(recordedCall) ([]byte, error) { return nil, nil })

	_, err := test.validate(t)
	if err == nil || !strings.Contains(err.Error(), "read the file Linux created") {
		t.Fatalf("Validate() error = %v, want the invisible guest file reported", err)
	}
}

func TestPersistenceValidateReportsAGuestFileThatDiffers(t *testing.T) {
	test := newPersistenceHarness(t)
	test.interceptGuest(" cp ", func(call recordedCall) ([]byte, error) {
		destination := call.args[len(call.args)-1]
		return nil, os.WriteFile(destination, []byte("something else\n"), 0o644)
	})

	_, err := test.validate(t)
	if err == nil || !strings.Contains(err.Error(), "something else") {
		t.Fatalf("Validate() error = %v, want the differing content reported", err)
	}
}

func TestPersistenceValidateReportsUnreadableGuestOwnership(t *testing.T) {
	tests := map[string]struct {
		respond func(recordedCall) ([]byte, error)
		want    string
	}{
		"stat fails": {
			respond: func(recordedCall) ([]byte, error) {
				return []byte("stat: No such file"), errors.New("exit status 1")
			},
			want: "No such file",
		},
		"no separator": {
			respond: func(recordedCall) ([]byte, error) { return []byte("501\n"), nil },
			want:    "want uid:gid",
		},
		"unreadable uid": {
			respond: func(recordedCall) ([]byte, error) { return []byte("fx:20\n"), nil },
			want:    "want uid:gid",
		},
		"unreadable gid": {
			respond: func(recordedCall) ([]byte, error) { return []byte("501:staff\n"), nil },
			want:    "want uid:gid",
		},
	}
	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			test := newPersistenceHarness(t)
			test.interceptGuest(" stat ", testCase.respond)

			_, err := test.validate(t)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("Validate() error = %v, want it to contain %q", err, testCase.want)
			}
		})
	}
}

func TestPersistenceValidateReportsATunnelThatCannotBeInspected(t *testing.T) {
	test := newPersistenceHarness(t)
	test.persistence.Tunnels = &stateTunnel{
		state:          test.state,
		errWhenStopped: errors.New("record is unreadable"),
	}

	_, err := test.validate(t)
	if err == nil || !strings.Contains(err.Error(), "record is unreadable") {
		t.Fatalf("Validate() error = %v, want the unreadable tunnel reported", err)
	}
}

// The volumes are read again once the stack is back, and that reading can fail
// on its own.
func TestPersistenceValidateReportsVolumesThatCannotBeReadAfterTheRestart(t *testing.T) {
	test := newPersistenceHarness(t)
	test.lifecycle.onUp = func() {
		test.interceptGuest("docker volume inspect", func(recordedCall) ([]byte, error) {
			return []byte("Cannot connect to the Docker daemon"), errors.New("exit status 1")
		})
	}

	_, err := test.validate(t)
	if err == nil || !strings.Contains(err.Error(), "Cannot connect to the Docker daemon") {
		t.Fatalf("Validate() error = %v, want the failed re-inspection reported", err)
	}
}

// A caller that gives up while the ports are still being watched is not made to
// sit through the rest of the budget.
func TestPersistenceValidateStopsWhenTheCallerGivesUp(t *testing.T) {
	test := newPersistenceHarness(t)
	test.prober.answerWhenStopped = true
	ctx, cancel := context.WithCancel(context.Background())
	test.persistence.Sleep = func(time.Duration) { cancel() }

	if _, err := test.persistence.Validate(ctx, test.request); !errors.Is(err, context.Canceled) {
		t.Fatalf("Validate() error = %v, want the cancellation", err)
	}
}

// The zero-valued knobs are what a caller that only supplies dependencies gets,
// so they have to work on their own.
func TestPersistenceValidateRunsWithDefaultTimingAndOutput(t *testing.T) {
	test := newPersistenceHarness(t)
	test.persistence.Now = nil
	test.persistence.ClosureTries = 0
	test.persistence.Output = nil

	report, err := test.validate(t)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	// The stubbed restart is instant, so both measured intervals meet their
	// targets against the real clock.
	if missed := report.MissedTargets(); len(missed) != 0 {
		t.Errorf("MissedTargets() = %+v, want none", missed)
	}
	if _, err := os.Stat(filepath.Join(test.projectDir, DefaultMarkerName+guestCopySuffix)); !os.IsNotExist(err) {
		t.Errorf("Stat(guest marker) error = %v, want it removed", err)
	}
}

func TestPersistenceValidateReportsAMarkerNameThatIsNotAFileName(t *testing.T) {
	for _, name := range []string{".", "..", "nested/marker", " "} {
		t.Run(name, func(t *testing.T) {
			test := newPersistenceHarness(t)
			test.request.MarkerName = name

			_, err := test.validate(t)
			if err == nil || !strings.Contains(err.Error(), "marker") {
				t.Fatalf("Validate() error = %v, want the rejected marker name", err)
			}
		})
	}
}
