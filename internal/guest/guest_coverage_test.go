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
