package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadNeverDiscoversRepositoryComposeFilesAsCommands(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	for _, name := range []string{"docker-compose.yml", "compose.yaml", "Makefile"} {
		writeTestFile(t, filepath.Join(projectDir, name), "services:\n  web:\n    image: nginx\n")
	}

	got, err := Load(projectDir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(got.Commands) != 0 {
		t.Errorf("Commands = %#v, want none inferred from repository content", got.Commands)
	}
}

func TestLoadKeepsExplicitlyDeclaredCommands(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	writeTestFile(t, filepath.Join(projectDir, SharedFileName), `
version = 1

[commands.dev]
args = ["docker", "compose", "--profile", "dev", "up", "-d"]
compose = true

[commands.test]
args = ["npm", "test"]
workdir = "services/api"
`)

	got, err := Load(projectDir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	dev, ok := got.Commands["dev"]
	if !ok {
		t.Fatalf("Commands = %#v, want the declared dev command", got.Commands)
	}
	if !dev.Compose {
		t.Errorf("dev.Compose = false, want the declared Compose flag")
	}
	if got.Commands["test"].Workdir != "services/api" {
		t.Errorf("test.Workdir = %q, want services/api", got.Commands["test"].Workdir)
	}
}

func TestLoadRejectsUnusableCommandDeclarations(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name:    "missing arguments",
			content: "version = 1\n\n[commands.dev]\nargs = []\n",
			wantErr: "commands.dev.args: must not be empty",
		},
		{
			name:    "blank program",
			content: "version = 1\n\n[commands.dev]\nargs = [\"  \"]\n",
			wantErr: "commands.dev.args[0]",
		},
		{
			name:    "environment assignment instead of a program",
			content: "version = 1\n\n[commands.dev]\nargs = [\"DEBUG=1\", \"npm\", \"test\"]\n",
			wantErr: "commands.dev.args[0]",
		},
		{
			name:    "absolute workdir",
			content: "version = 1\n\n[commands.dev]\nargs = [\"npm\"]\nworkdir = \"/etc\"\n",
			wantErr: "commands.dev.workdir: must be a project-relative path",
		},
		{
			name:    "workdir escaping the project",
			content: "version = 1\n\n[commands.dev]\nargs = [\"npm\"]\nworkdir = \"../other\"\n",
			wantErr: "commands.dev.workdir: must stay inside the project",
		},
		{
			name:    "unusable command name",
			content: "version = 1\n\n[commands]\n\"npm test\" = { args = [\"npm\", \"test\"] }\n",
			wantErr: "commands.\"npm test\"",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			projectDir := t.TempDir()
			writeTestFile(t, filepath.Join(projectDir, SharedFileName), testCase.content)

			_, err := Load(projectDir)
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("Load() error = %v, want it to mention %q", err, testCase.wantErr)
			}
		})
	}
}

func TestCommandNamesAreSortedForStableDiagnostics(t *testing.T) {
	t.Parallel()

	cfg := Config{Commands: map[string]Command{
		"test":  {Args: []string{"npm", "test"}},
		"dev":   {Args: []string{"npm", "run", "dev"}},
		"build": {Args: []string{"npm", "run", "build"}},
	}}
	got := strings.Join(cfg.CommandNames(), " ")
	if got != "build dev test" {
		t.Errorf("CommandNames() = %q, want %q", got, "build dev test")
	}
	if names := (Config{}).CommandNames(); len(names) != 0 {
		t.Errorf("CommandNames() = %#v, want none", names)
	}
}
