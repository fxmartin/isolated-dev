package main

import (
	"context"
	"os"

	"github.com/fxmartin/isolated-dev/internal/app"
	"github.com/fxmartin/isolated-dev/internal/cli"
	"github.com/fxmartin/isolated-dev/internal/host"
	"github.com/fxmartin/isolated-dev/internal/state"
)

var version = "dev"

func main() {
	store, err := state.DefaultStore()
	if err != nil {
		os.Stderr.WriteString("isolated-dev: " + err.Error() + "\n")
		os.Exit(1)
	}
	application := app.App{
		Version:     version,
		HostChecker: host.DefaultChecker(),
		StateStore:  store,
	}
	os.Exit(cli.Run(os.Args[1:], cli.Dependencies{
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Version: version,
		Status: func(path string) error {
			return application.Status(context.Background(), path, os.Stdout)
		},
	}))
}
