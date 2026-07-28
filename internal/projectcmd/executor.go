// Package projectcmd runs the project commands that configuration declares
// explicitly.
//
// isolated-dev never discovers, infers, or executes repository content on its
// own: a Compose file, a task-runner manifest, or a script directory in the
// repository grants nothing. A command exists only because
// `.isolated-dev.toml` names it, and it runs only when that name is invoked.
package projectcmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/fxmartin/isolated-dev/internal/config"
)

var machineNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// guestUserPattern is the Linux login-name shape `guest.NewIdentity` derives.
// The recorded name is re-checked here because it also composes the guest home
// directory this package passes into the machine.
var guestUserPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)

// guestPath is the search path the command runs with. `container machine run`
// does not guarantee a PATH, so it is set explicitly rather than inherited.
const guestPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// Streams carries the invoking terminal's file handles into the guest command,
// so its output and input are the developer's own rather than a captured copy.
type Streams struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Runner executes a host process against the given streams and reports its
// exit status. A non-zero status is the command's own result, not a Runner
// failure: only a process that could not be run at all returns an error.
type Runner interface {
	Run(ctx context.Context, streams Streams, name string, args ...string) (int, error)
}

// DockerWaiter reports when the guest Docker daemon answers `docker info`.
type DockerWaiter interface {
	WaitDocker(ctx context.Context, machineName string) error
}

// Request describes one explicitly invoked project command.
type Request struct {
	MachineName string
	// GuestUser and GuestProjectPath are the provisioned identity and mounted
	// project recorded by `up`.
	GuestUser        string
	GuestProjectPath string
	// Name is the declared command name, used in diagnostics.
	Name    string
	Command config.Command
}

type Executor struct {
	Runner       Runner
	DockerWaiter DockerWaiter
}

// Execute runs the declared command inside the project machine as the non-root
// guest user and returns its exit status. The returned error covers only
// failures to run the command at all; a command that runs and fails reports
// that through its exit status, exactly as it would on the host.
func (executor Executor) Execute(
	ctx context.Context,
	request Request,
	streams Streams,
) (int, error) {
	workdir, err := executor.validate(request)
	if err != nil {
		return 0, err
	}
	if request.Command.Compose {
		if err := executor.DockerWaiter.WaitDocker(ctx, request.MachineName); err != nil {
			return 0, fmt.Errorf(
				"Docker is not ready in machine %q: `docker info` did not succeed, so %q was not run; check the daemon with `ssh %s sudo docker info`: %w",
				request.MachineName,
				request.Name,
				request.MachineName,
				err,
			)
		}
	}

	exitCode, err := executor.Runner.Run(ctx, streams, "container", guestArgs(request, workdir)...)
	if err != nil {
		return 0, fmt.Errorf(
			"run project command %q in machine %q: %w",
			request.Name,
			request.MachineName,
			err,
		)
	}
	return exitCode, nil
}

// guestArgs builds the `container machine run` invocation. The machine command
// enters as root, so `runuser` drops to the provisioned guest account before
// anything from the project runs; `env` then sets the working directory and the
// minimal environment the command needs. Every element is a separate argument:
// no shell interprets the declared arguments at any point.
func guestArgs(request Request, workdir string) []string {
	args := []string{
		"machine", "run",
		"--name", request.MachineName,
		"--root",
		"--",
		"/usr/sbin/runuser", "-u", request.GuestUser, "--",
		"/usr/bin/env", "-C", workdir,
		"PATH=" + guestPath,
		"HOME=" + filepath.Join("/home", request.GuestUser),
	}
	return append(args, request.Command.Args...)
}

// validate confirms the request before anything runs, and returns the absolute
// guest working directory.
func (executor Executor) validate(request Request) (string, error) {
	if executor.Runner == nil {
		return "", errors.New("project command runner is not configured")
	}
	if executor.DockerWaiter == nil && request.Command.Compose {
		return "", errors.New("Docker readiness waiter is not configured")
	}
	if !machineNamePattern.MatchString(request.MachineName) {
		return "", fmt.Errorf("invalid machine name %q", request.MachineName)
	}
	if request.GuestUser == "" || request.GuestProjectPath == "" {
		return "", fmt.Errorf(
			"machine %q has no recorded guest identity or mounted project; run `isolated-dev up` first",
			request.MachineName,
		)
	}
	if request.GuestUser == "root" {
		return "", errors.New(
			"project commands run as the non-root guest user; refusing to run as root",
		)
	}
	if !guestUserPattern.MatchString(request.GuestUser) {
		return "", fmt.Errorf("invalid guest user name %q", request.GuestUser)
	}
	if !filepath.IsAbs(request.GuestProjectPath) {
		return "", fmt.Errorf(
			"guest project path %q must be absolute",
			request.GuestProjectPath,
		)
	}
	if len(request.Command.Args) == 0 {
		return "", fmt.Errorf("commands.%s.args: must not be empty", request.Name)
	}
	return guestWorkdir(request)
}

// guestWorkdir resolves the declared workdir against the mounted project and
// keeps it there, so a declared command cannot step outside the repository it
// belongs to.
func guestWorkdir(request Request) (string, error) {
	workdir := request.Command.Workdir
	if workdir == "" {
		return request.GuestProjectPath, nil
	}
	if filepath.IsAbs(workdir) {
		return "", fmt.Errorf(
			"commands.%s.workdir: must be a project-relative path",
			request.Name,
		)
	}
	resolved := filepath.Join(request.GuestProjectPath, workdir)
	relative, err := filepath.Rel(request.GuestProjectPath, resolved)
	if err != nil ||
		relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf(
			"commands.%s.workdir: must stay inside the project",
			request.Name,
		)
	}
	return resolved, nil
}
