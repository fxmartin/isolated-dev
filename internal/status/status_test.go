package status

import (
	"bytes"
	"strings"
	"testing"

	"github.com/fxmartin/isolated-dev/internal/config"
)

func TestWriteOmitsSecretReferencesAndValues(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	err := Write(&output, Snapshot{
		CLIVersion:       "1.0.0",
		ContainerVersion: "1.1.0",
		ProjectPath:      "/Users/fx/dev/app",
		MachineName:      "isolated-dev-app-abcd1234",
		MachineStatus:    "running",
		BaseImage:        "isolated-dev-base:1",
		MountScope:       "home",
		TunnelStatus:     "running",
		Config: config.Config{
			Resources: config.Resources{CPUs: 4, MemoryGB: 8},
			Ports:     []config.Port{{Name: "web", Guest: 3000, Host: 3001}},
			Secrets: config.SecretReferences{
				Environment: []string{"SUPER_SECRET"},
				Files:       []string{".env"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	got := output.String()
	for _, expected := range []string{
		"CLI: 1.0.0",
		"Apple Container: 1.1.0",
		"Machine: isolated-dev-app-abcd1234 (running)",
		"Mount scope: home",
		"Resources: 4 CPU, 8 GB memory",
		"web: localhost:3001 -> guest:3000",
	} {
		if !strings.Contains(got, expected) {
			t.Errorf("status output missing %q:\n%s", expected, got)
		}
	}
	if strings.Contains(got, "SUPER_SECRET") || strings.Contains(got, ".env") {
		t.Fatalf("status output exposed secret references:\n%s", got)
	}
	if strings.Contains(got, "disk") {
		t.Fatalf("status output presented unsupported disk allocation:\n%s", got)
	}
}
