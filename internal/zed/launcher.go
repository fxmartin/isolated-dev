// Package zed opens a project machine in Zed's SSH remote-development mode.
package zed

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
)

// aliasPattern matches the managed SSH host aliases, which are the derived
// project-machine names.
var aliasPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type Launcher struct {
	Runner Runner
	// LookPath defaults to resolving `zed` through the host PATH.
	LookPath func(string) (string, error)
}

// Open opens the guest project over the managed SSH host. Zed resolves the
// alias through the managed configuration, so no connection has to be set up by
// hand.
func (launcher Launcher) Open(ctx context.Context, alias string, guestPath string) error {
	if launcher.Runner == nil {
		return errors.New("Zed runner is not configured")
	}
	if !aliasPattern.MatchString(alias) {
		return fmt.Errorf("invalid SSH host alias %q", alias)
	}
	if !filepath.IsAbs(guestPath) {
		return fmt.Errorf("guest project path %q must be absolute", guestPath)
	}

	lookPath := launcher.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	command, err := lookPath("zed")
	if err != nil {
		return fmt.Errorf(
			"the zed command is not on PATH; install it from Zed's command palette with `cli: install cli` and retry: %w",
			err,
		)
	}

	target := url.URL{Scheme: "ssh", Host: alias, Path: guestPath}
	output, err := launcher.Runner.Run(ctx, command, target.String())
	if err != nil {
		return fmt.Errorf("open %s in Zed: %w\n%s", target.String(), err, output)
	}
	return nil
}
