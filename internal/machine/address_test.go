package machine

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const guestAddresses = `[
  {"ifname":"lo","addr_info":[{"family":"inet","local":"127.0.0.1","scope":"host"}]},
  {"ifname":"eth0","addr_info":[
    {"family":"inet6","local":"fd00::1","scope":"global"},
    {"family":"inet","local":"192.168.64.5","scope":"global"}
  ]},
  {"ifname":"docker0","addr_info":[{"family":"inet","local":"172.17.0.1","scope":"global"}]}
]`

func TestAddressReadsTheGuestInterfaceReachableFromMacOS(t *testing.T) {
	t.Parallel()

	runner := &runnerStub{responses: []response{{output: []byte(guestAddresses)}}}
	manager := Manager{Runner: runner}

	address, err := manager.Address(context.Background(), Target{
		ProjectPath: "/Users/fx/dev/app",
		MachineName: "isolated-dev-app-abcd1234",
	})
	if err != nil {
		t.Fatalf("Address() error = %v", err)
	}
	if address != "192.168.64.5" {
		t.Errorf("Address() = %q, want the guest interface address", address)
	}
	want := []string{
		"machine", "run",
		"--name", "isolated-dev-app-abcd1234",
		"--",
		"/usr/sbin/ip", "-json", "-4", "addr", "show", "scope", "global",
	}
	if len(runner.calls) != 1 || !reflect.DeepEqual(runner.calls[0].args, want) {
		t.Errorf("calls = %+v, want a single address query %v", runner.calls, want)
	}
}

func TestAddressIgnoresDockerAndBridgeInterfaces(t *testing.T) {
	t.Parallel()

	// Docker runs inside the guest, so its bridges answer the same query with
	// addresses macOS cannot reach.
	dockerOnly := `[
      {"ifname":"docker0","addr_info":[{"family":"inet","local":"172.17.0.1","scope":"global"}]},
      {"ifname":"br-1a2b3c","addr_info":[{"family":"inet","local":"172.18.0.1","scope":"global"}]},
      {"ifname":"veth9f8","addr_info":[{"family":"inet","local":"172.19.0.1","scope":"global"}]}
    ]`
	manager := Manager{Runner: &runnerStub{responses: []response{{output: []byte(dockerOnly)}}}}

	_, err := manager.Address(context.Background(), Target{
		ProjectPath: "/Users/fx/dev/app",
		MachineName: "isolated-dev-app-abcd1234",
	})
	if err == nil || !strings.Contains(err.Error(), "no reachable IPv4 address") {
		t.Fatalf("Address() error = %v, want the container bridges rejected", err)
	}
}

func TestAddressReportsAFailedQuery(t *testing.T) {
	t.Parallel()

	manager := Manager{Runner: &runnerStub{responses: []response{{
		output: []byte("machine is not running"),
		err:    errors.New("exit status 1"),
	}}}}

	_, err := manager.Address(context.Background(), Target{
		ProjectPath: "/Users/fx/dev/app",
		MachineName: "isolated-dev-app-abcd1234",
	})
	if err == nil || !strings.Contains(err.Error(), "machine is not running") {
		t.Fatalf("Address() error = %v, want the guest output reported", err)
	}
}

func TestAddressReportsUnreadableOutput(t *testing.T) {
	t.Parallel()

	manager := Manager{Runner: &runnerStub{responses: []response{{output: []byte("not json")}}}}

	_, err := manager.Address(context.Background(), Target{
		ProjectPath: "/Users/fx/dev/app",
		MachineName: "isolated-dev-app-abcd1234",
	})
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("Address() error = %v, want a decode failure", err)
	}
}

func TestAddressRejectsAnUnusableTarget(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		manager Manager
		target  Target
		want    string
	}{
		"runner missing": {
			manager: Manager{},
			target: Target{
				ProjectPath: "/Users/fx/dev/app",
				MachineName: "isolated-dev-app-abcd1234",
			},
			want: "runner is not configured",
		},
		"machine name invalid": {
			manager: Manager{Runner: &runnerStub{}},
			target:  Target{ProjectPath: "/Users/fx/dev/app", MachineName: "../evil"},
			want:    "invalid machine name",
		},
		"project path relative": {
			manager: Manager{Runner: &runnerStub{}},
			target:  Target{ProjectPath: "app", MachineName: "isolated-dev-app-abcd1234"},
			want:    "project path must be absolute",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := testCase.manager.Address(context.Background(), testCase.target)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("Address() error = %v, want %q", err, testCase.want)
			}
		})
	}
}
