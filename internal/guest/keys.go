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
//
// An entry this tool cannot use is skipped rather than failing the whole scan:
// `~/.ssh` legitimately holds `.pub` files that are not login keys — an SSH-CA
// certificate must sit beside the key it certifies — and one of those must not
// cost the user every valid key next to it. Private material remains a hard
// failure, and a scan that yields nothing usable still errors.
func PublicKeys(sshDir string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(sshDir, "*.pub"))
	if err != nil {
		return nil, fmt.Errorf("scan %s for public keys: %w", sshDir, err)
	}
	sort.Strings(matches)

	var keys []string
	var skipped []string
	seen := make(map[string]struct{}, len(matches))
	for _, path := range matches {
		fileKeys, unusable, err := publicKeysFromFile(path)
		if err != nil {
			return nil, err
		}
		if unusable {
			skipped = append(skipped, filepath.Base(path))
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
		if len(skipped) > 0 {
			return nil, fmt.Errorf(
				noPublicKeyGuidance+"; ignored unusable entries in %s",
				sshDir,
				strings.Join(skipped, ", "),
			)
		}
		return nil, fmt.Errorf(noPublicKeyGuidance, sshDir)
	}
	return keys, nil
}

// publicKeysFromFile returns the usable entries of one `.pub` file and reports
// whether any entry was skipped, so the caller can name the file if nothing
// usable survives the whole scan. Only the file name is ever reported: a
// rejected entry's own bytes never reach an error message.
func publicKeysFromFile(path string) ([]string, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("read public key %s: %w", filepath.Base(path), err)
	}
	if strings.Contains(string(data), "PRIVATE KEY") {
		return nil, false, fmt.Errorf(
			"%s contains private key material; isolated-dev never copies private keys into the machine",
			filepath.Base(path),
		)
	}

	var keys []string
	unusable := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if err := validatePublicKey(line); err != nil {
			unusable = true
			continue
		}
		keys = append(keys, line)
	}
	return keys, unusable, nil
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
