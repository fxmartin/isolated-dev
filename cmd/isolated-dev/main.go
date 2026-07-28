package main

import (
	"context"
	"os"
	"path/filepath"

	"github.com/fxmartin/isolated-dev/internal/app"
	"github.com/fxmartin/isolated-dev/internal/baseimage"
	"github.com/fxmartin/isolated-dev/internal/cli"
	"github.com/fxmartin/isolated-dev/internal/guest"
	"github.com/fxmartin/isolated-dev/internal/host"
	"github.com/fxmartin/isolated-dev/internal/machine"
	"github.com/fxmartin/isolated-dev/internal/projectcmd"
	"github.com/fxmartin/isolated-dev/internal/sshconfig"
	"github.com/fxmartin/isolated-dev/internal/state"
	"github.com/fxmartin/isolated-dev/internal/zed"
)

var version = "dev"

func main() {
	store, err := state.DefaultStore()
	if err != nil {
		os.Stderr.WriteString("isolated-dev: " + err.Error() + "\n")
		os.Exit(1)
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		os.Stderr.WriteString("isolated-dev: resolve home directory: " + err.Error() + "\n")
		os.Exit(1)
	}
	runner := baseimage.ExecRunner{}
	imageManager := &baseimage.Manager{Runner: runner}
	machineManager := &machine.Manager{
		Runner:       runner,
		StateStore:   store,
		DockerWaiter: imageManager,
		ImageEnsurer: imageManager,
	}
	application := app.App{
		Version:          version,
		HostChecker:      host.DefaultChecker(),
		StateStore:       store,
		MachineManager:   machineManager,
		GuestProvisioner: guest.Provisioner{Runner: runner},
		ImageEnsurer:     imageManager,
		AddressResolver:  machineManager,
		SSHConfig:        sshconfig.Manager{SSHDir: filepath.Join(homeDir, ".ssh")},
		Zed:              zed.Launcher{Runner: runner},
		ProjectCommands: projectcmd.Executor{
			Runner:       projectcmd.ExecRunner{},
			DockerWaiter: imageManager,
		},
		WarningOutput: os.Stderr,
	}
	os.Exit(cli.Run(os.Args[1:], cli.Dependencies{
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Version: version,
		Status: func(path string) error {
			return application.Status(context.Background(), path, os.Stdout)
		},
		Up: func(path string) error {
			return application.Up(context.Background(), path, os.Stdout)
		},
		Open: func(path string) error {
			return application.Open(context.Background(), path, os.Stdout)
		},
		Stop: func(path string) error {
			return application.Stop(context.Background(), path)
		},
		Destroy: func(path string) error {
			return application.Destroy(context.Background(), path)
		},
		Upgrade: func(path string, confirmed bool) error {
			return application.Upgrade(context.Background(), path, confirmed, os.Stdout)
		},
		Run: func(path string, name string) (int, error) {
			return application.Run(context.Background(), path, name, projectcmd.Streams{
				Stdin:  os.Stdin,
				Stdout: os.Stdout,
				Stderr: os.Stderr,
			})
		},
	}))
}
