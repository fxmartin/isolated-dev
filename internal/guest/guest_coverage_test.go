package guest

import (
	"strings"
	"testing"
)

// A malformed directory name reaches filepath.Glob as a malformed pattern,
// which is the only way the host scan itself fails.
func TestPublicKeysReportsAnUnscannableDirectory(t *testing.T) {
	t.Parallel()

	_, err := PublicKeys("/nonexistent/[")
	if err == nil || !strings.Contains(err.Error(), "scan") {
		t.Fatalf("PublicKeys() error = %v, want a scan failure", err)
	}
}

// Containment is only meaningful between two paths of the same kind. A pair
// that cannot be compared at all must read as "outside", because treating it as
// inside is what would let provisioning rewrite the mounted project's ~/.ssh.
func TestWithinDirRejectsIncomparablePaths(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		dir  string
		path string
	}{
		"relative dir, absolute path": {dir: "home/fx", path: "/home/fx/app"},
		"absolute dir, relative path": {dir: "/home/fx", path: "home/fx/app"},
	}

	for name, test := range tests {
		test := test
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			relative, within := withinDir(test.dir, test.path)
			if within || relative != "" {
				t.Fatalf("withinDir(%q, %q) = (%q, %t), want (\"\", false)",
					test.dir, test.path, relative, within)
			}
		})
	}
}
