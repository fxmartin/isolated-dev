package sshconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func managerAt(t *testing.T) Manager {
	t.Helper()

	return Manager{SSHDir: filepath.Join(t.TempDir(), ".ssh")}
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	return string(data)
}

func TestApplyWritesTheManagedHostForZed(t *testing.T) {
	t.Parallel()

	manager := managerAt(t)
	entry := Entry{Alias: "isolated-dev-app-abcd1234", HostName: "192.168.64.5", User: "fx"}

	if err := manager.Apply(entry); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	managed := readFile(t, manager.ManagedConfigPath())
	for _, want := range []string{
		"Host isolated-dev-app-abcd1234\n",
		"    HostName 192.168.64.5\n",
		"    User fx\n",
		// Git inside Linux authenticates through the macOS agent.
		"    ForwardAgent yes\n",
		// The alias keeps host keys stable when the machine address changes.
		"    HostKeyAlias isolated-dev-app-abcd1234\n",
		"    UserKnownHostsFile \"" + manager.KnownHostsPath() + "\"\n",
		"    StrictHostKeyChecking accept-new\n",
		"    HashKnownHosts no\n",
	} {
		if !strings.Contains(managed, want) {
			t.Errorf("managed config missing %q:\n%s", want, managed)
		}
	}
}

func TestApplyIncludesTheManagedFileFromTheDeveloperConfigOnce(t *testing.T) {
	t.Parallel()

	manager := managerAt(t)
	developerConfig := "Host github.com\n    User git\n"
	if err := os.MkdirAll(manager.SSHDir, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	userConfig := filepath.Join(manager.SSHDir, "config")
	if err := os.WriteFile(userConfig, []byte(developerConfig), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	for range 3 {
		if err := manager.Apply(Entry{
			Alias:    "isolated-dev-app-abcd1234",
			HostName: "192.168.64.5",
			User:     "fx",
		}); err != nil {
			t.Fatalf("Apply() error = %v", err)
		}
	}

	content := readFile(t, userConfig)
	includes := strings.Count(content, "Include \""+manager.ManagedConfigPath()+"\"")
	if includes != 1 {
		t.Errorf("Include directives = %d, want exactly one:\n%s", includes, content)
	}
	if !strings.HasSuffix(content, developerConfig) {
		t.Errorf("developer entries were rewritten:\n%s", content)
	}
	if strings.Index(content, "Include") > strings.Index(content, "Host github.com") {
		t.Errorf("Include must precede developer host blocks to apply globally:\n%s", content)
	}
}

func TestApplyCreatesTheDeveloperConfigWhenItIsMissing(t *testing.T) {
	t.Parallel()

	manager := managerAt(t)

	if err := manager.Apply(Entry{
		Alias:    "isolated-dev-app-abcd1234",
		HostName: "192.168.64.5",
		User:     "fx",
	}); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	userConfig := filepath.Join(manager.SSHDir, "config")
	if !strings.Contains(readFile(t, userConfig), "Include \""+manager.ManagedConfigPath()+"\"") {
		t.Errorf("created config missing the managed Include:\n%s", readFile(t, userConfig))
	}
	for _, path := range []string{manager.SSHDir, filepath.Dir(manager.ManagedConfigPath())} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat(%q) error = %v", path, err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Errorf("%q mode = %v, want 0700", path, info.Mode().Perm())
		}
	}
	for _, path := range []string{userConfig, manager.ManagedConfigPath()} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat(%q) error = %v", path, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%q mode = %v, want 0600", path, info.Mode().Perm())
		}
	}
}

func TestApplyReplacesOnlyItsOwnHostBlock(t *testing.T) {
	t.Parallel()

	manager := managerAt(t)
	other := Entry{Alias: "isolated-dev-other-99999999", HostName: "192.168.64.9", User: "fx"}
	if err := manager.Apply(other); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if err := manager.Apply(Entry{
		Alias:    "isolated-dev-app-abcd1234",
		HostName: "192.168.64.5",
		User:     "fx",
	}); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	// A machine that moves to a new address is reconciled in place rather than
	// duplicated.
	if err := manager.Apply(Entry{
		Alias:    "isolated-dev-app-abcd1234",
		HostName: "192.168.64.7",
		User:     "fx",
	}); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	managed := readFile(t, manager.ManagedConfigPath())
	if count := strings.Count(managed, "Host isolated-dev-app-abcd1234\n"); count != 1 {
		t.Errorf("host blocks = %d, want one reconciled block:\n%s", count, managed)
	}
	if strings.Contains(managed, "192.168.64.5") {
		t.Errorf("stale address kept:\n%s", managed)
	}
	if !strings.Contains(managed, "Host "+other.Alias+"\n") ||
		!strings.Contains(managed, "192.168.64.9") {
		t.Errorf("other project machine lost its entry:\n%s", managed)
	}
}

func TestApplyRejectsUnusableEntries(t *testing.T) {
	t.Parallel()

	cases := map[string]Entry{
		"empty alias": {Alias: "", HostName: "192.168.64.5", User: "fx"},
		"alias with path": {
			Alias:    "isolated-dev/../evil",
			HostName: "192.168.64.5",
			User:     "fx",
		},
		"injected option": {
			Alias:    "isolated-dev-app-abcd1234",
			HostName: "192.168.64.5\n    ProxyCommand curl evil",
			User:     "fx",
		},
		"empty host name": {Alias: "isolated-dev-app-abcd1234", HostName: "", User: "fx"},
		"empty user":      {Alias: "isolated-dev-app-abcd1234", HostName: "192.168.64.5", User: ""},
		"quoted user": {
			Alias:    "isolated-dev-app-abcd1234",
			HostName: "192.168.64.5",
			User:     "f\"x",
		},
	}
	for name, entry := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			manager := managerAt(t)
			if err := manager.Apply(entry); err == nil {
				t.Fatalf("Apply(%+v) error = nil, want rejection", entry)
			}
			if _, err := os.Stat(manager.ManagedConfigPath()); err == nil {
				t.Errorf("rejected entry still wrote %q", manager.ManagedConfigPath())
			}
		})
	}
}

func TestRemoveDropsTheManagedHostAndItsHostKeys(t *testing.T) {
	t.Parallel()

	manager := managerAt(t)
	kept := Entry{Alias: "isolated-dev-other-99999999", HostName: "192.168.64.9", User: "fx"}
	if err := manager.Apply(kept); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if err := manager.Apply(Entry{
		Alias:    "isolated-dev-app-abcd1234",
		HostName: "192.168.64.5",
		User:     "fx",
	}); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if err := os.WriteFile(manager.KnownHostsPath(), []byte(
		"isolated-dev-app-abcd1234 ssh-ed25519 AAAAremoved\n"+
			"isolated-dev-other-99999999 ssh-ed25519 AAAAkept\n",
	), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// Destroy runs cleanup once; a repeated destroy must stay safe.
	for range 2 {
		if err := manager.Remove("isolated-dev-app-abcd1234"); err != nil {
			t.Fatalf("Remove() error = %v", err)
		}
	}

	managed := readFile(t, manager.ManagedConfigPath())
	if strings.Contains(managed, "isolated-dev-app-abcd1234") {
		t.Errorf("destroyed machine kept its host block:\n%s", managed)
	}
	if !strings.Contains(managed, "Host "+kept.Alias+"\n") {
		t.Errorf("other project machine lost its entry:\n%s", managed)
	}
	knownHosts := readFile(t, manager.KnownHostsPath())
	if strings.Contains(knownHosts, "AAAAremoved") {
		t.Errorf("destroyed machine kept its host key:\n%s", knownHosts)
	}
	if !strings.Contains(knownHosts, "AAAAkept") {
		t.Errorf("other host key was dropped:\n%s", knownHosts)
	}
}

func TestRemoveOnAnUnconfiguredHostIsSafe(t *testing.T) {
	t.Parallel()

	manager := managerAt(t)

	if err := manager.Remove("isolated-dev-app-abcd1234"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := os.Stat(manager.ManagedConfigPath()); err == nil {
		t.Errorf("cleanup created %q", manager.ManagedConfigPath())
	}
}

func TestRemoveRejectsAnUnusableAlias(t *testing.T) {
	t.Parallel()

	manager := managerAt(t)

	if err := manager.Remove("../../etc/hosts"); err == nil {
		t.Fatal("Remove() error = nil, want rejection")
	}
}

func TestForgetHostKeyDropsOnlyTheRecreatedMachine(t *testing.T) {
	t.Parallel()

	manager := managerAt(t)
	if err := os.MkdirAll(filepath.Dir(manager.KnownHostsPath()), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(manager.KnownHostsPath(), []byte(
		"# tool-owned known hosts\n"+
			"isolated-dev-app-abcd1234,192.168.64.5 ssh-ed25519 AAAAstale\n"+
			"isolated-dev-other-99999999 ssh-ed25519 AAAAkept\n",
	), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := manager.ForgetHostKey("isolated-dev-app-abcd1234"); err != nil {
		t.Fatalf("ForgetHostKey() error = %v", err)
	}

	knownHosts := readFile(t, manager.KnownHostsPath())
	if strings.Contains(knownHosts, "AAAAstale") {
		t.Errorf("recreated machine kept its old host key:\n%s", knownHosts)
	}
	if !strings.Contains(knownHosts, "AAAAkept") || !strings.Contains(knownHosts, "# tool-owned") {
		t.Errorf("unrelated known-hosts content was lost:\n%s", knownHosts)
	}
}

func TestForgetHostKeyWithoutAKnownHostsFileIsSafe(t *testing.T) {
	t.Parallel()

	manager := managerAt(t)

	if err := manager.ForgetHostKey("isolated-dev-app-abcd1234"); err != nil {
		t.Fatalf("ForgetHostKey() error = %v", err)
	}
	if _, err := os.Stat(manager.KnownHostsPath()); err == nil {
		t.Errorf("cleanup created %q", manager.KnownHostsPath())
	}
}

func TestForgetHostKeyRejectsAnUnusableAlias(t *testing.T) {
	t.Parallel()

	manager := managerAt(t)

	if err := manager.ForgetHostKey(""); err == nil {
		t.Fatal("ForgetHostKey() error = nil, want rejection")
	}
}

func TestManagerRequiresAnSSHDirectory(t *testing.T) {
	t.Parallel()

	manager := Manager{}

	err := manager.Apply(Entry{Alias: "isolated-dev-app-abcd1234", HostName: "10.0.0.1", User: "fx"})
	if err == nil || !strings.Contains(err.Error(), "SSH directory") {
		t.Fatalf("Apply() error = %v, want a missing SSH directory rejection", err)
	}
}
