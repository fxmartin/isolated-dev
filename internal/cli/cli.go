package cli

import (
	"fmt"
	"io"
)

type Dependencies struct {
	Stdout   io.Writer
	Stderr   io.Writer
	Version  string
	Status   func(string) error
	OnMutate func()
}

func Run(args []string, deps Dependencies) int {
	if len(args) == 1 && (args[0] == "--version" || args[0] == "version") {
		fmt.Fprintf(deps.Stdout, "isolated-dev %s\n", deps.Version)
		return 0
	}
	if len(args) == 2 && args[0] == "status" {
		if deps.Status == nil {
			fmt.Fprintln(deps.Stderr, "status: command is unavailable")
			return 1
		}
		if err := deps.Status(args[1]); err != nil {
			fmt.Fprintf(deps.Stderr, "status: %v\n", err)
			return 1
		}
		return 0
	}

	fmt.Fprintln(deps.Stderr, "usage: isolated-dev <status PROJECT|--version>")
	return 2
}
