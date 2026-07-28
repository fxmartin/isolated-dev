package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadReturnsDefaultsWithoutConfigurationFiles(t *testing.T) {
	t.Parallel()

	got, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got.BaseImage != DefaultBaseImage {
		t.Errorf("BaseImage = %q, want %q", got.BaseImage, DefaultBaseImage)
	}
	if got.MountTarget != DefaultMountTarget {
		t.Errorf("MountTarget = %q, want %q", got.MountTarget, DefaultMountTarget)
	}
	if got.Resources.CPUs <= 0 || got.Resources.MemoryGB <= 0 {
		t.Errorf("Resources = %+v, want positive defaults", got.Resources)
	}
}

func TestLoadMergesAllowedLocalOverrides(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	writeTestFile(t, filepath.Join(projectDir, SharedFileName), `
version = 1
base_image = "example/base:v2"
mount_target = "/workspace/app"
packages = ["nodejs"]

[resources]
cpus = 4
memory_gb = 8

[[ports]]
name = "web"
guest = 3000
host = 3000
`)
	writeTestFile(t, filepath.Join(projectDir, LocalFileName), `
[resources]
cpus = 6
memory_gb = 12

[[ports]]
name = "web"
host = 3100
`)

	got, err := Load(projectDir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got.BaseImage != "example/base:v2" {
		t.Errorf("BaseImage = %q, want shared value", got.BaseImage)
	}
	if got.Resources.CPUs != 6 || got.Resources.MemoryGB != 12 {
		t.Errorf("Resources = %+v, want merged values", got.Resources)
	}
	if len(got.Ports) != 1 || got.Ports[0].Host != 3100 || got.Ports[0].Guest != 3000 {
		t.Errorf("Ports = %+v, want local host override and shared guest port", got.Ports)
	}
}

func TestLoadRejectsUnsupportedDiskConfiguration(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	writeTestFile(t, filepath.Join(projectDir, SharedFileName), `
[resources]
disk_gb = 64
`)

	_, err := Load(projectDir)
	if err == nil || !strings.Contains(err.Error(), "resources.disk_gb") {
		t.Fatalf("Load() error = %v, want unsupported disk field", err)
	}
}

func TestLoadReportsUnknownLocalField(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	writeTestFile(t, filepath.Join(projectDir, LocalFileName), `
base_image = "not-allowed-locally"
`)

	_, err := Load(projectDir)
	if err == nil {
		t.Fatal("Load() error = nil, want validation error")
	}
	if !strings.Contains(err.Error(), LocalFileName) || !strings.Contains(err.Error(), "base_image") {
		t.Fatalf("Load() error = %q, want file and field", err)
	}
}

func TestLoadRejectsInlineSecretValue(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	writeTestFile(t, filepath.Join(projectDir, SharedFileName), `
[secrets]
environment = ["API_TOKEN"]
files = [".env"]
api_token = "must-not-be-accepted"
`)

	_, err := Load(projectDir)
	if err == nil {
		t.Fatal("Load() error = nil, want validation error")
	}
	if !strings.Contains(err.Error(), SharedFileName) || !strings.Contains(err.Error(), "secrets.api_token") {
		t.Fatalf("Load() error = %q, want file and field without value", err)
	}
	if strings.Contains(err.Error(), "must-not-be-accepted") {
		t.Fatalf("Load() leaked inline secret value: %q", err)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}
