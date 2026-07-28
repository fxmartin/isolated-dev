package forge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fxmartin/isolated-dev/internal/config"
)

const (
	// ComposeFileName is the Compose file the Forge repository already has. The
	// acceptance run reads it to prove it stayed as it is and never writes it.
	ComposeFileName = "docker-compose.yml"
	// DevProfile is the Compose profile the DEV stack lives in.
	DevProfile = "dev"
)

// DevCommandArgs is the argument vector the project must declare for its DEV
// command. It is the command the acceptance criteria name, unchanged: no
// additional Compose file, no override, no project name of ours.
var DevCommandArgs = []string{"docker", "compose", "--profile", DevProfile, "up", "-d"}

// Service is one DEV service the run expects to find running in the guest.
type Service struct {
	// Container is the container name the Compose file pins for the service,
	// which is how the run addresses it without asking Compose anything.
	Container string
	// Description is how the service is named in the acceptance criteria, so a
	// failure reads as the workload rather than as a container id.
	Description string
	// ImagePrefix, when set, is the image the service must be running. It is what
	// turns "PostgreSQL 16 is running" into something the run actually verifies
	// rather than infers from a service name.
	ImagePrefix string
}

// DevServices are the four services the Forge DEV profile declares.
func DevServices() []Service {
	return []Service{
		{
			Container:   "rosetta-dev-db",
			Description: "PostgreSQL 16",
			ImagePrefix: "postgres:16",
		},
		{
			Container:   "rosetta-dev-backend",
			Description: "the FastAPI backend",
		},
		{
			Container:   "rosetta-dev-worker",
			Description: "the Python worker",
		},
		{
			Container:   "rosetta-dev-frontend",
			Description: "the React/Vite-to-Nginx frontend",
		},
	}
}

// Endpoint is one macOS-side address the DEV stack has to answer on, reached
// through the managed tunnel rather than from inside the guest.
type Endpoint struct {
	Label string
	Path  string
	// HostPort is the macOS loopback port, and GuestPort the port the Compose
	// file publishes inside the machine.
	HostPort  int
	GuestPort int
}

// URL is the macOS address the endpoint is read at. It is loopback by literal
// address: the managed tunnel binds `127.0.0.1`, which a `localhost` that
// resolves to `::1` first would miss.
func (endpoint Endpoint) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", endpoint.HostPort, endpoint.Path)
}

// DevEndpoints are the two macOS addresses the acceptance criteria name.
func DevEndpoints() []Endpoint {
	return []Endpoint{
		{Label: "frontend", Path: "/", HostPort: 3001, GuestPort: 3001},
		{Label: "backend health", Path: "/health", HostPort: 8001, GuestPort: 8001},
	}
}

// VerifyDevCommand confirms the project declares the DEV command as the
// unmodified Compose invocation.
//
// The declaration is what decides which Compose file starts, so it is checked
// before anything runs: an added `--file`, a different profile, or a working
// directory below the project would all start something other than the
// repository's own DEV stack, and the run would prove nothing.
func VerifyDevCommand(name string, command config.Command) error {
	canonical := strings.Join(DevCommandArgs, " ")
	if len(command.Args) == 0 {
		return fmt.Errorf(
			"commands.%s declares no arguments; the Forge acceptance run needs it to be exactly `%s`",
			name,
			canonical,
		)
	}
	if got := strings.Join(normalizeArgs(command.Args), " "); got != canonical {
		return fmt.Errorf(
			"commands.%s runs `%s`, but the Forge acceptance run needs the unmodified `%s`",
			name,
			strings.Join(command.Args, " "),
			canonical,
		)
	}
	if command.Workdir != "" {
		return fmt.Errorf(
			"commands.%s.workdir is %q, but the repository's own Compose file is at the project root, so the command must run there",
			name,
			command.Workdir,
		)
	}
	if !command.Compose {
		return fmt.Errorf(
			"commands.%s must declare compose = true so the guest Docker daemon is confirmed ready before the DEV stack starts",
			name,
		)
	}
	return nil
}

// normalizeArgs folds the spellings of the detach flag together, so a project
// that declares `--detach` is not rejected for a difference Compose does not
// make.
func normalizeArgs(args []string) []string {
	normalized := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--detach" {
			arg = "-d"
		}
		normalized = append(normalized, arg)
	}
	return normalized
}

// ComposeDigest is the SHA-256 of the project's Compose file. Taking it before
// and after the run is what shows the existing file was used as it is.
func ComposeDigest(projectPath string) (string, error) {
	path := filepath.Join(projectPath, ComposeFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read the project %s: %w", ComposeFileName, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
