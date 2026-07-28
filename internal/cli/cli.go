package cli

import (
	"fmt"
	"io"
)

type Dependencies struct {
	Stdout  io.Writer
	Stderr  io.Writer
	Version string
	Status  func(string) error
	Up      func(string) error
	Stop    func(string) error
	Destroy func(string) error
	// Open reconciles the project machine and opens it in Zed.
	Open func(string) error
	// Upgrade previews the base-image recreation, and performs it only when
	// the confirmation is passed through.
	Upgrade func(string, bool) error
	// Run executes one explicitly declared project command and reports the exit
	// status that command produced.
	Run      func(string, string) (int, error)
	OnMutate func()
}

func Run(args []string, deps Dependencies) int {
	if len(args) == 1 && (args[0] == "--version" || args[0] == "version") {
		fmt.Fprintf(deps.Stdout, "isolated-dev %s\n", deps.Version)
		return 0
	}
	if len(args) == 2 && args[0] == "status" {
		return runCommand("status", args[1], deps.Status, deps, false)
	}
	if len(args) == 2 && args[0] == "up" {
		return runCommand("up", args[1], deps.Up, deps, true)
	}
	if len(args) == 2 && args[0] == "open" {
		return runCommand("open", args[1], deps.Open, deps, true)
	}
	if len(args) == 2 && args[0] == "stop" {
		return runCommand("stop", args[1], deps.Stop, deps, true)
	}
	// `upgrade --yes` alone is a forgotten project path, not a preview of a
	// project named "--yes": naming the real mistake beats a resolve failure.
	if len(args) == 2 && args[0] == "upgrade" && args[1] == "--yes" {
		fmt.Fprintln(deps.Stderr, "upgrade: pass the project path, as in `isolated-dev upgrade --yes PROJECT`")
		return 2
	}
	// A bare `upgrade` is the preview: it reports what a recreation would
	// discard and changes nothing.
	if len(args) == 2 && args[0] == "upgrade" {
		return runUpgrade(args[1], false, deps)
	}
	if len(args) == 3 && args[0] == "upgrade" {
		projectPath, confirmed := confirmedYes(args[1:])
		if !confirmed {
			fmt.Fprintln(deps.Stderr, "upgrade: pass --yes to confirm recreating the project machine")
			return 2
		}
		return runUpgrade(projectPath, true, deps)
	}
	if len(args) == 3 && args[0] == "destroy" {
		projectPath, confirmed := confirmedYes(args[1:])
		if !confirmed {
			fmt.Fprintln(deps.Stderr, "destroy: pass --yes to confirm deletion of the project machine and persistent data")
			return 2
		}
		return runCommand("destroy", projectPath, deps.Destroy, deps, true)
	}
	if len(args) == 2 && args[0] == "destroy" {
		fmt.Fprintln(deps.Stderr, "destroy: pass --yes to confirm deletion of the project machine and persistent data")
		return 2
	}
	// Only a command the project declares by name can be run, so the name is
	// required rather than inferred from the repository.
	if len(args) == 3 && args[0] == "run" {
		return runProjectCommand(args[1], args[2], deps)
	}
	if len(args) == 2 && args[0] == "run" {
		fmt.Fprintln(deps.Stderr, "run: pass the declared command name, as in `isolated-dev run PROJECT COMMAND`")
		return 2
	}

	fmt.Fprintln(
		deps.Stderr,
		"usage: isolated-dev <up PROJECT|open PROJECT|run PROJECT COMMAND|status PROJECT|stop PROJECT|upgrade [--yes] PROJECT|destroy --yes PROJECT|--version>",
	)
	return 2
}

// runProjectCommand reports the guest command's own exit status. A command that
// runs and fails is not a CLI error, so only a rejected or unrunnable command
// writes to stderr.
func runProjectCommand(projectPath string, commandName string, deps Dependencies) int {
	if deps.Run == nil {
		fmt.Fprintln(deps.Stderr, "run: command is unavailable")
		return 1
	}
	if deps.OnMutate != nil {
		deps.OnMutate()
	}
	exitCode, err := deps.Run(projectPath, commandName)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "run: %v\n", err)
		return 1
	}
	return exitCode
}

func runUpgrade(projectPath string, confirmed bool, deps Dependencies) int {
	if deps.Upgrade == nil {
		fmt.Fprintln(deps.Stderr, "upgrade: command is unavailable")
		return 1
	}
	command := func(path string) error { return deps.Upgrade(path, confirmed) }
	return runCommand("upgrade", projectPath, command, deps, confirmed)
}

func runCommand(
	name string,
	projectPath string,
	command func(string) error,
	deps Dependencies,
	mutating bool,
) int {
	if command == nil {
		fmt.Fprintf(deps.Stderr, "%s: command is unavailable\n", name)
		return 1
	}
	if mutating && deps.OnMutate != nil {
		deps.OnMutate()
	}
	if err := command(projectPath); err != nil {
		fmt.Fprintf(deps.Stderr, "%s: %v\n", name, err)
		return 1
	}
	return 0
}

func confirmedYes(args []string) (string, bool) {
	if args[0] == "--yes" {
		return args[1], true
	}
	if args[1] == "--yes" {
		return args[0], true
	}
	return "", false
}
