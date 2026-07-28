package zed

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestHostZedCLIOpensTheManagedTarget is the host-side check for the Zed leg of
// the integration. The stubbed tests prove which argument the launcher builds;
// only a real host can prove that the `zed` CLI the launcher resolves is
// actually installed and invocable, and that the target handed to it is a URL a
// remote-development client takes apart into exactly the alias and path that
// went in — including a project path that has to survive percent-encoding.
//
// It deliberately stops before launching a window. A real `ssh://` target makes
// Zed open a remote workspace and dial the machine, which leaves a GUI window
// behind and cannot be asserted on unattended, so the launch itself stays
// stubbed while everything the host can decide is checked for real.
func TestHostZedCLIOpensTheManagedTarget(t *testing.T) {
	if os.Getenv("ISOLATED_DEV_RUN_HOST_TESTS") != "1" {
		t.Skip("set ISOLATED_DEV_RUN_HOST_TESTS=1 to run the Zed CLI test")
	}
	command, err := exec.LookPath("zed")
	if err != nil {
		t.Fatalf("find zed CLI: %v (install it from Zed's command palette with `cli: install cli`)", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// An invocable CLI is the precondition the launcher cannot check for itself:
	// LookPath only proves a file with that name is on PATH.
	version, err := exec.CommandContext(ctx, command, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("zed --version error = %v\n%s", err, version)
	}
	if len(version) == 0 {
		t.Error("zed --version printed nothing, want the installed Zed build")
	}

	const alias = "isolated-dev-my-app-abcd1234"
	const guestPath = "/home/fx/my projects/app"

	// Resolution goes through the real PATH here, the way `isolated-dev open`
	// does; only the launch is stubbed.
	runner := &runnerStub{}
	launcher := Launcher{Runner: runner}
	if err := launcher.Open(ctx, alias, guestPath); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if len(runner.names) != 1 || runner.names[0] != command {
		t.Fatalf("commands = %v, want the zed CLI resolved on PATH (%q)", runner.names, command)
	}
	if len(runner.args[0]) != 1 {
		t.Fatalf("arguments = %v, want exactly the target URL", runner.args[0])
	}

	target, err := url.Parse(runner.args[0][0])
	if err != nil {
		t.Fatalf("Zed target %q is not a URL: %v", runner.args[0][0], err)
	}
	if target.Scheme != "ssh" {
		t.Errorf("scheme = %q, want Zed's SSH remote-development scheme", target.Scheme)
	}
	// The alias has to arrive unescaped, or Zed asks SSH for a host the managed
	// configuration has never heard of.
	if target.Host != alias {
		t.Errorf("host = %q, want the managed SSH alias %q", target.Host, alias)
	}
	if target.Path != guestPath {
		t.Errorf("path = %q, want the guest project path %q", target.Path, guestPath)
	}
}
