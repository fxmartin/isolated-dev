package sshconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var coverageEntry = Entry{
	Alias:    "isolated-dev-app-abcd1234",
	HostName: "192.168.64.5",
	User:     "fx",
}

func TestApplyReportsHostSideFailures(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		arrange func(*testing.T, Manager)
		want    string
	}{
		"managed directory cannot be created": {
			arrange: func(t *testing.T, manager Manager) {
				t.Helper()
				if err := os.WriteFile(manager.SSHDir, []byte("not a directory\n"), 0o600); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
			},
			want: "create managed SSH directory",
		},
		"managed configuration cannot be read": {
			arrange: func(t *testing.T, manager Manager) {
				t.Helper()
				if err := os.MkdirAll(manager.ManagedConfigPath(), 0o700); err != nil {
					t.Fatalf("MkdirAll() error = %v", err)
				}
			},
			want: "read managed SSH configuration",
		},
		"developer configuration cannot be read": {
			arrange: func(t *testing.T, manager Manager) {
				t.Helper()
				if err := os.MkdirAll(manager.userConfigPath(), 0o700); err != nil {
					t.Fatalf("MkdirAll() error = %v", err)
				}
			},
			want: "read SSH configuration",
		},
		"developer configuration cannot be written": {
			arrange: func(t *testing.T, manager Manager) {
				t.Helper()
				if err := os.MkdirAll(manager.managedDir(), 0o700); err != nil {
					t.Fatalf("MkdirAll() error = %v", err)
				}
				if err := os.Chmod(manager.SSHDir, 0o500); err != nil {
					t.Fatalf("Chmod() error = %v", err)
				}
				t.Cleanup(func() { _ = os.Chmod(manager.SSHDir, 0o700) })
			},
			want: "write SSH configuration",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			manager := managerAt(t)
			testCase.arrange(t, manager)

			err := manager.Apply(coverageEntry)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("Apply() error = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestRemoveReportsHostSideFailures(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		arrange func(*testing.T, Manager)
		want    string
	}{
		"managed configuration cannot be read": {
			arrange: func(t *testing.T, manager Manager) {
				t.Helper()
				if err := os.MkdirAll(manager.ManagedConfigPath(), 0o700); err != nil {
					t.Fatalf("MkdirAll() error = %v", err)
				}
			},
			want: "read managed SSH configuration",
		},
		"managed configuration cannot be rewritten": {
			arrange: func(t *testing.T, manager Manager) {
				t.Helper()
				if err := manager.Apply(coverageEntry); err != nil {
					t.Fatalf("Apply() error = %v", err)
				}
				if err := os.Chmod(manager.managedDir(), 0o500); err != nil {
					t.Fatalf("Chmod() error = %v", err)
				}
				t.Cleanup(func() { _ = os.Chmod(manager.managedDir(), 0o700) })
			},
			want: "write managed SSH configuration",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			manager := managerAt(t)
			testCase.arrange(t, manager)

			err := manager.Remove(coverageEntry.Alias)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("Remove() error = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestForgetHostKeyReportsHostSideFailures(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		arrange func(*testing.T, Manager)
		want    string
	}{
		"known hosts cannot be read": {
			arrange: func(t *testing.T, manager Manager) {
				t.Helper()
				if err := os.MkdirAll(manager.KnownHostsPath(), 0o700); err != nil {
					t.Fatalf("MkdirAll() error = %v", err)
				}
			},
			want: "read managed known hosts",
		},
		"known hosts cannot be rewritten": {
			arrange: func(t *testing.T, manager Manager) {
				t.Helper()
				if err := os.MkdirAll(manager.managedDir(), 0o700); err != nil {
					t.Fatalf("MkdirAll() error = %v", err)
				}
				if err := os.WriteFile(
					manager.KnownHostsPath(),
					[]byte(coverageEntry.Alias+" ssh-ed25519 AAAAstale\n"),
					0o600,
				); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
				if err := os.Chmod(manager.managedDir(), 0o500); err != nil {
					t.Fatalf("Chmod() error = %v", err)
				}
				t.Cleanup(func() { _ = os.Chmod(manager.managedDir(), 0o700) })
			},
			want: "write managed known hosts",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			manager := managerAt(t)
			testCase.arrange(t, manager)

			err := manager.ForgetHostKey(coverageEntry.Alias)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("ForgetHostKey() error = %v, want %q", err, testCase.want)
			}
		})
	}
}

// Cleanup runs against whatever SSH directory the caller resolved. A directory
// that could never hold the managed file has to be rejected before Remove and
// ForgetHostKey start rewriting paths derived from it.
func TestCleanupRejectsAnUnusableSSHDirectory(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"relative":         ".ssh",
		"embedded quote":   `/home/fx/"ssh`,
		"embedded newline": "/home/fx/.ssh\nHost evil",
	}
	for name, sshDir := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			manager := Manager{SSHDir: sshDir}

			err := manager.Remove(coverageEntry.Alias)
			if err == nil || !strings.Contains(err.Error(), "invalid SSH directory") {
				t.Errorf("Remove() error = %v, want an invalid-directory failure", err)
			}
			err = manager.ForgetHostKey(coverageEntry.Alias)
			if err == nil || !strings.Contains(err.Error(), "invalid SSH directory") {
				t.Errorf("ForgetHostKey() error = %v, want an invalid-directory failure", err)
			}
		})
	}
}

// Every managed file is written through the same atomic replace, so its two
// host-side failure modes are covered once here rather than per caller.
func TestWriteAtomicReportsHostSideFailures(t *testing.T) {
	t.Parallel()

	t.Run("directory cannot be created", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		blocker := filepath.Join(root, "blocker")
		if err := os.WriteFile(blocker, []byte("not a directory\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}

		err := writeAtomic(filepath.Join(blocker, "config"), "Host example\n")
		if err == nil || !strings.Contains(err.Error(), "create") {
			t.Fatalf("writeAtomic() error = %v, want a directory-creation failure", err)
		}
	})

	t.Run("destination cannot be replaced", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		// A non-empty directory at the destination cannot be replaced by a rename.
		destination := filepath.Join(root, "config")
		if err := os.Mkdir(destination, 0o700); err != nil {
			t.Fatalf("Mkdir() error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(destination, "blocker"), []byte("x"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}

		err := writeAtomic(destination, "Host example\n")
		if err == nil || !strings.Contains(err.Error(), "replace") {
			t.Fatalf("writeAtomic() error = %v, want a replacement failure", err)
		}

		// The temporary file must not survive a failed replace, or the next
		// reconciliation would find a stray file in the managed directory.
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatalf("ReadDir() error = %v", err)
		}
		if len(entries) != 1 {
			t.Fatalf("directory entries = %d, want only the untouched destination", len(entries))
		}
	})
}

func TestManagedFileKeepsUnrelatedHostBlocksVerbatim(t *testing.T) {
	t.Parallel()

	manager := managerAt(t)
	if err := os.MkdirAll(manager.managedDir(), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	// A hand-edited managed file is normalized rather than duplicated: only its
	// host blocks survive the next reconciliation.
	if err := os.WriteFile(manager.ManagedConfigPath(), []byte(
		"# stale header\nHost isolated-dev-legacy-00000000\n    HostName 10.0.0.2\n\n",
	), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := manager.Apply(coverageEntry); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	managed := readFile(t, manager.ManagedConfigPath())
	if !strings.Contains(managed, "Host isolated-dev-legacy-00000000\n    HostName 10.0.0.2\n") {
		t.Errorf("unrelated managed block was lost:\n%s", managed)
	}
	if strings.Contains(managed, "# stale header") {
		t.Errorf("header was not regenerated:\n%s", managed)
	}
}

func TestIncludeIsDetectedWhateverItsSpelling(t *testing.T) {
	t.Parallel()

	manager := managerAt(t)
	if err := os.MkdirAll(manager.SSHDir, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	existing := "# comment\n\ninclude " + manager.ManagedConfigPath() + "\nHost example\n"
	if err := os.WriteFile(manager.userConfigPath(), []byte(existing), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := manager.Apply(coverageEntry); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	if content := readFile(t, manager.userConfigPath()); content != existing {
		t.Errorf("configuration = %q, want it left untouched", content)
	}
}

func TestKnownHostsCommentsAndBlankLinesAreKept(t *testing.T) {
	t.Parallel()

	manager := managerAt(t)
	if err := os.MkdirAll(manager.managedDir(), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(manager.KnownHostsPath(), []byte(
		"\n#"+coverageEntry.Alias+" commented out\n"+
			coverageEntry.Alias+" ssh-ed25519 AAAAstale\n",
	), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := manager.ForgetHostKey(coverageEntry.Alias); err != nil {
		t.Fatalf("ForgetHostKey() error = %v", err)
	}

	knownHosts := readFile(t, manager.KnownHostsPath())
	if !strings.Contains(knownHosts, "commented out") {
		t.Errorf("comment was dropped:\n%s", knownHosts)
	}
	if strings.Contains(knownHosts, "AAAAstale") {
		t.Errorf("stale key was kept:\n%s", knownHosts)
	}
}

func TestManagedPathsLiveUnderTheSSHDirectory(t *testing.T) {
	t.Parallel()

	manager := Manager{SSHDir: filepath.Join("/home/fx", ".ssh")}

	if manager.ManagedConfigPath() != "/home/fx/.ssh/isolated-dev/config" {
		t.Errorf("ManagedConfigPath() = %q", manager.ManagedConfigPath())
	}
	if manager.KnownHostsPath() != "/home/fx/.ssh/isolated-dev/known_hosts" {
		t.Errorf("KnownHostsPath() = %q", manager.KnownHostsPath())
	}
}
