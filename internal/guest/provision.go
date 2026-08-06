package guest

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

//go:embed provision.sh
var provisionScript string

//go:embed packages.sh
var packagesScript string

var machineNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// packageNamePattern mirrors the configuration-level check: the names become
// arguments to a root shell inside the guest, so this boundary validates them
// again rather than trusting its caller.
var packageNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+$`)

// ownershipPattern matches the `stat -c %u:%g` output used to confirm that the
// mounted project maps to the guest user rather than to root.
var ownershipPattern = regexp.MustCompile(`^(\d+):(\d+)$`)

type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

// Request describes one idempotent guest provisioning pass.
type Request struct {
	MachineName string
	ProjectPath string
	HomeDir     string
	Identity    Identity
	PublicKeys  []string
	// Packages are the Ubuntu packages the project declares; missing ones are
	// installed after the guest identity is configured.
	Packages []string
}

// Result reports the converged guest state.
type Result struct {
	Identity Identity
	// GuestProjectPath is the stable Linux path at which the macOS repository
	// is readable and writable.
	GuestProjectPath string
	// OwnershipMatched reports whether the mounted project already carries the
	// guest user's numeric UID and GID.
	OwnershipMatched bool
}

type Provisioner struct {
	Runner Runner
}

// Provision converges the guest identity, credentials, and mounted project.
// Rerunning it is safe: every step is idempotent.
func (provisioner Provisioner) Provision(
	ctx context.Context,
	request Request,
) (Result, error) {
	relativeProject, err := provisioner.validate(request)
	if err != nil {
		return Result{}, err
	}

	// The project is located before the guest is configured. Provisioning owns
	// /home/<user> and rewrites it, so a mount that lands there has to be
	// detected before any of those writes happen rather than after.
	guestPath, matched, err := provisioner.locateProject(ctx, request, relativeProject)
	if err != nil {
		return Result{}, err
	}
	if err := refuseGuestHomeMount(request.Identity.Username, guestPath); err != nil {
		return Result{}, err
	}

	args := []string{
		"machine", "run",
		"--name", request.MachineName,
		"--root",
		"--",
		"/usr/bin/bash", "-c", provisionScript, "isolated-dev-provision",
		request.Identity.Username,
		strconv.Itoa(request.Identity.UID),
		strconv.Itoa(request.Identity.GID),
	}
	args = append(args, request.PublicKeys...)
	output, err := provisioner.Runner.Run(ctx, "container", args...)
	if err != nil {
		return Result{}, fmt.Errorf(
			"configure guest identity on machine %q: %w\n%s",
			request.MachineName,
			err,
			output,
		)
	}
	if err := provisioner.installPackages(ctx, request); err != nil {
		return Result{}, err
	}

	return Result{
		Identity:         request.Identity,
		GuestProjectPath: guestPath,
		OwnershipMatched: matched,
	}, nil
}

// installPackages converges the declared Ubuntu packages. The script installs
// only what is missing, so a machine that already carries them is untouched
// and no network is needed after the first install.
func (provisioner Provisioner) installPackages(ctx context.Context, request Request) error {
	if len(request.Packages) == 0 {
		return nil
	}
	args := []string{
		"machine", "run",
		"--name", request.MachineName,
		"--root",
		"--",
		"/usr/bin/bash", "-c", packagesScript, "isolated-dev-packages",
	}
	args = append(args, request.Packages...)
	output, err := provisioner.Runner.Run(ctx, "container", args...)
	if err != nil {
		return fmt.Errorf(
			"install declared packages on machine %q: %w\n%s",
			request.MachineName,
			err,
			output,
		)
	}
	return nil
}

// refuseGuestHomeMount stops provisioning when the macOS home is exposed at the
// guest home path. provision.sh owns /home/<user>: it relaxes the directory to
// 0755 and replaces .ssh/authorized_keys with the collected host keys. If that
// directory is the mounted macOS home, both writes land on real host data and
// destroy the developer's own authorized_keys, so `up` stops instead.
func refuseGuestHomeMount(username string, guestProjectPath string) error {
	guestHome := filepath.Join("/home", username)
	if _, within := withinDir(guestHome, guestProjectPath); !within {
		return nil
	}
	return fmt.Errorf(
		"the project is exposed at %s, inside the guest home %s that provisioning rewrites; refusing to configure the guest because that would overwrite the macOS ~/.ssh/authorized_keys",
		guestProjectPath,
		guestHome,
	)
}

// withinDir reports whether path is dir or one of its descendants, and returns
// path relative to dir.
func withinDir(dir string, path string) (string, bool) {
	relative, err := filepath.Rel(dir, path)
	if err != nil {
		return "", false
	}
	if relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(relative) {
		return "", false
	}
	return relative, true
}

// locateProject resolves the Linux path of the mounted repository. Apple
// Container Machine 1.1.0 exposes the macOS home directory without documenting
// the guest path, so both the host path and the guest home are probed.
func (provisioner Provisioner) locateProject(
	ctx context.Context,
	request Request,
	relativeProject string,
) (string, bool, error) {
	candidates := []string{
		request.ProjectPath,
		filepath.Join("/home", request.Identity.Username, relativeProject),
	}
	for _, candidate := range candidates {
		output, err := provisioner.Runner.Run(
			ctx,
			"container",
			"machine", "run",
			"--name", request.MachineName,
			"--root",
			"--",
			"/usr/bin/stat", "-c", "%u:%g", filepath.Join(candidate, ".git"),
		)
		if err != nil {
			continue
		}
		match := ownershipPattern.FindStringSubmatch(strings.TrimSpace(string(output)))
		if match == nil {
			return "", false, fmt.Errorf(
				"could not read the ownership of %s inside machine %q",
				candidate,
				request.MachineName,
			)
		}
		uid, _ := strconv.Atoi(match[1])
		gid, _ := strconv.Atoi(match[2])
		return candidate, uid == request.Identity.UID && gid == request.Identity.GID, nil
	}
	return "", false, fmt.Errorf(
		"could not find the project inside machine %q at %s; the macOS home mount is missing or incomplete",
		request.MachineName,
		strings.Join(candidates, " or "),
	)
}

func (provisioner Provisioner) validate(request Request) (string, error) {
	if provisioner.Runner == nil {
		return "", errors.New("guest runner is not configured")
	}
	if !machineNamePattern.MatchString(request.MachineName) {
		return "", fmt.Errorf("invalid machine name %q", request.MachineName)
	}
	if !filepath.IsAbs(request.ProjectPath) {
		return "", errors.New("project path must be absolute")
	}
	if !filepath.IsAbs(request.HomeDir) {
		return "", errors.New("home directory must be absolute")
	}
	if _, err := NewIdentity(
		request.Identity.Username,
		request.Identity.UID,
		request.Identity.GID,
	); err != nil {
		return "", err
	}
	if len(request.PublicKeys) == 0 {
		return "", errors.New("at least one authorized public key is required for guest login")
	}
	for _, key := range request.PublicKeys {
		if err := validatePublicKey(key); err != nil {
			return "", fmt.Errorf("authorized public key: %w", err)
		}
	}
	for _, name := range request.Packages {
		if !packageNamePattern.MatchString(name) {
			return "", fmt.Errorf("invalid guest package name %q", name)
		}
	}

	relative, within := withinDir(request.HomeDir, request.ProjectPath)
	if !within {
		return "", fmt.Errorf(
			"project %q is outside the mounted home directory %q",
			request.ProjectPath,
			request.HomeDir,
		)
	}
	return relative, nil
}
