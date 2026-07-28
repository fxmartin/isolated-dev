package forge_test

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/fxmartin/isolated-dev/internal/baseimage"
	"github.com/fxmartin/isolated-dev/internal/config"
	"github.com/fxmartin/isolated-dev/internal/forge"
	"github.com/fxmartin/isolated-dev/internal/project"
	"github.com/fxmartin/isolated-dev/internal/smoke"
)

// TestHostKeepsForgeUsableAcrossARestart is the MVP's daily-work acceptance
// test. Where the DEV stack test proves startup, this one proves the day after
// it: the Forge database and application-data volumes survive stopping and
// restarting the project machine, macOS ports 3001 and 8001 answer while no CLI
// is running and stop answering once the machine does, edits cross the mount in
// both directions with usable ownership, and the cached restart is measured
// against the 30-second machine and 2-minute stack targets.
//
// It owns nothing and removes nothing: the machine, the containers, and above
// all the named volumes holding real data are left as they were found, and the
// two marker files it writes into the repository are removed on every path.
// Run it only on a development host where restarting the Forge DEV stack is
// what you want to happen.
func TestHostKeepsForgeUsableAcrossARestart(t *testing.T) {
	if os.Getenv("ISOLATED_DEV_RUN_HOST_TESTS") != "1" {
		t.Skip("set ISOLATED_DEV_RUN_HOST_TESTS=1 to run the Forge persistence test")
	}
	if runtime.GOOS != "darwin" {
		t.Fatal("the Forge persistence test requires macOS")
	}
	if _, err := exec.LookPath("container"); err != nil {
		t.Fatalf("find container CLI: %v", err)
	}
	projectPath := forgeRepository(t)

	// The stack may still need its first build, which is the slowest thing this
	// repository does; the restart it then measures is a cached one.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Minute)
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
	request := forge.PersistenceRequest{
		Request: forge.Request{
			ProjectPath:      resolved.Path,
			MachineName:      resolved.MachineName,
			GuestUser:        stored.GuestUser,
			GuestProjectPath: stored.GuestProjectPath,
			CommandName:      devCommandName(t, effectiveConfig),
			Config:           effectiveConfig,
		},
		GuestUID: stored.GuestUID,
		GuestGID: stored.GuestGID,
	}

	// The stack has to be up and reachable before persistence means anything, so
	// the acceptance run is what establishes the state this test then restarts.
	if _, err := acceptance.Run(ctx, request.Request); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	persistence := forge.Persistence{Acceptance: acceptance, Lifecycle: application}
	report, err := persistence.Validate(ctx, request)
	for _, volume := range report.Volumes {
		t.Logf(
			"%s (%s): created %s, %d entries",
			volume.Name,
			volume.Description,
			volume.After.CreatedAt,
			len(volume.After.Entries),
		)
		if !volume.Preserved {
			t.Errorf("%s (%s) %s", volume.Name, volume.Description, volume.Difference)
		}
	}
	for _, timing := range report.Timings {
		t.Log(timing)
	}
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	// Ports followed the machine: reachable while no CLI ran, closed once it
	// stopped, and reachable again through the reconciled tunnel.
	if len(report.Ports.BeforeStop) != len(forge.DevEndpoints()) {
		t.Errorf("reachable before stop = %d endpoints, want %d",
			len(report.Ports.BeforeStop), len(forge.DevEndpoints()))
	}
	if len(report.Ports.ClosedAfterStop) != len(forge.DevEndpoints()) {
		t.Errorf("closed after stop = %d endpoints, want %d",
			len(report.Ports.ClosedAfterStop), len(forge.DevEndpoints()))
	}
	for _, endpoint := range report.Ports.AfterRestart {
		t.Logf("%s: %s through %s after the restart", endpoint.Label, endpoint.URL(), endpoint.Forward)
		if endpoint.Body == "" {
			t.Errorf("%s at %s answered with nothing after the restart", endpoint.Label, endpoint.URL())
		}
	}

	// The mounted repository is a working place to develop from both sides.
	if !report.Edit.HostEditRead {
		t.Errorf("Linux did not read the macOS edit at %s", report.Edit.GuestPath)
	}
	if !report.Edit.GuestFileRead {
		t.Errorf("macOS did not read the file Linux created at %s", report.Edit.HostPath)
	}
	if !report.Edit.OwnershipMatched {
		t.Errorf("ownership = guest %d:%d, macOS %d:%d, want the developer's own on both sides",
			report.Edit.GuestUID, report.Edit.GuestGID, report.Edit.HostUID, report.Edit.HostGID)
	}

	// A missed target is a finding about this host rather than a broken
	// environment, but the requirement names both, so both are asserted.
	for _, missed := range report.MissedTargets() {
		t.Errorf("%s", missed)
	}

	// Nothing this test wrote may outlive it, markers included.
	if status := gitStatus(t, resolved.Path); status != "" {
		t.Errorf("the persistence run left changes in the Forge repository:\n%s", status)
	}
}
