package forge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fxmartin/isolated-dev/internal/config"
)

func TestDevServicesDescribeTheFourDevProfileServices(t *testing.T) {
	services := DevServices()
	if len(services) != 4 {
		t.Fatalf("DevServices() = %d services, want the database, backend, worker, and frontend", len(services))
	}

	containers := make(map[string]struct{}, len(services))
	for _, service := range services {
		if service.Container == "" || service.Description == "" {
			t.Errorf("service %+v is missing its container or description", service)
		}
		if _, exists := containers[service.Container]; exists {
			t.Errorf("container %q is claimed by two services", service.Container)
		}
		containers[service.Container] = struct{}{}
	}
	// PostgreSQL 16 is named by the acceptance criteria, so the run verifies the
	// major version rather than trusting the service name.
	if services[0].ImagePrefix != "postgres:16" {
		t.Errorf("database ImagePrefix = %q, want the pinned PostgreSQL 16 image", services[0].ImagePrefix)
	}
}

func TestDevEndpointsAreTheDocumentedMacOSPorts(t *testing.T) {
	endpoints := DevEndpoints()
	if len(endpoints) != 2 {
		t.Fatalf("DevEndpoints() = %d endpoints, want the frontend and the backend health endpoint", len(endpoints))
	}
	frontend, backend := endpoints[0], endpoints[1]
	if frontend.HostPort != 3001 || frontend.GuestPort != 3001 {
		t.Errorf("frontend = %+v, want macOS localhost 3001 forwarded to guest 3001", frontend)
	}
	if backend.HostPort != 8001 || backend.GuestPort != 8001 {
		t.Errorf("backend = %+v, want macOS localhost 8001 forwarded to guest 8001", backend)
	}
	if backend.Path != "/health" {
		t.Errorf("backend path = %q, want the health endpoint", backend.Path)
	}
	if frontend.URL() != "http://127.0.0.1:3001/" {
		t.Errorf("frontend URL = %q, want the macOS loopback URL", frontend.URL())
	}
}

func TestVerifyDevCommandAcceptsTheDeclaredDevProfileCommand(t *testing.T) {
	for name, args := range map[string][]string{
		"short flag": {"docker", "compose", "--profile", "dev", "up", "-d"},
		"long flag":  {"docker", "compose", "--profile", "dev", "up", "--detach"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := VerifyDevCommand("dev", config.Command{Args: args, Compose: true}); err != nil {
				t.Errorf("VerifyDevCommand(%v) error = %v, want it accepted", args, err)
			}
		})
	}
}

func TestVerifyDevCommandRejectsAnythingElse(t *testing.T) {
	canonical := append([]string(nil), DevCommandArgs...)
	tests := map[string]struct {
		command config.Command
		want    string
	}{
		"no arguments": {
			command: config.Command{Compose: true},
			want:    "declares no arguments",
		},
		"different profile": {
			command: config.Command{
				Args:    []string{"docker", "compose", "--profile", "prod", "up", "-d"},
				Compose: true,
			},
			want: "docker compose --profile dev up -d",
		},
		// An added Compose file is the one thing the acceptance run may not do:
		// it would no longer be the repository's own stack that started.
		"overridden compose file": {
			command: config.Command{
				Args: []string{
					"docker", "compose", "--file", "docker-compose.override.yml",
					"--profile", "dev", "up", "-d",
				},
				Compose: true,
			},
			want: "docker compose --profile dev up -d",
		},
		"missing compose flag": {
			command: config.Command{Args: canonical},
			want:    "compose = true",
		},
		"workdir below the project": {
			command: config.Command{Args: canonical, Compose: true, Workdir: "services"},
			want:    "workdir",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := VerifyDevCommand("dev", test.command)
			if err == nil {
				t.Fatalf("VerifyDevCommand(%+v) error = nil, want it rejected", test.command)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("error = %q, want it to mention %q", err, test.want)
			}
		})
	}
}

func TestComposeDigestReadsTheProjectComposeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ComposeFileName)
	if err := os.WriteFile(path, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	digest, err := ComposeDigest(dir)
	if err != nil {
		t.Fatalf("ComposeDigest() error = %v", err)
	}
	if len(digest) != 64 {
		t.Errorf("ComposeDigest() = %q, want a hex SHA-256", digest)
	}

	again, err := ComposeDigest(dir)
	if err != nil {
		t.Fatalf("ComposeDigest() second error = %v", err)
	}
	if again != digest {
		t.Errorf("ComposeDigest() = %q then %q, want a stable digest of an unchanged file", digest, again)
	}

	if err := os.WriteFile(path, []byte("services: {}\n# changed\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	changed, err := ComposeDigest(dir)
	if err != nil {
		t.Fatalf("ComposeDigest() error = %v", err)
	}
	if changed == digest {
		t.Error("ComposeDigest() is unchanged after the Compose file changed")
	}
}

func TestComposeDigestReportsAMissingComposeFile(t *testing.T) {
	_, err := ComposeDigest(t.TempDir())
	if err == nil {
		t.Fatal("ComposeDigest() error = nil, want the missing Compose file reported")
	}
	if !strings.Contains(err.Error(), ComposeFileName) {
		t.Errorf("error = %q, want it to name %s", err, ComposeFileName)
	}
}
