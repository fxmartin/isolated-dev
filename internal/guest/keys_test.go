package guest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	ed25519Key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK7Q3rP0m3xnKcQ9pR6l3n3v1wq1V7pQvXvJ0uZ0aAaA fx@mac"
	rsaKey     = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQC7 fx@mac"
)

// privateKeyHeader is assembled at run time so the fixture is not itself
// flagged as a committed private key by secret scanners.
var privateKeyHeader = "-----BEGIN OPENSSH " + "PRIVATE KEY-----"

func writeFile(t *testing.T, path string, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func TestPublicKeysCollectsOnlyPublicMaterial(t *testing.T) {
	t.Parallel()

	sshDir := t.TempDir()
	writeFile(t, filepath.Join(sshDir, "id_ed25519.pub"), ed25519Key+"\n")
	writeFile(t, filepath.Join(sshDir, "id_rsa.pub"), "# comment\n\n"+rsaKey+"\n")
	writeFile(t, filepath.Join(sshDir, "id_ed25519"), privateKeyHeader+"\nsecret\n")
	writeFile(t, filepath.Join(sshDir, "config"), "Host *\n")

	keys, err := PublicKeys(sshDir)
	if err != nil {
		t.Fatalf("PublicKeys() error = %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("keys = %#v, want the two public keys", keys)
	}
	for _, key := range keys {
		if strings.Contains(key, "PRIVATE KEY") || strings.Contains(key, "secret") {
			t.Fatalf("PublicKeys() returned private material: %q", key)
		}
	}
	if keys[0] != ed25519Key || keys[1] != rsaKey {
		t.Fatalf("keys = %#v, want deterministic order", keys)
	}
}

func TestPublicKeysDeduplicatesRepeatedKeys(t *testing.T) {
	t.Parallel()

	sshDir := t.TempDir()
	writeFile(t, filepath.Join(sshDir, "a.pub"), ed25519Key+"\n")
	writeFile(t, filepath.Join(sshDir, "b.pub"), ed25519Key+"\n")

	keys, err := PublicKeys(sshDir)
	if err != nil {
		t.Fatalf("PublicKeys() error = %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("keys = %#v, want one deduplicated key", keys)
	}
}

func TestPublicKeysRejectsPrivateKeyStoredWithAPublicSuffix(t *testing.T) {
	t.Parallel()

	sshDir := t.TempDir()
	writeFile(
		t,
		filepath.Join(sshDir, "id_ed25519.pub"),
		privateKeyHeader+"\nb3BlbnNzaC1rZXk=\n",
	)

	keys, err := PublicKeys(sshDir)
	if err == nil || !strings.Contains(err.Error(), "id_ed25519.pub") {
		t.Fatalf("PublicKeys() = %#v, error = %v, want a rejection naming the file", keys, err)
	}
	if strings.Contains(err.Error(), "b3BlbnNzaC1rZXk=") {
		t.Fatalf("PublicKeys() leaked key material: %v", err)
	}
}

func TestPublicKeysRejectsUnusableEntries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
	}{
		{name: "unknown algorithm", content: "ssh-dss AAAAB3NzaC1kc3M= fx@mac\n"},
		{name: "missing body", content: "ssh-ed25519\n"},
		{name: "body is not base64", content: "ssh-ed25519 not-base64!! fx@mac\n"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			sshDir := t.TempDir()
			writeFile(t, filepath.Join(sshDir, "id_ed25519.pub"), test.content)

			keys, err := PublicKeys(sshDir)
			if err == nil {
				t.Fatalf("PublicKeys() = %#v, want an error", keys)
			}
			// Nothing usable survived, so the actionable guidance stands — and
			// it names the file whose entries were skipped.
			if !strings.Contains(err.Error(), "ssh-keygen") {
				t.Errorf("PublicKeys() error = %v, want actionable ssh-keygen guidance", err)
			}
			if !strings.Contains(err.Error(), "id_ed25519.pub") {
				t.Errorf("PublicKeys() error = %v, want the skipped file named", err)
			}
		})
	}
}

// An unusable `.pub` file must not cost the user the valid keys beside it. An
// SSH-CA certificate is the case that bites: OpenSSH requires it to live next
// to the key it certifies, so its owner has no host-side workaround.
func TestPublicKeysSkipsUnusableEntriesBesideValidKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		fileName string
		content  string
	}{
		{
			name:     "ssh certificate authority certificate",
			fileName: "id_ed25519-cert.pub",
			content: "ssh-ed25519-cert-v01@openssh.com " +
				"AAAAIHNzaC1lZDI1NTE5LWNlcnQ= fx@mac\n",
		},
		{name: "unknown algorithm", fileName: "legacy.pub", content: "ssh-dss AAAAB3NzaC1kc3M= fx@mac\n"},
		{name: "not a key entry at all", fileName: "notes.pub", content: "just some text\n"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			sshDir := t.TempDir()
			writeFile(t, filepath.Join(sshDir, "id_ed25519.pub"), ed25519Key+"\n")
			writeFile(t, filepath.Join(sshDir, test.fileName), test.content)

			keys, err := PublicKeys(sshDir)
			if err != nil {
				t.Fatalf("PublicKeys() error = %v, want the valid key beside %s", err, test.fileName)
			}
			if len(keys) != 1 || keys[0] != ed25519Key {
				t.Fatalf("keys = %#v, want only the usable ed25519 key", keys)
			}
		})
	}
}

// A skipped entry within an otherwise usable file must not take the rest of
// that file down with it.
func TestPublicKeysSkipsUnusableEntriesWithinAFile(t *testing.T) {
	t.Parallel()

	sshDir := t.TempDir()
	writeFile(
		t,
		filepath.Join(sshDir, "authorized.pub"),
		"ssh-dss AAAAB3NzaC1kc3M= fx@mac\n"+ed25519Key+"\n",
	)

	keys, err := PublicKeys(sshDir)
	if err != nil {
		t.Fatalf("PublicKeys() error = %v", err)
	}
	if len(keys) != 1 || keys[0] != ed25519Key {
		t.Fatalf("keys = %#v, want only the usable ed25519 key", keys)
	}
}

// Skipping must never soften the private-key guarantee: a private key beside a
// valid public key still fails the whole scan.
func TestPublicKeysStillRejectsPrivateMaterialBesideAValidKey(t *testing.T) {
	t.Parallel()

	sshDir := t.TempDir()
	writeFile(t, filepath.Join(sshDir, "id_ed25519.pub"), ed25519Key+"\n")
	writeFile(t, filepath.Join(sshDir, "leaked.pub"), privateKeyHeader+"\nb3BlbnNzaC1rZXk=\n")

	keys, err := PublicKeys(sshDir)
	if err == nil || !strings.Contains(err.Error(), "never copies private keys") {
		t.Fatalf("PublicKeys() = %#v, error = %v, want the private-key rejection", keys, err)
	}
}

func TestPublicKeysReportsAnActionableErrorWhenNoneExist(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		sshDir func(*testing.T) string
	}{
		{name: "missing directory", sshDir: func(t *testing.T) string {
			return filepath.Join(t.TempDir(), "absent")
		}},
		{name: "no public keys", sshDir: func(t *testing.T) string { return t.TempDir() }},
		{name: "only blank lines", sshDir: func(t *testing.T) string {
			sshDir := t.TempDir()
			writeFile(t, filepath.Join(sshDir, "empty.pub"), "\n\n")
			return sshDir
		}},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := PublicKeys(test.sshDir(t))
			if err == nil || !strings.Contains(err.Error(), "ssh-keygen") {
				t.Fatalf("PublicKeys() error = %v, want actionable ssh-keygen guidance", err)
			}
			if err != nil && !strings.Contains(err.Error(), "never copies private keys") {
				t.Errorf("PublicKeys() error = %v, want the private-key guarantee restated", err)
			}
		})
	}
}

func TestPublicKeysReportsUnreadableFiles(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	sshDir := t.TempDir()
	unreadable := filepath.Join(sshDir, "id_ed25519.pub")
	writeFile(t, unreadable, ed25519Key+"\n")
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o600) })

	if _, err := PublicKeys(sshDir); err == nil {
		t.Fatal("PublicKeys() error = nil, want a read failure")
	}
}
