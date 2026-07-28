package sshconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// A developer whose dotfiles own ~/.ssh/config reaches it through a symlink.
// Adding the Include must write through that link: replacing it with a regular
// file detaches the config from its source, and every later dotfiles change
// then stops reaching SSH without anything being reported.
func TestApplyWritesThroughASymlinkedDeveloperConfig(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sshDir := filepath.Join(root, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	dotfiles := filepath.Join(root, "dotfiles")
	if err := os.MkdirAll(dotfiles, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	source := filepath.Join(dotfiles, "ssh_config")
	own := "Host build-box\n    HostName build.example.com\n"
	if err := os.WriteFile(source, []byte(own), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	manager := Manager{SSHDir: sshDir}
	if err := os.Symlink(source, manager.userConfigPath()); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	if err := manager.Apply(Entry{
		Alias:    "isolated-dev-app-abcd1234",
		HostName: "192.168.64.5",
		User:     "fx",
	}); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	info, err := os.Lstat(manager.userConfigPath())
	if err != nil {
		t.Fatalf("Lstat() error = %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the developer's symlinked SSH config was replaced by a regular file")
	}
	// The Include has to land in the file the link points at, or the dotfiles
	// source and the live config have silently diverged.
	linked := readFile(t, source)
	if !strings.Contains(linked, "Include") {
		t.Errorf("dotfiles source did not receive the Include:\n%s", linked)
	}
	if !strings.Contains(linked, own) {
		t.Errorf("dotfiles source lost the developer's own entries:\n%s", linked)
	}
}

// A relative symlink is the common dotfiles shape, and a link chain is what a
// stow-style layout produces; both have to resolve to the same real file.
func TestApplyFollowsARelativeSymlinkChain(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sshDir := filepath.Join(root, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	source := filepath.Join(sshDir, "config.real")
	if err := os.WriteFile(source, []byte("Host mine\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	manager := Manager{SSHDir: sshDir}
	if err := os.Symlink("config.hop", manager.userConfigPath()); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	if err := os.Symlink("config.real", filepath.Join(sshDir, "config.hop")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	if err := manager.Apply(Entry{
		Alias:    "isolated-dev-app-abcd1234",
		HostName: "192.168.64.5",
		User:     "fx",
	}); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	if !strings.Contains(readFile(t, source), "Include") {
		t.Error("the Include did not reach the end of the symlink chain")
	}
	for _, link := range []string{manager.userConfigPath(), filepath.Join(sshDir, "config.hop")} {
		info, err := os.Lstat(link)
		if err != nil {
			t.Fatalf("Lstat() error = %v", err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%q was replaced by a regular file", link)
		}
	}
}

// A symlink loop can never resolve to a file, so it has to be reported rather
// than followed until the process runs out of descriptors.
func TestWriteAtomicRejectsASymlinkLoop(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	if err := os.Symlink(second, first); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	if err := os.Symlink(first, second); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	err := writeAtomic(first, "Host example\n")
	if err == nil || !strings.Contains(err.Error(), "too many levels of symbolic links") {
		t.Fatalf("writeAtomic() error = %v, want a symlink-loop failure", err)
	}
}

// Two `up` runs for different projects share one managed file. Each reads it,
// upserts only its own host, and writes the whole file back, so without a lock
// around that read-modify-write the second replace drops the first project's
// host while both runs report success.
func TestConcurrentApplyKeepsEveryManagedHost(t *testing.T) {
	t.Parallel()

	manager := managerAt(t)
	const projects = 8

	var group sync.WaitGroup
	errs := make([]error, projects)
	for index := range projects {
		group.Add(1)
		go func() {
			defer group.Done()
			errs[index] = manager.Apply(Entry{
				Alias:    fmt.Sprintf("isolated-dev-app%d-abcd1234", index),
				HostName: fmt.Sprintf("192.168.64.%d", index+2),
				User:     "fx",
			})
		}()
	}
	group.Wait()

	for index, err := range errs {
		if err != nil {
			t.Fatalf("Apply(project %d) error = %v", index, err)
		}
	}
	managed := readFile(t, manager.ManagedConfigPath())
	for index := range projects {
		alias := fmt.Sprintf("isolated-dev-app%d-abcd1234", index)
		if !strings.Contains(managed, "Host "+alias+"\n") {
			t.Errorf("concurrent Apply lost the host block for %q:\n%s", alias, managed)
		}
	}
	// The developer's config must still gain exactly one Include, however many
	// runs raced to add it.
	if got := strings.Count(readFile(t, manager.userConfigPath()), "Include "); got != 1 {
		t.Errorf("Include count = %d, want exactly 1", got)
	}
}

// Destroy prunes a host block from the same shared file, so it has to take the
// same lock as the `up` runs it can overlap with.
func TestConcurrentApplyAndRemoveKeepUnrelatedHosts(t *testing.T) {
	t.Parallel()

	manager := managerAt(t)
	doomed := Entry{Alias: "isolated-dev-old-99999999", HostName: "192.168.64.9", User: "fx"}
	if err := manager.Apply(doomed); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	var group sync.WaitGroup
	var applyErr, removeErr error
	group.Add(2)
	go func() {
		defer group.Done()
		applyErr = manager.Apply(Entry{
			Alias:    "isolated-dev-new-abcd1234",
			HostName: "192.168.64.5",
			User:     "fx",
		})
	}()
	go func() {
		defer group.Done()
		removeErr = manager.Remove(doomed.Alias)
	}()
	group.Wait()

	if applyErr != nil || removeErr != nil {
		t.Fatalf("Apply() error = %v, Remove() error = %v", applyErr, removeErr)
	}
	managed := readFile(t, manager.ManagedConfigPath())
	if !strings.Contains(managed, "Host isolated-dev-new-abcd1234\n") {
		t.Errorf("the new project's host block was lost:\n%s", managed)
	}
}
