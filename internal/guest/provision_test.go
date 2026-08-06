package guest

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

type recordedCall struct {
	Name string
	Args []string
}

type fakeRunner struct {
	calls  []recordedCall
	handle func(recordedCall) ([]byte, error)
}

func (runner *fakeRunner) Run(
	_ context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	call := recordedCall{Name: name, Args: args}
	runner.calls = append(runner.calls, call)
	if runner.handle == nil {
		return nil, nil
	}
	return runner.handle(call)
}

func (runner *fakeRunner) statTargets() []string {
	var targets []string
	for _, call := range runner.calls {
		if len(call.Args) > 0 && call.Args[len(call.Args)-2] == "%u:%g" {
			targets = append(targets, call.Args[len(call.Args)-1])
		}
	}
	return targets
}

// provisionCall returns the single call that runs the provisioning script,
// which the mount probe now precedes.
func (runner *fakeRunner) provisionCall() (recordedCall, bool) {
	for _, call := range runner.calls {
		if !isStat(call) {
			return call, true
		}
	}
	return recordedCall{}, false
}

func isStat(call recordedCall) bool {
	for _, arg := range call.Args {
		if arg == "/usr/bin/stat" {
			return true
		}
	}
	return false
}

const (
	hostHome    = "/Users/fxmartin"
	hostProject = "/Users/fxmartin/dev/app"
)

func testRequest() Request {
	return Request{
		MachineName: "isolated-dev-app-0123456789abcdef",
		ProjectPath: hostProject,
		HomeDir:     hostHome,
		Identity:    Identity{Username: "fxmartin", UID: 501, GID: 20},
		PublicKeys:  []string{ed25519Key},
	}
}

func statResponder(matching map[string]string) func(recordedCall) ([]byte, error) {
	return func(call recordedCall) ([]byte, error) {
		if !isStat(call) {
			return nil, nil
		}
		target := call.Args[len(call.Args)-1]
		ownership, found := matching[target]
		if !found {
			return []byte("stat: cannot statx: No such file or directory"), errors.New("exit status 1")
		}
		return []byte(ownership + "\n"), nil
	}
}

func TestProvisionConfiguresIdentityCredentialsAndMount(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{handle: statResponder(map[string]string{
		hostProject + "/.git": "501:20",
	})}
	provisioner := Provisioner{Runner: runner}

	result, err := provisioner.Provision(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	if result.GuestProjectPath != hostProject {
		t.Errorf("GuestProjectPath = %q, want the mounted host path", result.GuestProjectPath)
	}
	if !result.OwnershipMatched {
		t.Error("OwnershipMatched = false, want the mount to map to the host UID and GID")
	}
	if result.Identity != (Identity{Username: "fxmartin", UID: 501, GID: 20}) {
		t.Errorf("Identity = %+v, want the requested identity", result.Identity)
	}

	provision, found := runner.provisionCall()
	if !found {
		t.Fatal("no provisioning command was run")
	}
	if provision.Name != "container" {
		t.Fatalf("command = %q, want the Apple Container CLI", provision.Name)
	}
	joined := strings.Join(provision.Args, " ")
	for _, want := range []string{
		"machine run --name isolated-dev-app-0123456789abcdef --root",
		"/usr/bin/bash -c",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("provision command %q missing %q", joined, want)
		}
	}
	positional := provision.Args[len(provision.Args)-4:]
	if positional[0] != "fxmartin" || positional[1] != "501" || positional[2] != "20" {
		t.Errorf("positional arguments = %#v, want user, UID, and GID", positional)
	}
	if positional[3] != ed25519Key {
		t.Errorf("public key argument = %q, want the collected public key", positional[3])
	}
	script := provision.Args[len(provision.Args)-6]
	for _, want := range []string{
		"PermitRootLogin no",
		"PasswordAuthentication no",
		"AllowAgentForwarding yes",
		"NOPASSWD:ALL",
		"--groups docker",
		"authorized_keys",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("provisioning script is missing %q", want)
		}
	}
	if strings.Contains(script, "PRIVATE KEY") {
		t.Error("provisioning script references private key material")
	}
}

// A mount that exposes the macOS home at the guest home path would have
// provisioning rewrite the developer's own ~/.ssh/authorized_keys, so it is
// refused before the guest is touched rather than discovered afterwards.
func TestProvisionRefusesAProjectMountedInsideTheGuestHome(t *testing.T) {
	t.Parallel()

	guestPath := "/home/fxmartin/dev/app"
	runner := &fakeRunner{handle: statResponder(map[string]string{
		guestPath + "/.git": "501:20",
	})}

	_, err := Provisioner{Runner: runner}.Provision(context.Background(), testRequest())
	if err == nil || !strings.Contains(err.Error(), "guest home") {
		t.Fatalf("Provision() error = %v, want the guest home collision refused", err)
	}
	targets := runner.statTargets()
	if len(targets) != 2 || targets[0] != hostProject+"/.git" {
		t.Errorf("stat targets = %#v, want the host path probed first", targets)
	}
	for _, call := range runner.calls {
		if !isStat(call) {
			t.Fatalf("guest was configured despite the refusal: %#v", call)
		}
	}
}

func TestProvisionLocatesTheProjectBeforeConfiguringTheGuest(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{handle: statResponder(nil)}

	_, err := Provisioner{Runner: runner}.Provision(context.Background(), testRequest())
	if err == nil || !strings.Contains(err.Error(), "could not find the project") {
		t.Fatalf("Provision() error = %v, want the missing project reported", err)
	}
	for _, call := range runner.calls {
		if !isStat(call) {
			t.Fatalf("guest was configured before the project was located: %#v", call)
		}
	}
}

func TestProvisionReportsMountOwnershipMismatchWithoutFailing(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{handle: statResponder(map[string]string{
		hostProject + "/.git": "0:0",
	})}

	result, err := Provisioner{Runner: runner}.Provision(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	if result.OwnershipMatched {
		t.Error("OwnershipMatched = true, want a root-owned mount reported as unmatched")
	}
	if result.GuestProjectPath != hostProject {
		t.Errorf("GuestProjectPath = %q, want the discovered path", result.GuestProjectPath)
	}
}

func TestProvisionReportsGuestFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		handle func(recordedCall) ([]byte, error)
		want   string
	}{
		{
			name: "provisioning script",
			handle: func(call recordedCall) ([]byte, error) {
				if isStat(call) {
					return []byte("501:20\n"), nil
				}
				return []byte("useradd: UID 501 is not unique"), errors.New("exit status 1")
			},
			want: "configure guest identity",
		},
		{
			name:   "project not visible",
			handle: statResponder(nil),
			want:   "could not find the project",
		},
		{
			name: "unreadable ownership",
			handle: func(call recordedCall) ([]byte, error) {
				if isStat(call) {
					return []byte("not-ownership\n"), nil
				}
				return nil, nil
			},
			want: "ownership",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := &fakeRunner{handle: test.handle}

			_, err := Provisioner{Runner: runner}.Provision(context.Background(), testRequest())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Provision() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestProvisionValidatesTheRequestBeforeTouchingTheGuest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Request)
		runner Runner
		want   string
	}{
		{name: "missing runner", mutate: func(*Request) {}, runner: nil, want: "runner is not configured"},
		{
			name:   "machine name",
			mutate: func(request *Request) { request.MachineName = "../escape" },
			want:   "machine name",
		},
		{
			name:   "relative project path",
			mutate: func(request *Request) { request.ProjectPath = "app" },
			want:   "project path",
		},
		{
			name:   "relative home",
			mutate: func(request *Request) { request.HomeDir = "home" },
			want:   "home directory",
		},
		{
			name:   "project outside home",
			mutate: func(request *Request) { request.ProjectPath = "/opt/app" },
			want:   "outside",
		},
		{
			name:   "invalid identity",
			mutate: func(request *Request) { request.Identity.UID = 0 },
			want:   "must not be root",
		},
		{
			name:   "no public keys",
			mutate: func(request *Request) { request.PublicKeys = nil },
			want:   "public key",
		},
		{
			name:   "invalid package name",
			mutate: func(request *Request) { request.Packages = []string{"two words"} },
			want:   "package",
		},
		{
			name:   "private key material",
			mutate: func(request *Request) { request.PublicKeys = []string{privateKeyHeader} },
			want:   "public key",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := testRequest()
			test.mutate(&request)
			runner := test.runner
			recorder := &fakeRunner{}
			if test.name != "missing runner" {
				runner = recorder
			}

			_, err := Provisioner{Runner: runner}.Provision(context.Background(), request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Provision() error = %v, want containing %q", err, test.want)
			}
			if len(recorder.calls) != 0 {
				t.Fatalf("guest commands = %#v, want none", recorder.calls)
			}
		})
	}
}

// packagesCall returns the call that runs the package script, which announces
// itself through the argv name the script is started under.
func (runner *fakeRunner) packagesCall() (recordedCall, int) {
	for index, call := range runner.calls {
		for _, arg := range call.Args {
			if arg == "isolated-dev-packages" {
				return call, index
			}
		}
	}
	return recordedCall{}, -1
}

func TestProvisionInstallsDeclaredPackages(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{handle: statResponder(map[string]string{
		hostProject + "/.git": "501:20",
	})}
	request := testRequest()
	request.Packages = []string{"golang-go", "python3-venv"}

	if _, err := (Provisioner{Runner: runner}).Provision(context.Background(), request); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}

	install, index := runner.packagesCall()
	if index == -1 {
		t.Fatal("no package installation command was run")
	}
	// The identity script owns the guest account; packages are converged for a
	// guest that already exists.
	if index != len(runner.calls)-1 {
		t.Errorf("package installation ran at call %d, want after provisioning", index)
	}
	if install.Name != "container" {
		t.Fatalf("command = %q, want the Apple Container CLI", install.Name)
	}
	joined := strings.Join(install.Args, " ")
	for _, want := range []string{
		"machine run --name isolated-dev-app-0123456789abcdef --root",
		"/usr/bin/bash -c",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("install command %q missing %q", joined, want)
		}
	}
	trailing := install.Args[len(install.Args)-2:]
	if trailing[0] != "golang-go" || trailing[1] != "python3-venv" {
		t.Errorf("package arguments = %#v, want the declared packages", trailing)
	}
	script := install.Args[len(install.Args)-4]
	for _, want := range []string{
		"dpkg-query",
		"apt-get update",
		"apt-get install -y --no-install-recommends",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("package script is missing %q", want)
		}
	}
}

func TestProvisionSkipsPackageInstallationWhenNoneAreDeclared(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{handle: statResponder(map[string]string{
		hostProject + "/.git": "501:20",
	})}

	if _, err := (Provisioner{Runner: runner}).Provision(context.Background(), testRequest()); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	if _, index := runner.packagesCall(); index != -1 {
		t.Errorf("call %d installs packages, want none for an empty declaration", index)
	}
}

func TestProvisionReportsAFailedPackageInstallation(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{}
	runner.handle = func(call recordedCall) ([]byte, error) {
		if isStat(call) {
			return []byte("501:20\n"), nil
		}
		for _, arg := range call.Args {
			if arg == "isolated-dev-packages" {
				return []byte("E: Unable to locate package no-such-tool"), errors.New("exit status 100")
			}
		}
		return nil, nil
	}
	request := testRequest()
	request.Packages = []string{"no-such-tool"}

	_, err := Provisioner{Runner: runner}.Provision(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "install declared packages") {
		t.Fatalf("Provision() error = %v, want the failed installation reported", err)
	}
	if !strings.Contains(err.Error(), "Unable to locate package") {
		t.Errorf("error = %v, want the guest output carried for diagnosis", err)
	}
}

// The repository may be the home directory itself, which makes the probed
// fallback the guest home exactly — the worst case of the collision above.
func TestProvisionRefusesAHomeRepositoryResolvedToTheGuestHome(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{handle: statResponder(map[string]string{
		filepath.Join("/home", "fxmartin", ".git"): "501:20",
	})}
	request := testRequest()
	request.ProjectPath = hostHome

	_, err := Provisioner{Runner: runner}.Provision(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "authorized_keys") {
		t.Fatalf("Provision() error = %v, want the guest home collision refused", err)
	}
}
