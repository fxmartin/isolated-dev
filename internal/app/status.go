package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/fxmartin/isolated-dev/internal/baseimage"
	"github.com/fxmartin/isolated-dev/internal/config"
	"github.com/fxmartin/isolated-dev/internal/guest"
	"github.com/fxmartin/isolated-dev/internal/host"
	"github.com/fxmartin/isolated-dev/internal/machine"
	"github.com/fxmartin/isolated-dev/internal/project"
	"github.com/fxmartin/isolated-dev/internal/sshconfig"
	"github.com/fxmartin/isolated-dev/internal/state"
	statusview "github.com/fxmartin/isolated-dev/internal/status"
)

type MachineManager interface {
	Up(context.Context, machine.Request) (machine.UpResult, error)
	Stop(context.Context, machine.Target) error
	Destroy(context.Context, machine.Target) error
}

type GuestProvisioner interface {
	Provision(context.Context, guest.Request) (guest.Result, error)
}

// AddressResolver reports the address at which macOS reaches a project machine.
// A machine can move between restarts, so it is resolved on every `up`.
type AddressResolver interface {
	Address(context.Context, machine.Target) (string, error)
}

// SSHConfigurator maintains the managed SSH host that Zed and ordinary SSH
// sessions connect through.
type SSHConfigurator interface {
	Apply(sshconfig.Entry) error
	Remove(alias string) error
	ForgetHostKey(alias string) error
}

// ZedLauncher opens a guest path over the managed SSH host.
type ZedLauncher interface {
	Open(ctx context.Context, alias string, guestPath string) error
}

type App struct {
	Version          string
	HostChecker      host.Checker
	StateStore       state.Store
	MachineManager   MachineManager
	GuestProvisioner GuestProvisioner
	// ImageEnsurer builds the target base image before `upgrade` destroys the
	// machine it is replacing.
	ImageEnsurer    ImageEnsurer
	AddressResolver AddressResolver
	SSHConfig       SSHConfigurator
	// Tunnels keeps the configured guest ports reachable on macOS loopback
	// after the CLI and Zed have exited.
	Tunnels TunnelManager
	Zed     ZedLauncher
	// ProjectCommands executes explicitly declared project commands. It is used
	// only by `run`: no lifecycle command ever reaches for it.
	ProjectCommands ProjectCommandRunner
	HomeDir         string
	WarningOutput   io.Writer
	// ResolveIdentity defaults to the invoking macOS user.
	ResolveIdentity func() (guest.Identity, error)
}

func (app App) Status(ctx context.Context, projectPath string, output io.Writer) error {
	resolved, err := project.Resolve(projectPath)
	if err != nil {
		return err
	}
	effectiveConfig, err := config.Load(resolved.Path)
	if err != nil {
		return err
	}
	prerequisites, err := app.HostChecker.Check(ctx)
	if err != nil {
		return err
	}

	snapshot := statusview.Snapshot{
		CLIVersion:       app.Version,
		ContainerVersion: prerequisites.ContainerVersion,
		ProjectPath:      resolved.Path,
		MachineName:      resolved.MachineName,
		MachineStatus:    "not-created",
		BaseImage:        effectiveConfig.BaseImage,
		MountScope:       "not-created",
		TunnelStatus:     "stopped",
		Config:           effectiveConfig,
	}
	stored, err := app.StateStore.Load(resolved.MachineName)
	if err == nil {
		snapshot.MachineStatus = "unknown"
		snapshot.BaseImage = stored.BaseImage
		snapshot.AvailableBaseImage = effectiveConfig.BaseImage
		snapshot.MountScope = stored.MountScope
		snapshot.TunnelStatus = "unknown"
		snapshot.GuestUser = stored.GuestUser
		snapshot.GuestUID = stored.GuestUID
		snapshot.GuestGID = stored.GuestGID
		snapshot.GuestProjectPath = stored.GuestProjectPath
		snapshot.SSHAddress = stored.SSHAddress
		snapshot.Config.Resources.CPUs = stored.CPUs
		snapshot.Config.Resources.MemoryGB = stored.MemoryGB
	} else if !errors.Is(err, state.ErrNotFound) {
		return fmt.Errorf("load project state: %w", err)
	}

	tunnelStatus, err := app.tunnelStatus(resolved.MachineName, snapshot.TunnelStatus)
	if err != nil {
		return err
	}
	snapshot.TunnelStatus = tunnelStatus

	return statusview.Write(output, snapshot)
}

// upPreparation holds everything `up` establishes before it mutates any
// machine. `upgrade` runs the same preparation so a rejected repository,
// missing key, or unmanaged image fails while the existing machine — and the
// guest data only it holds — is still intact.
type upPreparation struct {
	project       project.Project
	config        config.Config
	canonicalHome string
	identity      guest.Identity
	publicKeys    []string
}

func (app App) prepareUp(ctx context.Context, projectPath string) (upPreparation, error) {
	resolved, err := project.Resolve(projectPath)
	if err != nil {
		return upPreparation{}, err
	}
	effectiveConfig, err := config.Load(resolved.Path)
	if err != nil {
		return upPreparation{}, err
	}
	if _, err := app.HostChecker.Check(ctx); err != nil {
		return upPreparation{}, err
	}
	if app.MachineManager == nil {
		return upPreparation{}, errors.New("machine lifecycle is not configured")
	}
	if app.GuestProvisioner == nil {
		return upPreparation{}, errors.New("guest provisioning is not configured")
	}
	if app.SSHConfig == nil || app.AddressResolver == nil {
		return upPreparation{}, errors.New("SSH access is not configured")
	}
	if app.Tunnels == nil {
		return upPreparation{}, errors.New("port forwarding is not configured")
	}
	if !baseimage.IsManagedReference(effectiveConfig.BaseImage) {
		return upPreparation{}, fmt.Errorf(
			"base image %q is not a managed isolated-dev image; refusing to grant it read-write access to the full home directory",
			effectiveConfig.BaseImage,
		)
	}
	canonicalHome, err := app.canonicalHome()
	if err != nil {
		return upPreparation{}, err
	}
	if err := validateHomeMountedProject(canonicalHome, resolved.Path); err != nil {
		return upPreparation{}, err
	}
	// The guest identity and its authorized public keys are resolved before any
	// machine mutation so a missing key fails without leaving a half-configured
	// machine behind.
	identity, err := app.guestIdentity()
	if err != nil {
		return upPreparation{}, err
	}
	publicKeys, err := guest.PublicKeys(filepath.Join(canonicalHome, ".ssh"))
	if err != nil {
		return upPreparation{}, err
	}
	return upPreparation{
		project:       resolved,
		config:        effectiveConfig,
		canonicalHome: canonicalHome,
		identity:      identity,
		publicKeys:    publicKeys,
	}, nil
}

// upOutcome describes the reconciled machine, which `open` uses to reach the
// project without resolving it a second time.
type upOutcome struct {
	project          project.Project
	guestProjectPath string
}

func (app App) Up(ctx context.Context, projectPath string, output io.Writer) error {
	_, err := app.up(ctx, projectPath, output)
	return err
}

func (app App) up(
	ctx context.Context,
	projectPath string,
	output io.Writer,
) (upOutcome, error) {
	preparation, err := app.prepareUp(ctx, projectPath)
	if err != nil {
		return upOutcome{}, err
	}
	resolved := preparation.project
	effectiveConfig := preparation.config
	canonicalHome := preparation.canonicalHome
	if err := app.warn(
		"warning: this machine receives read-write access to your full home directory",
	); err != nil {
		return upOutcome{}, fmt.Errorf("write full-home mount warning: %w", err)
	}
	if err := app.warnMissingSecretFiles(resolved.Path, effectiveConfig.Secrets); err != nil {
		return upOutcome{}, err
	}
	result, err := app.MachineManager.Up(ctx, machine.Request{
		ProjectPath:      resolved.Path,
		MachineName:      resolved.MachineName,
		BaseImage:        effectiveConfig.BaseImage,
		BaseImageVersion: imageVersion(effectiveConfig.BaseImage),
		CPUs:             effectiveConfig.Resources.CPUs,
		MemoryGB:         effectiveConfig.Resources.MemoryGB,
		MountScope:       "home",
	})
	if err != nil {
		return upOutcome{}, err
	}
	provisioned, err := app.GuestProvisioner.Provision(ctx, guest.Request{
		MachineName: resolved.MachineName,
		ProjectPath: resolved.Path,
		HomeDir:     canonicalHome,
		Identity:    preparation.identity,
		PublicKeys:  preparation.publicKeys,
		Packages:    effectiveConfig.Packages,
	})
	if err != nil {
		return upOutcome{}, err
	}
	address, err := app.reconcileSSH(ctx, resolved, provisioned, result.Created)
	if err != nil {
		return upOutcome{}, err
	}
	if err := app.recordConnection(resolved.MachineName, provisioned, address); err != nil {
		return upOutcome{}, err
	}
	if !provisioned.OwnershipMatched {
		if err := app.warn(
			"warning: the mounted project at %s is not owned by %s (%d:%d); files created in Linux may not match your macOS ownership",
			provisioned.GuestProjectPath,
			provisioned.Identity.Username,
			provisioned.Identity.UID,
			provisioned.Identity.GID,
		); err != nil {
			return upOutcome{}, fmt.Errorf("write mount ownership warning: %w", err)
		}
	}

	outcome := "ready"
	if result.Created {
		outcome = "created"
	}
	if _, err := fmt.Fprintf(output, "%s %s\n", outcome, resolved.Path); err != nil {
		return upOutcome{}, fmt.Errorf("write up summary: %w", err)
	}
	if _, err := fmt.Fprintf(
		output,
		"guest %s (%d:%d) at %s\n",
		provisioned.Identity.Username,
		provisioned.Identity.UID,
		provisioned.Identity.GID,
		provisioned.GuestProjectPath,
	); err != nil {
		return upOutcome{}, fmt.Errorf("write guest summary: %w", err)
	}
	if len(effectiveConfig.Packages) > 0 {
		if _, err := fmt.Fprintf(
			output,
			"packages %s\n",
			strings.Join(effectiveConfig.Packages, " "),
		); err != nil {
			return upOutcome{}, fmt.Errorf("write package summary: %w", err)
		}
	}
	// The alias is a working `ssh` argument, and the address is what changed if
	// a connection ever misbehaves.
	if _, err := fmt.Fprintf(
		output,
		"ssh %s (%s@%s)\n",
		resolved.MachineName,
		provisioned.Identity.Username,
		address,
	); err != nil {
		return upOutcome{}, fmt.Errorf("write SSH summary: %w", err)
	}
	if err := app.reconcileTunnel(
		resolved,
		effectiveConfig,
		address,
		provisioned.Identity.Username,
		result.Created,
		output,
	); err != nil {
		return upOutcome{}, err
	}
	return upOutcome{
		project:          resolved,
		guestProjectPath: provisioned.GuestProjectPath,
	}, nil
}

// reconcileSSH points the managed SSH host at the machine's current address.
// The address is resolved after provisioning, which is what starts sshd, and a
// freshly created machine first loses the host keys recorded for the machine it
// replaced: it answers under the same alias with a new key.
func (app App) reconcileSSH(
	ctx context.Context,
	resolved project.Project,
	provisioned guest.Result,
	created bool,
) (string, error) {
	address, err := app.AddressResolver.Address(ctx, machine.Target{
		ProjectPath: resolved.Path,
		MachineName: resolved.MachineName,
	})
	if err != nil {
		return "", err
	}
	if created {
		if err := app.SSHConfig.ForgetHostKey(resolved.MachineName); err != nil {
			return "", fmt.Errorf("forget the host keys of the replaced machine: %w", err)
		}
	}
	if err := app.SSHConfig.Apply(sshconfig.Entry{
		Alias:    resolved.MachineName,
		HostName: address,
		User:     provisioned.Identity.Username,
	}); err != nil {
		return "", fmt.Errorf("configure SSH access to %q: %w", resolved.MachineName, err)
	}
	return address, nil
}

func (app App) guestIdentity() (guest.Identity, error) {
	if app.ResolveIdentity != nil {
		return app.ResolveIdentity()
	}
	return guest.ResolveIdentity()
}

// recordConnection persists the provisioned identity, the mounted-project path,
// and the address behind the managed SSH host so `status` can report them
// without touching the machine.
func (app App) recordConnection(
	machineName string,
	provisioned guest.Result,
	address string,
) error {
	stored, err := app.StateStore.Load(machineName)
	if err != nil {
		return fmt.Errorf("load project state: %w", err)
	}
	stored.GuestUser = provisioned.Identity.Username
	stored.GuestUID = provisioned.Identity.UID
	stored.GuestGID = provisioned.Identity.GID
	stored.GuestProjectPath = provisioned.GuestProjectPath
	stored.SSHAddress = address
	if err := app.StateStore.Save(stored); err != nil {
		return fmt.Errorf("record guest identity: %w", err)
	}
	return nil
}

// warnMissingSecretFiles reports referenced secret files that the project does
// not contain. Only their existence is checked: isolated-dev never opens,
// copies, or prints them.
func (app App) warnMissingSecretFiles(
	projectPath string,
	secrets config.SecretReferences,
) error {
	for _, reference := range secrets.Files {
		_, err := os.Stat(filepath.Join(projectPath, reference))
		if err == nil {
			continue
		}
		message := "warning: referenced secret file %s is not present in the project; its contents are never read"
		if !errors.Is(err, os.ErrNotExist) {
			// A reference is advisory metadata that isolated-dev never opens, so
			// one it cannot even stat — a path under a regular file, or a
			// directory it may not traverse — warns rather than blocking the
			// machine from being created at all.
			message = "warning: referenced secret file %s cannot be checked; its contents are never read"
		}
		if err := app.warn(message, reference); err != nil {
			return fmt.Errorf("write secret reference warning: %w", err)
		}
	}
	return nil
}

func (app App) warn(format string, args ...any) error {
	if app.WarningOutput == nil {
		return nil
	}
	_, err := fmt.Fprintf(app.WarningOutput, format+"\n", args...)
	return err
}

func (app App) canonicalHome() (string, error) {
	homeDir := app.HomeDir
	if homeDir == "" {
		var err error
		homeDir, err = os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
	}
	canonicalHome, err := filepath.EvalSymlinks(homeDir)
	if err != nil {
		return "", fmt.Errorf("resolve home directory %q: %w", homeDir, err)
	}
	return canonicalHome, nil
}

func validateHomeMountedProject(canonicalHome string, projectPath string) error {
	relative, err := filepath.Rel(canonicalHome, projectPath)
	if err != nil {
		return fmt.Errorf("compare project and home directories: %w", err)
	}
	if relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(relative) {
		return fmt.Errorf(
			"project %q is outside the mounted home directory %q; Apple Container Machine 1.1.0 cannot expose it, so move the repository under your home directory before running up",
			projectPath,
			canonicalHome,
		)
	}
	return nil
}

func (app App) Stop(ctx context.Context, projectPath string) error {
	resolved, err := app.resolveForMutation(ctx, projectPath)
	if err != nil {
		return err
	}
	if app.Tunnels == nil {
		return errors.New("port forwarding is not configured")
	}
	if err := app.MachineManager.Stop(ctx, machine.Target{
		ProjectPath: resolved.Path,
		MachineName: resolved.MachineName,
	}); err != nil {
		return err
	}
	// A stopped machine forwards nothing, so its tunnel goes with it. Removal
	// is idempotent, which keeps a repeated stop safe.
	return app.Tunnels.Remove(resolved.MachineName)
}

func (app App) Destroy(ctx context.Context, projectPath string) error {
	resolved, err := app.resolveForMutation(ctx, projectPath)
	if err != nil {
		return err
	}
	if app.SSHConfig == nil {
		return errors.New("SSH access is not configured")
	}
	if app.Tunnels == nil {
		return errors.New("port forwarding is not configured")
	}
	if err := app.MachineManager.Destroy(ctx, machine.Target{
		ProjectPath: resolved.Path,
		MachineName: resolved.MachineName,
	}); err != nil {
		return err
	}
	// The tunnel points at a machine that no longer exists, so its process goes
	// first; repeating this stays safe.
	if err := app.Tunnels.Remove(resolved.MachineName); err != nil {
		return err
	}
	// The machine is gone, so its managed host and host keys are stale. Removal
	// is idempotent, which keeps a repeated destroy safe.
	if err := app.SSHConfig.Remove(resolved.MachineName); err != nil {
		return fmt.Errorf("remove SSH access to %q: %w", resolved.MachineName, err)
	}
	return nil
}

func (app App) resolveForMutation(ctx context.Context, projectPath string) (project.Project, error) {
	resolved, err := project.Resolve(projectPath)
	if err != nil {
		return project.Project{}, err
	}
	if _, err := app.HostChecker.Check(ctx); err != nil {
		return project.Project{}, err
	}
	if app.MachineManager == nil {
		return project.Project{}, errors.New("machine lifecycle is not configured")
	}
	return resolved, nil
}

func imageVersion(reference string) string {
	lastSlash := strings.LastIndexByte(reference, '/')
	lastColon := strings.LastIndexByte(reference, ':')
	if lastColon > lastSlash && lastColon+1 < len(reference) {
		return reference[lastColon+1:]
	}
	return reference
}
