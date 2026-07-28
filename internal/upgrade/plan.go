// Package upgrade renders the preview shown before a project machine is
// recreated on a newer base image.
package upgrade

import (
	"fmt"
	"io"
)

type Plan struct {
	ProjectPath      string
	MachineName      string
	CurrentBaseImage string
	CurrentVersion   string
	TargetBaseImage  string
	TargetVersion    string
}

// DiscardedCategories names the guest-only state a recreation cannot carry
// forward. It is a fixed list rather than an inspection of the live machine:
// the preview must be printable before anything is destroyed, and naming a
// category the machine happens not to use is far safer than omitting one it
// does.
func DiscardedCategories() []string {
	return []string{
		"guest packages installed after provisioning",
		"Docker images and build cache",
		"Docker Compose volumes and the data inside them",
		"guest home directory contents outside the mounted project",
		"guest-only data such as databases, shell history, and tool caches",
	}
}

func Write(writer io.Writer, plan Plan) error {
	lines := []string{
		"Upgrade: " + plan.ProjectPath,
		"Machine: " + plan.MachineName,
		fmt.Sprintf("Current base image: %s (version %s)", plan.CurrentBaseImage, plan.CurrentVersion),
		fmt.Sprintf("Target base image: %s (version %s)", plan.TargetBaseImage, plan.TargetVersion),
		"Recreating the machine discards state that exists only inside it:",
	}
	for _, category := range DiscardedCategories() {
		lines = append(lines, "  - "+category)
	}
	lines = append(
		lines,
		"Preserved: the macOS project source at "+plan.ProjectPath+" is mounted, not copied.",
	)
	for _, line := range lines {
		if _, err := fmt.Fprintln(writer, line); err != nil {
			return fmt.Errorf("write upgrade preview: %w", err)
		}
	}
	return nil
}
