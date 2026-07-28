package projectcmd

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
)

// ExecRunner runs the host process with the developer's own streams attached,
// so a project command's output appears as it is produced rather than after it
// finishes, and its exit status survives unchanged.
type ExecRunner struct{}

func (ExecRunner) Run(
	ctx context.Context,
	streams Streams,
	name string,
	args ...string,
) (int, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdin = streams.Stdin
	command.Stdout = streams.Stdout
	command.Stderr = streams.Stderr

	err := command.Run()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// A command killed by a signal has no exit status of its own. Reporting
		// the shell convention keeps the failure visible instead of collapsing
		// it to a success.
		if code := exitErr.ExitCode(); code >= 0 {
			return code, nil
		}
		return 0, fmt.Errorf("%s was terminated: %w", name, err)
	}
	return 0, fmt.Errorf("start %s: %w", name, err)
}
