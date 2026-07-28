// Package sshconfig maintains the SSH configuration that connects Zed and
// ordinary SSH sessions to a project machine.
//
// Everything isolated-dev generates lives in its own directory under the
// developer's ~/.ssh. The developer-owned config file receives at most one
// Include directive and is never otherwise rewritten, reordered, or pruned.
package sshconfig

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// aliasPattern matches the derived project-machine names that become SSH host
// aliases, which keeps a crafted alias from escaping the managed file or the
// tool-owned known-hosts file.
var aliasPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

const managedHeader = "# Managed by isolated-dev. The host blocks below are rewritten on every `up`;\n" +
	"# keep your own entries in ~/.ssh/config, which isolated-dev never rewrites.\n"

const includeHeader = "# Added once by isolated-dev. Its project-machine entries live in the\n" +
	"# included file; the rest of this file is yours.\n"

// Entry describes the managed SSH host for one project machine.
type Entry struct {
	// Alias is the stable host alias, which is the project machine name.
	Alias string
	// HostName is the current machine address.
	HostName string
	// User is the dedicated guest account.
	User string
}

// Manager owns the isolated-dev SSH configuration inside the developer's SSH
// directory.
type Manager struct {
	// SSHDir is the developer-owned ~/.ssh directory.
	SSHDir string
}

func (manager Manager) managedDir() string {
	return filepath.Join(manager.SSHDir, "isolated-dev")
}

// ManagedConfigPath is the tool-owned SSH configuration file.
func (manager Manager) ManagedConfigPath() string {
	return filepath.Join(manager.managedDir(), "config")
}

// KnownHostsPath is the tool-owned known-hosts file. Keeping project-machine
// host keys here means machine recreation and address changes never touch the
// developer's global known-hosts file.
func (manager Manager) KnownHostsPath() string {
	return filepath.Join(manager.managedDir(), "known_hosts")
}

func (manager Manager) userConfigPath() string {
	return filepath.Join(manager.SSHDir, "config")
}

func (manager Manager) lockPath() string {
	return filepath.Join(manager.managedDir(), ".lock")
}

// withLock serializes one whole read-modify-write of the managed files against
// every other isolated-dev process. Without it two `up` runs for different
// projects read the same file, each write back only their own host block, and
// the second replace silently drops the first project's host while both runs
// report success.
//
// The lock is advisory and released when the process exits, so a crashed run
// never wedges the next one. mutate must not call an exported method that locks
// again: the second descriptor would wait on the first.
func (manager Manager) withLock(mutate func() error) error {
	if err := os.MkdirAll(manager.managedDir(), 0o700); err != nil {
		return fmt.Errorf("create managed SSH directory: %w", err)
	}
	file, err := os.OpenFile(manager.lockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open managed SSH lock: %w", err)
	}
	// Closing the descriptor releases the lock, so one defer covers both.
	defer file.Close()

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock managed SSH configuration: %w", err)
	}
	return mutate()
}

// Apply reconciles the managed host block for one project machine and makes
// sure the developer's configuration includes the managed file. It is
// idempotent: rerunning it with a new address updates that machine's block and
// leaves every other entry — managed or developer-owned — untouched.
func (manager Manager) Apply(entry Entry) error {
	if err := manager.validateDir(); err != nil {
		return err
	}
	if err := validateEntry(entry); err != nil {
		return err
	}
	return manager.withLock(func() error {
		// Only the tool-owned directory is tightened: the developer's ~/.ssh keeps
		// whatever mode they gave it.
		if err := os.Chmod(manager.managedDir(), 0o700); err != nil {
			return fmt.Errorf("secure managed SSH directory: %w", err)
		}
		blocks, err := manager.loadBlocks()
		if err != nil {
			return err
		}
		blocks = upsert(blocks, entry.Alias, manager.render(entry))
		if err := writeAtomic(manager.ManagedConfigPath(), renderFile(blocks)); err != nil {
			return fmt.Errorf("write managed SSH configuration: %w", err)
		}
		return manager.ensureInclude()
	})
}

// Remove drops one project machine from the managed configuration and forgets
// its host keys. Repeating it is safe, so destroy can always run it.
func (manager Manager) Remove(alias string) error {
	if err := manager.validateDir(); err != nil {
		return err
	}
	if err := validateAlias(alias); err != nil {
		return err
	}
	return manager.withLock(func() error {
		blocks, err := manager.loadBlocks()
		if err != nil {
			return err
		}
		remaining := make([]block, 0, len(blocks))
		for _, existing := range blocks {
			if existing.alias != alias {
				remaining = append(remaining, existing)
			}
		}
		// A machine that was never configured leaves no file behind; cleanup must
		// not create one.
		if len(remaining) != len(blocks) {
			if err := writeAtomic(manager.ManagedConfigPath(), renderFile(remaining)); err != nil {
				return fmt.Errorf("write managed SSH configuration: %w", err)
			}
		}
		return manager.forgetHostKey(alias)
	})
}

// ForgetHostKey drops the host keys recorded for one project machine. A
// recreated machine presents a new key under the same alias, so the stale entry
// has to go before the next connection is attempted.
func (manager Manager) ForgetHostKey(alias string) error {
	if err := manager.validateDir(); err != nil {
		return err
	}
	if err := validateAlias(alias); err != nil {
		return err
	}
	return manager.withLock(func() error { return manager.forgetHostKey(alias) })
}

// forgetHostKey does the work of ForgetHostKey with the managed lock already
// held, so Remove can prune the host block and its keys under one lock.
func (manager Manager) forgetHostKey(alias string) error {
	data, err := os.ReadFile(manager.KnownHostsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read managed known hosts: %w", err)
	}

	kept := make([]string, 0)
	dropped := false
	for _, line := range strings.Split(string(data), "\n") {
		if matchesAlias(line, alias) {
			dropped = true
			continue
		}
		kept = append(kept, line)
	}
	if !dropped {
		return nil
	}
	if err := writeAtomic(manager.KnownHostsPath(), strings.Join(kept, "\n")); err != nil {
		return fmt.Errorf("write managed known hosts: %w", err)
	}
	return nil
}

// matchesAlias reports whether a known-hosts line records a key for alias. The
// managed file disables host-key hashing, so its patterns stay comparable.
func matchesAlias(line string, alias string) bool {
	fields := strings.Fields(line)
	if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
		return false
	}
	for _, pattern := range strings.Split(fields[0], ",") {
		if pattern == alias {
			return true
		}
	}
	return false
}

type block struct {
	alias string
	text  string
}

// loadBlocks reads the managed file back into one block per host alias. The
// file is tool-owned, so only its host blocks are preserved: the header is
// regenerated on every write.
func (manager Manager) loadBlocks() ([]block, error) {
	data, err := os.ReadFile(manager.ManagedConfigPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read managed SSH configuration: %w", err)
	}

	var blocks []block
	var current *block
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.EqualFold(fields[0], "Host") {
			blocks = append(blocks, block{alias: fields[1]})
			current = &blocks[len(blocks)-1]
		}
		if current == nil || strings.TrimSpace(line) == "" {
			continue
		}
		current.text += line + "\n"
	}
	return blocks, nil
}

func upsert(blocks []block, alias string, text string) []block {
	for index, existing := range blocks {
		if existing.alias == alias {
			blocks[index].text = text
			return blocks
		}
	}
	return append(blocks, block{alias: alias, text: text})
}

func renderFile(blocks []block) string {
	content := managedHeader
	for _, current := range blocks {
		content += "\n" + current.text
	}
	return content
}

// render writes the options Zed and ordinary SSH sessions need: the current
// address, the guest account, agent forwarding for Git, and a tool-owned
// known-hosts file keyed by the stable alias rather than by the address.
func (manager Manager) render(entry Entry) string {
	options := [][2]string{
		{"HostName", entry.HostName},
		{"User", entry.User},
		{"ForwardAgent", "yes"},
		{"HostKeyAlias", entry.Alias},
		{"UserKnownHostsFile", quote(manager.KnownHostsPath())},
		// accept-new records the key of a machine seen for the first time
		// without prompting, and still refuses a changed key.
		{"StrictHostKeyChecking", "accept-new"},
		// Unhashed entries keep the tool-owned file prunable when a machine is
		// recreated.
		{"HashKnownHosts", "no"},
	}
	text := "Host " + entry.Alias + "\n"
	for _, option := range options {
		text += "    " + option[0] + " " + option[1] + "\n"
	}
	return text
}

// ensureInclude adds at most one Include directive to the developer's config,
// ahead of their own entries so the managed hosts resolve, and leaves the rest
// of the file byte for byte as it was.
func (manager Manager) ensureInclude() error {
	data, err := os.ReadFile(manager.userConfigPath())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read SSH configuration: %w", err)
	}
	directive := "Include " + quote(manager.ManagedConfigPath())
	if includesManaged(string(data), manager.ManagedConfigPath()) {
		return nil
	}
	content := includeHeader + directive + "\n"
	if len(data) > 0 {
		content += "\n" + string(data)
	}
	if err := writeAtomic(manager.userConfigPath(), content); err != nil {
		return fmt.Errorf("write SSH configuration: %w", err)
	}
	return nil
}

func includesManaged(content string, managedPath string) bool {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.EqualFold(fields[0], "Include") {
			continue
		}
		for _, argument := range fields[1:] {
			if strings.Trim(argument, `"`) == managedPath {
				return true
			}
		}
	}
	return false
}

func quote(path string) string {
	return `"` + path + `"`
}

func (manager Manager) validateDir() error {
	if !filepath.IsAbs(manager.SSHDir) || strings.ContainsAny(manager.SSHDir, "\"\n") {
		return fmt.Errorf("invalid SSH directory %q", manager.SSHDir)
	}
	return nil
}

func validateEntry(entry Entry) error {
	if err := validateAlias(entry.Alias); err != nil {
		return err
	}
	if err := validateOption("host name", entry.HostName); err != nil {
		return err
	}
	return validateOption("guest user", entry.User)
}

func validateAlias(alias string) error {
	if !aliasPattern.MatchString(alias) {
		return fmt.Errorf("invalid SSH host alias %q", alias)
	}
	return nil
}

// validateOption keeps a value that would break out of its configuration line —
// or smuggle in an extra option such as ProxyCommand — out of the managed file.
func validateOption(name string, value string) error {
	if value == "" || len(strings.Fields(value)) != 1 || strings.ContainsAny(value, "\"#") {
		return fmt.Errorf("invalid SSH %s %q", name, value)
	}
	return nil
}

// resolveLink follows a symlinked destination to the file it points at. A
// rename replaces the link itself, so writing straight to path would turn a
// dotfiles-managed ~/.ssh/config into a regular file and silently orphan the
// source it was linked to.
func resolveLink(path string) (string, error) {
	for depth := 0; depth < 8; depth++ {
		info, err := os.Lstat(path)
		// A destination that cannot be inspected — missing, or below a
		// non-directory — is left alone so the write below reports the real
		// failure in the caller's own words.
		if err != nil || info.Mode()&fs.ModeSymlink == 0 {
			return path, nil
		}
		target, err := os.Readlink(path)
		if err != nil {
			return path, nil
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		path = target
	}
	return "", fmt.Errorf("resolve %q: too many levels of symbolic links", path)
}

func writeAtomic(path string, content string) error {
	path, err := resolveLink(path)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create %q: %w", directory, err)
	}
	temporary, err := os.CreateTemp(directory, ".isolated-dev-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("secure temporary file: %w", err)
	}
	if _, err := temporary.WriteString(content); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace %q: %w", path, err)
	}
	return nil
}
