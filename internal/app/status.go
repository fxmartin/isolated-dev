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

type App struct {
	Version          string
	HostChecker      host.Checker
	StateStore       state.Store
	MachineManager   MachineManager
	GuestProvisioner GuestProvisioner
	HomeDir          string
	WarningOutput    io.Writer
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
		snapshot.MountScope = stored.MountScope
		snapshot.TunnelStatus = "unknown"
		snapshot.GuestUser = stored.GuestUser
		snapshot.GuestUID = stored.GuestUID
		snapshot.GuestGID = stored.GuestGID
		snapshot.GuestProjectPath = stored.GuestProjectPath
		snapshot.Config.Resources.CPUs = stored.CPUs
		snapshot.Config.Resources.MemoryGB = stored.MemoryGB
	} else if !errors.Is(err, state.ErrNotFound) {
		return fmt.Errorf("load project state: %w", err)
	}

	return statusview.Write(output, snapshot)
}

func (app App) Up(ctx context.Context, projectPath string, output io.Writer) error {
	resolved, err := project.Resolve(projectPath)
	if err != nil {
		return err
	}
	effectiveConfig, err := config.Load(resolved.Path)
	if err != nil {
		return err
	}
	if _, err := app.HostChecker.Check(ctx); err != nil {
		return err
	}
	if app.MachineManager == nil {
		return errors.New("machine lifecycle is not configured")
	}
	if app.GuestProvisioner == nil {
		return errors.New("guest provisioning is not configured")
	}
	if !baseimage.IsManagedReference(effectiveConfig.BaseImage) {
		return fmt.Errorf(
			"base image %q is not a managed isolated-dev image; refusing to grant it read-write access to the full home directory",
			effectiveConfig.BaseImage,
		)
	}
	canonicalHome, err := app.canonicalHome()
	if err != nil {
		return err
	}
	if err := validateHomeMountedProject(canonicalHome, resolved.Path); err != nil {
		return err
	}
	// The guest identity and its authorized public keys are resolved before any
	// machine mutation so a missing key fails without leaving a half-configured
	// machine behind.
	identity, err := app.guestIdentity()
	if err != nil {
		return err
	}
	publicKeys, err := guest.PublicKeys(filepath.Join(canonicalHome, ".ssh"))
	if err != nil {
		return err
	}
	if err := app.warn(
		"warning: this machine receives read-write access to your full home directory",
	); err != nil {
		return fmt.Errorf("write full-home mount warning: %w", err)
	}
	if err := app.warnMissingSecretFiles(resolved.Path, effectiveConfig.Secrets); err != nil {
		return err
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
		return err
	}
	provisioned, err := app.GuestProvisioner.Provision(ctx, guest.Request{
		MachineName: resolved.MachineName,
		ProjectPath: resolved.Path,
		HomeDir:     canonicalHome,
		Identity:    identity,
		PublicKeys:  publicKeys,
	})
	if err != nil {
		return err
	}
	if err := app.recordGuest(resolved.MachineName, provisioned); err != nil {
		return err
	}
	if !provisioned.OwnershipMatched {
		if err := app.warn(
			"warning: the mounted project at %s is not owned by %s (%d:%d); files created in Linux may not match your macOS ownership",
			provisioned.GuestProjectPath,
			provisioned.Identity.Username,
			provisioned.Identity.UID,
			provisioned.Identity.GID,
		); err != nil {
			return fmt.Errorf("write mount ownership warning: %w", err)
		}
	}

	outcome := "ready"
	if result.Created {
		outcome = "created"
	}
	if _, err := fmt.Fprintf(output, "%s %s\n", outcome, resolved.Path); err != nil {
		return fmt.Errorf("write up summary: %w", err)
	}
	if _, err := fmt.Fprintf(
		output,
		"guest %s (%d:%d) at %s\n",
		provisioned.Identity.Username,
		provisioned.Identity.UID,
		provisioned.Identity.GID,
		provisioned.GuestProjectPath,
	); err != nil {
		return fmt.Errorf("write guest summary: %w", err)
	}
	return nil
}

func (app App) guestIdentity() (guest.Identity, error) {
	if app.ResolveIdentity != nil {
		return app.ResolveIdentity()
	}
	return guest.ResolveIdentity()
}

// recordGuest persists the provisioned identity and mounted-project path so
// `status` can report them without touching the machine.
func (app App) recordGuest(machineName string, provisioned guest.Result) error {
	stored, err := app.StateStore.Load(machineName)
	if err != nil {
		return fmt.Errorf("load project state: %w", err)
	}
	stored.GuestUser = provisioned.Identity.Username
	stored.GuestUID = provisioned.Identity.UID
	stored.GuestGID = provisioned.Identity.GID
	stored.GuestProjectPath = provisioned.GuestProjectPath
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
		if _, err := os.Stat(filepath.Join(projectPath, reference)); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("check secret file reference %s: %w", reference, err)
		}
		if err := app.warn(
			"warning: referenced secret file %s is not present in the project; its contents are never read",
			reference,
		); err != nil {
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
	return app.MachineManager.Stop(ctx, machine.Target{
		ProjectPath: resolved.Path,
		MachineName: resolved.MachineName,
	})
}

func (app App) Destroy(ctx context.Context, projectPath string) error {
	resolved, err := app.resolveForMutation(ctx, projectPath)
	if err != nil {
		return err
	}
	return app.MachineManager.Destroy(ctx, machine.Target{
		ProjectPath: resolved.Path,
		MachineName: resolved.MachineName,
	})
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
