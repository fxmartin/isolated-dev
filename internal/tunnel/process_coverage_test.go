package tunnel

import (
	"testing"
	"time"
)

// startStubbornTestProcess launches a process that ignores termination, the way
// a wedged `ssh -N` can, so the controller has to escalate to reclaim the macOS
// ports it is holding. The marker sits on its command line exactly as the
// machine name sits on the tunnel's.
func startStubbornTestProcess(t *testing.T, marker string) (ExecController, int) {
	t.Helper()

	controller := ExecController{}
	pid, err := controller.Start("/bin/sh", []string{
		"-c", "trap '' TERM; while :; do sleep 1; done", marker,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		if err := controller.Stop(pid, marker); err != nil {
			t.Errorf("Stop() error = %v", err)
		}
	})
	// The trap is installed by the shell a moment after it is started, so the
	// process is only stubborn once it is running.
	for attempt := 0; attempt < 50; attempt++ {
		if controller.Running(pid, marker) {
			return controller, pid
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("Running(%d) = false, want the stubborn process started", pid)
	return controller, pid
}

// A tunnel that refuses to terminate still has to release its macOS ports,
// because the next reconciliation rebinds them. Waiting is what makes the
// escalation safe: the process is only killed after it was given the chance to
// exit on its own.
func TestExecControllerKillsATunnelThatIgnoresTermination(t *testing.T) {
	t.Parallel()

	const marker = "isolated-dev-stubborn-0000beef"
	controller, pid := startStubbornTestProcess(t, marker)

	if err := controller.Stop(pid, marker); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if controller.Running(pid, marker) {
		t.Errorf("Running(%d) = true, want the stubborn process killed", pid)
	}
	// Both `stop` and `destroy` run cleanup, so repeating it after an escalation
	// has to stay a no-op.
	if err := controller.Stop(pid, marker); err != nil {
		t.Errorf("Stop() error = %v, want repeated cleanup to succeed", err)
	}
}

// The wait is bounded rather than open-ended, so a process that never goes away
// cannot hang `stop` forever.
func TestWaitForExitGivesUpOnAProcessThatStays(t *testing.T) {
	t.Parallel()

	const marker = "isolated-dev-stubborn-0000cafe"
	_, pid := startStubbornTestProcess(t, marker)

	start := time.Now()
	if waitForExit(pid, marker) {
		t.Fatalf("waitForExit(%d) = true, want the live process reported as staying", pid)
	}
	if waited := time.Since(start); waited < exitPollInterval {
		t.Errorf("waited = %v, want the process given time to exit", waited)
	}
}

// A process ID that never named a process of ours is not something to wait for
// at all, which is what keeps repeated cleanup instant.
func TestWaitForExitAcceptsAProcessThatIsAlreadyGone(t *testing.T) {
	t.Parallel()

	if !waitForExit(4194303, "isolated-dev-app-abcd1234") {
		t.Error("waitForExit() = false, want an absent process reported as exited")
	}
}
