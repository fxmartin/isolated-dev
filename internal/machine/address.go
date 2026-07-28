package machine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
)

// guestInterface is the subset of `ip -json addr` that identifies a usable
// guest address.
type guestInterface struct {
	Name      string `json:"ifname"`
	Addresses []struct {
		Family string `json:"family"`
		Local  string `json:"local"`
	} `json:"addr_info"`
}

// Address resolves the address at which macOS reaches the project machine.
//
// Apple Container Machine 1.1.0 does not report the machine address, so it is
// read from inside the guest. The absolute path matches the other in-guest
// calls in this repository: `machine run` does not guarantee a PATH.
func (manager Manager) Address(ctx context.Context, target Target) (string, error) {
	if manager.Runner == nil {
		return "", errors.New("machine runner is not configured")
	}
	if err := validateTarget(target); err != nil {
		return "", err
	}
	output, err := manager.Runner.Run(
		ctx,
		"container",
		"machine", "run",
		"--name", target.MachineName,
		"--",
		"/usr/sbin/ip", "-json", "-4", "addr", "show", "scope", "global",
	)
	if err != nil {
		return "", fmt.Errorf(
			"resolve the address of machine %q: %w\n%s",
			target.MachineName,
			err,
			output,
		)
	}

	var interfaces []guestInterface
	if err := json.Unmarshal(output, &interfaces); err != nil {
		return "", fmt.Errorf("decode the addresses of machine %q: %w", target.MachineName, err)
	}
	for _, guest := range interfaces {
		if !reachableInterface(guest.Name) {
			continue
		}
		for _, address := range guest.Addresses {
			parsed := net.ParseIP(address.Local)
			if address.Family == "inet" && parsed != nil && parsed.To4() != nil {
				return parsed.String(), nil
			}
		}
	}
	return "", fmt.Errorf(
		"machine %q has no reachable IPv4 address; start it with up before connecting",
		target.MachineName,
	)
}

// reachableInterface excludes the interfaces that exist only inside the guest.
// Docker runs in the machine, and its bridges carry addresses that macOS cannot
// route to.
func reachableInterface(name string) bool {
	if name == "lo" || strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "veth") {
		return false
	}
	return !strings.HasPrefix(name, "br-")
}
