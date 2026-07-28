package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateRejectsUnsafeEffectiveConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{
			name:   "version",
			mutate: func(cfg *Config) { cfg.Version = 2 },
			want:   "version",
		},
		{
			name:   "base image",
			mutate: func(cfg *Config) { cfg.BaseImage = " " },
			want:   "base_image",
		},
		{
			name:   "mount target",
			mutate: func(cfg *Config) { cfg.MountTarget = "workspace" },
			want:   "mount_target",
		},
		{
			name:   "CPUs",
			mutate: func(cfg *Config) { cfg.Resources.CPUs = 0 },
			want:   "resources.cpus",
		},
		{
			name:   "memory",
			mutate: func(cfg *Config) { cfg.Resources.MemoryGB = 0 },
			want:   "resources.memory_gb",
		},
		{
			name: "empty port name",
			mutate: func(cfg *Config) {
				cfg.Ports = []Port{{Guest: 3000, Host: 3000}}
			},
			want: "ports[0].name",
		},
		{
			name: "duplicate port name",
			mutate: func(cfg *Config) {
				cfg.Ports = []Port{
					{Name: "web", Guest: 3000, Host: 3000},
					{Name: "web", Guest: 3001, Host: 3001},
				}
			},
			want: "duplicate",
		},
		{
			name: "invalid guest port",
			mutate: func(cfg *Config) {
				cfg.Ports = []Port{{Name: "web", Guest: 0, Host: 3000}}
			},
			want: ".guest",
		},
		{
			name: "invalid host port",
			mutate: func(cfg *Config) {
				cfg.Ports = []Port{{Name: "web", Guest: 3000, Host: 65536}}
			},
			want: ".host",
		},
		{
			name: "empty command name",
			mutate: func(cfg *Config) {
				cfg.Commands = map[string]Command{" ": {Args: []string{"true"}}}
			},
			want: "must be a name usable as an `isolated-dev run` argument",
		},
		{
			name: "empty command arguments",
			mutate: func(cfg *Config) {
				cfg.Commands = map[string]Command{"test": {}}
			},
			want: "commands.test.args",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cfg := Defaults()
			test.mutate(&cfg)
			err := validate(cfg)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestLoadReportsMalformedAndUnreadableConfiguration(t *testing.T) {
	t.Parallel()

	t.Run("malformed TOML", func(t *testing.T) {
		t.Parallel()
		projectDir := t.TempDir()
		writeTestFile(t, filepath.Join(projectDir, SharedFileName), `version = [`)

		_, err := Load(projectDir)
		if err == nil || !strings.Contains(err.Error(), "invalid TOML") {
			t.Fatalf("Load() error = %v, want invalid TOML", err)
		}
	})

	t.Run("configuration path is directory", func(t *testing.T) {
		t.Parallel()
		projectDir := t.TempDir()
		if err := os.Mkdir(filepath.Join(projectDir, SharedFileName), 0o700); err != nil {
			t.Fatalf("Mkdir() error = %v", err)
		}

		_, err := Load(projectDir)
		if err == nil || !strings.Contains(err.Error(), "read configuration") {
			t.Fatalf("Load() error = %v, want read failure", err)
		}
	})
}

func TestLoadRejectsLocalOverrideWithoutSharedPort(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	writeTestFile(t, filepath.Join(projectDir, LocalFileName), `
[[ports]]
name = "web"
host = 3100
`)

	_, err := Load(projectDir)
	if err == nil || !strings.Contains(err.Error(), "no matching shared port") {
		t.Fatalf("Load() error = %v, want unmatched local port", err)
	}
}
