package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fxmartin/isolated-dev/internal/config"
	"github.com/fxmartin/isolated-dev/internal/project"
	"github.com/fxmartin/isolated-dev/internal/projectcmd"
	"github.com/fxmartin/isolated-dev/internal/state"
)

// ProjectCommandRunner executes one explicitly declared project command inside
// the project machine.
type ProjectCommandRunner interface {
	Execute(context.Context, projectcmd.Request, projectcmd.Streams) (int, error)
}

// Run executes the named project command and returns its exit status.
//
// Only a command that `.isolated-dev.toml` declares can be invoked, and the
// declaration is looked up before anything else happens: a name the project
// does not declare is rejected without reading, resolving, or executing any
// repository content. Nothing about the repository — a Compose file, a task
// runner, a script — is ever consulted to decide what a name means.
func (app App) Run(
	ctx context.Context,
	projectPath string,
	commandName string,
	streams projectcmd.Streams,
) (int, error) {
	resolved, err := project.Resolve(projectPath)
	if err != nil {
		return 0, err
	}
	effectiveConfig, err := config.Load(resolved.Path)
	if err != nil {
		return 0, err
	}
	declared, err := declaredCommand(effectiveConfig, commandName)
	if err != nil {
		return 0, err
	}
	if app.ProjectCommands == nil {
		return 0, errors.New("project command execution is not configured")
	}
	if _, err := app.HostChecker.Check(ctx); err != nil {
		return 0, err
	}

	stored, err := app.StateStore.Load(resolved.MachineName)
	if errors.Is(err, state.ErrNotFound) {
		return 0, fmt.Errorf(
			"no project machine exists for %s; run `isolated-dev up %s` first",
			resolved.Path,
			resolved.Path,
		)
	}
	if err != nil {
		return 0, fmt.Errorf("load project state: %w", err)
	}

	return app.ProjectCommands.Execute(ctx, projectcmd.Request{
		MachineName:      resolved.MachineName,
		GuestUser:        stored.GuestUser,
		GuestProjectPath: stored.GuestProjectPath,
		Name:             commandName,
		Command:          declared,
	}, streams)
}

// declaredCommand resolves a command name against the project's declarations
// and explains what the project does offer when it does not match.
func declaredCommand(
	effectiveConfig config.Config,
	commandName string,
) (config.Command, error) {
	if strings.TrimSpace(commandName) == "" {
		return config.Command{}, errors.New("a command name is required")
	}
	declared, ok := effectiveConfig.Commands[commandName]
	if ok {
		return declared, nil
	}
	names := effectiveConfig.CommandNames()
	if len(names) == 0 {
		return config.Command{}, fmt.Errorf(
			"command %q is not declared: this project declares no commands; add a [commands.%s] section to %s to run it",
			commandName,
			commandName,
			config.SharedFileName,
		)
	}
	return config.Command{}, fmt.Errorf(
		"command %q is not declared by this project; declared commands: %s",
		commandName,
		strings.Join(names, ", "),
	)
}
