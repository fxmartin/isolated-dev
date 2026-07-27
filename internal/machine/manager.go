package machine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fxmartin/isolated-dev/internal/state"
)

var machineNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type StateStore interface {
	Load(string) (state.Project, error)
	Save(state.Project) error
	Delete(string) error
}

type DockerWaiter interface {
	WaitDocker(context.Context, string) error
}

type ImageEnsurer interface {
	EnsureReference(context.Context, string) error
}

type Manager struct {
	Runner       Runner
	StateStore   StateStore
	DockerWaiter DockerWaiter
	ImageEnsurer ImageEnsurer
	BootTries    int
	RetryDelay   time.Duration
	Sleep        func(time.Duration)
}

type Request struct {
	ProjectPath      string
	MachineName      string
	BaseImage        string
	BaseImageVersion string
	CPUs             int
	MemoryGB         int
	MountScope       string
}

type UpResult struct {
	Created bool
}

type machineInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

func (manager Manager) Up(ctx context.Context, request Request) (UpResult, error) {
	if err := manager.validateUp(request); err != nil {
		return UpResult{}, err
	}

	stored, loadErr := manager.StateStore.Load(request.MachineName)
	switch {
	case loadErr == nil:
		if err := validatePinnedConfiguration(stored, request); err != nil {
			return UpResult{}, err
		}
	case !errors.Is(loadErr, state.ErrNotFound):
		return UpResult{}, fmt.Errorf("load project state: %w", loadErr)
	}

	_, exists, err := manager.find(ctx, request.MachineName)
	if err != nil {
		return UpResult{}, err
	}
	if exists && errors.Is(loadErr, state.ErrNotFound) {
		return UpResult{}, fmt.Errorf(
			"machine %q exists but is not managed by this project; delete or rename it before retrying",
			request.MachineName,
		)
	}

	created := false
	if !exists {
		if manager.ImageEnsurer != nil {
			if err := manager.ImageEnsurer.EnsureReference(ctx, request.BaseImage); err != nil {
				return UpResult{}, fmt.Errorf("ensure base image: %w", err)
			}
		}
		if errors.Is(loadErr, state.ErrNotFound) {
			if err := manager.StateStore.Save(request.projectState()); err != nil {
				return UpResult{}, fmt.Errorf("record project state: %w", err)
			}
		}
		if err := manager.create(ctx, request); err != nil {
			return UpResult{}, err
		}
		created = true
	}

	if err := manager.waitForBoot(ctx, request.MachineName); err != nil {
		return UpResult{Created: created}, err
	}
	if err := manager.DockerWaiter.WaitDocker(ctx, request.MachineName); err != nil {
		return UpResult{Created: created}, fmt.Errorf("wait for Docker: %w", err)
	}
	return UpResult{Created: created}, nil
}

func (manager Manager) Stop(ctx context.Context, machineName string) error {
	if manager.Runner == nil {
		return errors.New("machine runner is not configured")
	}
	if err := validateMachineName(machineName); err != nil {
		return err
	}
	machine, exists, err := manager.find(ctx, machineName)
	if err != nil {
		return err
	}
	if !exists || strings.EqualFold(machine.Status, "stopped") {
		return nil
	}
	if err := manager.ensureOwned(machineName); err != nil {
		return err
	}
	output, err := manager.Runner.Run(ctx, "container", "machine", "stop", machineName)
	if err != nil {
		return fmt.Errorf("stop machine %q: %w\n%s", machineName, err, output)
	}
	return nil
}

func (manager Manager) Destroy(ctx context.Context, machineName string) error {
	if manager.Runner == nil {
		return errors.New("machine runner is not configured")
	}
	if manager.StateStore == nil {
		return errors.New("project state store is not configured")
	}
	if err := validateMachineName(machineName); err != nil {
		return err
	}
	machine, exists, err := manager.find(ctx, machineName)
	if err != nil {
		return err
	}
	if exists {
		if err := manager.ensureOwned(machineName); err != nil {
			return err
		}
		if !strings.EqualFold(machine.Status, "stopped") {
			output, err := manager.Runner.Run(ctx, "container", "machine", "stop", machineName)
			if err != nil {
				return fmt.Errorf("stop machine %q before deletion: %w\n%s", machineName, err, output)
			}
		}
		output, err := manager.Runner.Run(ctx, "container", "machine", "delete", machineName)
		if err != nil {
			return fmt.Errorf("delete machine %q: %w\n%s", machineName, err, output)
		}
	}
	if err := manager.StateStore.Delete(machineName); err != nil {
		return fmt.Errorf("delete project state: %w", err)
	}
	return nil
}

func (manager Manager) ensureOwned(machineName string) error {
	if manager.StateStore == nil {
		return errors.New("project state store is not configured")
	}
	stored, err := manager.StateStore.Load(machineName)
	if errors.Is(err, state.ErrNotFound) {
		return fmt.Errorf("machine %q exists but is not managed by this project", machineName)
	}
	if err != nil {
		return fmt.Errorf("load project state: %w", err)
	}
	if stored.MachineName != machineName {
		return fmt.Errorf(
			"project state identifies machine %q instead of %q; refusing lifecycle operation",
			stored.MachineName,
			machineName,
		)
	}
	return nil
}

func (manager Manager) validateUp(request Request) error {
	if manager.Runner == nil {
		return errors.New("machine runner is not configured")
	}
	if manager.StateStore == nil {
		return errors.New("project state store is not configured")
	}
	if manager.DockerWaiter == nil {
		return errors.New("Docker readiness waiter is not configured")
	}
	if err := validateMachineName(request.MachineName); err != nil {
		return err
	}
	if !filepath.IsAbs(request.ProjectPath) {
		return errors.New("project path must be absolute")
	}
	if strings.TrimSpace(request.BaseImage) == "" {
		return errors.New("base image must not be empty")
	}
	if strings.TrimSpace(request.BaseImageVersion) == "" {
		return errors.New("base-image version must not be empty")
	}
	if request.CPUs <= 0 {
		return errors.New("CPUs must be positive")
	}
	if request.MemoryGB <= 0 {
		return errors.New("memory must be positive")
	}
	if request.MountScope != "home" && request.MountScope != "repository" {
		return fmt.Errorf("unsupported mount scope %q", request.MountScope)
	}
	return nil
}

func (manager Manager) create(ctx context.Context, request Request) error {
	homeMount := "none"
	if request.MountScope == "home" {
		homeMount = "rw"
	}
	output, err := manager.Runner.Run(
		ctx,
		"container",
		"machine", "create",
		"--name", request.MachineName,
		"--cpus", strconv.Itoa(request.CPUs),
		"--memory", strconv.Itoa(request.MemoryGB)+"G",
		"--home-mount", homeMount,
		request.BaseImage,
	)
	if err != nil {
		return fmt.Errorf("create machine %q: %w\n%s", request.MachineName, err, output)
	}
	return nil
}

func (manager Manager) waitForBoot(ctx context.Context, machineName string) error {
	attempts := manager.BootTries
	if attempts <= 0 {
		attempts = 2
	}
	delay := manager.RetryDelay
	if delay <= 0 {
		delay = time.Second
	}
	sleep := manager.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}

	var lastErr error
	var lastOutput []byte
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		lastOutput, lastErr = manager.Runner.Run(
			ctx,
			"container",
			"machine", "run",
			"--name", machineName,
			"--",
			"/usr/bin/true",
		)
		if lastErr == nil {
			return nil
		}
		if attempt+1 < attempts {
			sleep(delay)
		}
	}
	return fmt.Errorf("machine %q did not become ready: %w\n%s", machineName, lastErr, lastOutput)
}

func (manager Manager) find(
	ctx context.Context,
	machineName string,
) (machineInfo, bool, error) {
	output, err := manager.Runner.Run(ctx, "container", "machine", "list", "--format", "json")
	if err != nil {
		return machineInfo{}, false, fmt.Errorf("list machines: %w\n%s", err, output)
	}
	var machines []machineInfo
	if err := json.Unmarshal(output, &machines); err != nil {
		return machineInfo{}, false, fmt.Errorf("decode machine list: %w", err)
	}
	for _, machine := range machines {
		name := machine.ID
		if name == "" {
			name = machine.Name
		}
		if name == machineName {
			return machine, true, nil
		}
	}
	return machineInfo{}, false, nil
}

func validatePinnedConfiguration(stored state.Project, request Request) error {
	fields := []struct {
		name    string
		current any
		wanted  any
	}{
		{name: "project path", current: stored.ProjectPath, wanted: request.ProjectPath},
		{name: "machine name", current: stored.MachineName, wanted: request.MachineName},
		{name: "base image", current: stored.BaseImage, wanted: request.BaseImage},
		{name: "base-image version", current: stored.BaseImageVersion, wanted: request.BaseImageVersion},
		{name: "mount scope", current: stored.MountScope, wanted: request.MountScope},
		{name: "CPUs", current: stored.CPUs, wanted: request.CPUs},
		{name: "memory", current: stored.MemoryGB, wanted: request.MemoryGB},
	}
	for _, field := range fields {
		if field.current != field.wanted {
			return fmt.Errorf(
				"%s changed from %v to %v; explicitly recreate the machine to apply this change",
				field.name,
				field.current,
				field.wanted,
			)
		}
	}
	return nil
}

func validateMachineName(machineName string) error {
	if !machineNamePattern.MatchString(machineName) {
		return fmt.Errorf("invalid machine name %q", machineName)
	}
	return nil
}

func (request Request) projectState() state.Project {
	return state.Project{
		SchemaVersion:    1,
		ProjectPath:      request.ProjectPath,
		MachineName:      request.MachineName,
		BaseImage:        request.BaseImage,
		BaseImageVersion: request.BaseImageVersion,
		MountScope:       request.MountScope,
		CPUs:             request.CPUs,
		MemoryGB:         request.MemoryGB,
	}
}
