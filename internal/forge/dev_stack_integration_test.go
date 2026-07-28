package forge_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/fxmartin/isolated-dev/internal/app"
	"github.com/fxmartin/isolated-dev/internal/baseimage"
	"github.com/fxmartin/isolated-dev/internal/config"
	"github.com/fxmartin/isolated-dev/internal/forge"
	"github.com/fxmartin/isolated-dev/internal/guest"
	"github.com/fxmartin/isolated-dev/internal/host"
	"github.com/fxmartin/isolated-dev/internal/machine"
	"github.com/fxmartin/isolated-dev/internal/project"
	"github.com/fxmartin/isolated-dev/internal/projectcmd"
	"github.com/fxmartin/isolated-dev/internal/smoke"
	"github.com/fxmartin/isolated-dev/internal/sshconfig"
	"github.com/fxmartin/isolated-dev/internal/state"
	"github.com/fxmartin/isolated-dev/internal/tunnel"
	"github.com/fxmartin/isolated-dev/internal/zed"
)

// forgePathVariable overrides where the acceptance workload is checked out. The
// default is the `../forge` the epic names, relative to this repository.
const forgePathVariable = "ISOLATED_DEV_FORGE_PATH"

// TestHostRunsTheUnmodifiedForgeDevStack is the MVP acceptance test: it brings
// up the Forge project machine, invokes the DEV command the project declares —
// `docker compose --profile dev up -d`, against the repository's own Compose
// file — and proves the four DEV services are running and reachable from macOS
// on localhost 3001 and 8001 through the managed tunnel.
//
// Unlike the baseline nested-Compose test it owns nothing and removes nothing:
// Forge is a real repository with real named volumes, and its stack is meant to
// keep running afterwards. Run it only on a development host where starting the
// Forge DEV stack is what you want to happen.
func TestHostRunsTheUnmodifiedForgeDevStack(t *testing.T) {
	if os.Getenv("ISOLATED_DEV_RUN_HOST_TESTS") != "1" {
		t.Skip("set ISOLATED_DEV_RUN_HOST_TESTS=1 to run the Forge DEV acceptance test")
	}
	if runtime.GOOS != "darwin" {
		t.Fatal("the Forge DEV acceptance test requires macOS")
	}
	if _, err := exec.LookPath("container"); err != nil {
		t.Fatalf("find container CLI: %v", err)
	}
	projectPath := forgeRepository(t)

	// The first run builds the backend and frontend images inside the machine,
	// which is the slowest thing this repository does.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	resolved, err := project.Resolve(projectPath)
	if err != nil {
		t.Fatalf("Resolve(%s) error = %v", projectPath, err)
	}
	effectiveConfig, err := config.Load(resolved.Path)
	if err != nil {
		t.Fatalf("Load(%s) error = %v", resolved.Path, err)
	}

	logs := testWriter{t: t}
	application, store := hostApplication(t)
	// `up` reconciles the machine, the guest account, SSH, and the tunnel, and
	// starts no project service: the DEV stack starts only from the declared
	// command below.
	if err := application.Up(ctx, resolved.Path, logs); err != nil {
		t.Fatalf("Up(%s) error = %v", resolved.Path, err)
	}
	stored, err := store.Load(resolved.MachineName)
	if err != nil {
		t.Fatalf("Load(%s) error = %v", resolved.MachineName, err)
	}

	acceptance := forge.Acceptance{
		Commands:    application.ProjectCommands,
		Runner:      baseimage.ExecRunner{},
		Tunnels:     application.Tunnels,
		Prober:      smoke.HTTPProber{},
		RetryDelay:  2 * time.Second,
		Output:      logs,
		Diagnostics: logs,
	}
	result, err := acceptance.Run(ctx, forge.Request{
		ProjectPath:      resolved.Path,
		MachineName:      resolved.MachineName,
		GuestUser:        stored.GuestUser,
		GuestProjectPath: stored.GuestProjectPath,
		CommandName:      devCommandName(t, effectiveConfig),
		Config:           effectiveConfig,
	})
	if result.Architecture != nil {
		// Whether the run passed or failed, an architecture finding is the
		// answer the epic's open question asks for.
		t.Logf("architecture finding: %s", result.Architecture)
	}
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	for _, service := range result.Services {
		if !service.Running {
			t.Errorf("%s (%s) is not running", service.Container, service.Description)
		}
		t.Logf("%s: %s (%s, health %s)", service.Description, service.Container, service.Image, service.Health)
	}
	for _, endpoint := range result.Endpoints {
		t.Logf("%s: %s through %s", endpoint.Label, endpoint.URL(), endpoint.Forward)
		if endpoint.Body == "" {
			t.Errorf("%s at %s answered with nothing", endpoint.Label, endpoint.URL())
		}
	}

	// The Compose file the stack came from is the repository's own, unchanged.
	digest, err := forge.ComposeDigest(resolved.Path)
	if err != nil {
		t.Fatalf("ComposeDigest() error = %v", err)
	}
	if digest != result.ComposeDigest {
		t.Errorf("%s digest = %q, want the unchanged %q", forge.ComposeFileName, digest, result.ComposeDigest)
	}
	if status := gitStatus(t, resolved.Path); status != "" {
		t.Errorf("the acceptance run left changes in the Forge repository:\n%s", status)
	}
}

// forgeRepository resolves the acceptance workload and refuses to guess: an
// acceptance run without the real project proves nothing.
func forgeRepository(t *testing.T) string {
	t.Helper()

	path := os.Getenv(forgePathVariable)
	if path == "" {
		// The package directory is `<repository>/internal/forge`, so the sibling
		// of the repository is three levels up.
		resolved, err := filepath.Abs(filepath.Join("..", "..", "..", "forge"))
		if err != nil {
			t.Fatalf("resolve the default Forge path: %v", err)
		}
		path = resolved
	}
	if _, err := os.Stat(filepath.Join(path, forge.ComposeFileName)); err != nil {
		t.Fatalf(
			"the Forge acceptance workload is not at %s (%v); check it out there or set %s",
			path,
			err,
			forgePathVariable,
		)
	}
	return path
}

// devCommandName finds the declared command that is the Forge DEV command, so
// the acceptance run invokes the project's own name for it.
func devCommandName(t *testing.T, effectiveConfig config.Config) string {
	t.Helper()

	for _, name := range effectiveConfig.CommandNames() {
		if forge.VerifyDevCommand(name, effectiveConfig.Commands[name]) == nil {
			return name
		}
	}
	t.Fatalf(
		"no command in %s declares the Forge DEV stack; add:\n\n[commands.dev]\nargs = [%q, %q, %q, %q, %q, %q]\ncompose = true\n",
		config.SharedFileName,
		"docker", "compose", "--profile", "dev", "up", "-d",
	)
	return ""
}

// hostApplication assembles the CLI exactly as `main` does, so the acceptance
// run exercises the paths a developer uses rather than a test-only assembly.
func hostApplication(t *testing.T) (app.App, state.Store) {
	t.Helper()

	store, err := state.DefaultStore()
	if err != nil {
		t.Fatalf("DefaultStore() error = %v", err)
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir() error = %v", err)
	}
	tunnels, err := tunnel.DefaultManager()
	if err != nil {
		t.Fatalf("DefaultManager() error = %v", err)
	}
	runner := baseimage.ExecRunner{}
	imageManager := &baseimage.Manager{Runner: runner}
	machineManager := &machine.Manager{
		Runner:       runner,
		StateStore:   store,
		DockerWaiter: imageManager,
		ImageEnsurer: imageManager,
	}
	return app.App{
		HostChecker:      host.DefaultChecker(),
		StateStore:       store,
		MachineManager:   machineManager,
		GuestProvisioner: guest.Provisioner{Runner: runner},
		ImageEnsurer:     imageManager,
		AddressResolver:  machineManager,
		SSHConfig:        sshconfig.Manager{SSHDir: filepath.Join(homeDir, ".ssh")},
		Tunnels:          tunnels,
		Zed:              zed.Launcher{Runner: runner},
		ProjectCommands: projectcmd.Executor{
			Runner:       projectcmd.ExecRunner{},
			DockerWaiter: imageManager,
		},
		WarningOutput: testWriter{t: t},
	}, store
}

// gitStatus reports what the acceptance run changed in the Forge repository,
// which has to be nothing at all.
func gitStatus(t *testing.T, projectPath string) string {
	t.Helper()

	output, err := exec.Command("git", "-C", projectPath, "status", "--porcelain").Output()
	if err != nil {
		t.Fatalf("git status error = %v", err)
	}
	return strings.TrimSpace(string(output))
}

// testWriter forwards streamed output and captured diagnostics into the test
// log, where a failing unattended run can still be read afterwards.
type testWriter struct {
	t *testing.T
}

func (writer testWriter) Write(data []byte) (int, error) {
	writer.t.Logf("%s", strings.TrimRight(string(data), "\n"))
	return len(data), nil
}
