package guest

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// publicKeyAlgorithms are the OpenSSH public-key types accepted for guest
// login. Anything else — including any private key — is refused so no private
// material can reach the machine.
var publicKeyAlgorithms = map[string]struct{}{
	"ssh-ed25519":                        {},
	"ssh-rsa":                            {},
	"ecdsa-sha2-nistp256":                {},
	"ecdsa-sha2-nistp384":                {},
	"ecdsa-sha2-nistp521":                {},
	"sk-ssh-ed25519@openssh.com":         {},
	"sk-ecdsa-sha2-nistp256@openssh.com": {},
}

const noPublicKeyGuidance = "no SSH public key found in %s; create one with `ssh-keygen -t ed25519` " +
	"and retry — isolated-dev never copies private keys into the machine"

// PublicKeys collects the authorized login keys for the guest user from the
// host SSH directory. Only `.pub` files are read, and each entry is validated
// as public material before it leaves the host.
func PublicKeys(sshDir string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(sshDir, "*.pub"))
	if err != nil {
		return nil, fmt.Errorf("scan %s for public keys: %w", sshDir, err)
	}
	sort.Strings(matches)

	var keys []string
	seen := make(map[string]struct{}, len(matches))
	for _, path := range matches {
		fileKeys, err := publicKeysFromFile(path)
		if err != nil {
			return nil, err
		}
		for _, key := range fileKeys {
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf(noPublicKeyGuidance, sshDir)
	}
	return keys, nil
}

func publicKeysFromFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read public key %s: %w", filepath.Base(path), err)
	}
	if strings.Contains(string(data), "PRIVATE KEY") {
		return nil, fmt.Errorf(
			"%s contains private key material; isolated-dev never copies private keys into the machine",
			filepath.Base(path),
		)
	}

	var keys []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if err := validatePublicKey(line); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		keys = append(keys, line)
	}
	return keys, nil
}

func validatePublicKey(key string) error {
	fields := strings.Fields(key)
	if len(fields) < 2 {
		return fmt.Errorf("not an OpenSSH public key entry")
	}
	if _, supported := publicKeyAlgorithms[fields[0]]; !supported {
		return fmt.Errorf("unsupported public key algorithm %q", fields[0])
	}
	if _, err := base64.StdEncoding.DecodeString(fields[1]); err != nil {
		return fmt.Errorf("public key body is not valid base64")
	}
	return nil
}
