// Package guest configures the dedicated non-root Linux account, its
// credentials, and the mounted project inside a project machine.
package guest

import (
	"fmt"
	"os/user"
	"strconv"
	"strings"
)

// maxUserNameLength is the Linux `useradd` limit for a login name.
const maxUserNameLength = 32

// reservedID is the `nobody`/`nogroup` boundary: anything at or above it is an
// overflow identity rather than a real host user.
const reservedID = 65534

// Identity is the guest account that owns every routine editor, shell, Git,
// build, and test operation. Its numeric IDs mirror the invoking macOS user so
// the mounted source tree never gains root-owned files.
type Identity struct {
	Username string
	UID      int
	GID      int
}

// ResolveIdentity derives the guest identity from the invoking macOS user.
func ResolveIdentity() (Identity, error) {
	current, err := user.Current()
	if err != nil {
		return Identity{}, fmt.Errorf("resolve the invoking macOS user: %w", err)
	}
	uid, err := strconv.Atoi(current.Uid)
	if err != nil {
		return Identity{}, fmt.Errorf("read the macOS user UID: %w", err)
	}
	gid, err := strconv.Atoi(current.Gid)
	if err != nil {
		return Identity{}, fmt.Errorf("read the macOS user GID: %w", err)
	}
	return NewIdentity(current.Username, uid, gid)
}

// NewIdentity validates the host identity and derives a Linux login name from
// the macOS user name, which may contain characters `useradd` rejects.
func NewIdentity(name string, uid int, gid int) (Identity, error) {
	username, err := linuxUserName(name)
	if err != nil {
		return Identity{}, err
	}
	if username == "root" {
		return Identity{}, fmt.Errorf(
			"macOS user %q maps to the guest root account; isolated-dev provisions a dedicated non-root user",
			name,
		)
	}
	if err := validateID("UID", uid); err != nil {
		return Identity{}, err
	}
	if err := validateID("GID", gid); err != nil {
		return Identity{}, err
	}
	return Identity{Username: username, UID: uid, GID: gid}, nil
}

func validateID(field string, value int) error {
	if value == 0 {
		return fmt.Errorf("guest %s must not be root", field)
	}
	if value < 0 || value >= reservedID {
		return fmt.Errorf("guest %s %d is outside the usable range 1-%d", field, value, reservedID-1)
	}
	return nil
}

func linuxUserName(name string) (string, error) {
	var builder strings.Builder
	previousHyphen := false
	for _, character := range strings.ToLower(strings.TrimSpace(name)) {
		if (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') ||
			character == '_' {
			builder.WriteRune(character)
			previousHyphen = false
			continue
		}
		if builder.Len() > 0 && !previousHyphen {
			builder.WriteByte('-')
			previousHyphen = true
		}
	}
	username := strings.Trim(builder.String(), "-")
	if username == "" {
		return "", fmt.Errorf("cannot derive a Linux user name from macOS user %q", name)
	}
	if first := username[0]; first >= '0' && first <= '9' {
		username = "u" + username
	}
	if len(username) > maxUserNameLength {
		username = strings.TrimRight(username[:maxUserNameLength], "-")
	}
	return username, nil
}
