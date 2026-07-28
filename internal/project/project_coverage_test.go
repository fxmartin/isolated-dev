package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveRejectsInvalidPaths(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "empty", path: " ", want: "must not be empty"},
		{name: "missing", path: filepath.Join(t.TempDir(), "missing"), want: "resolve project path"},
		{name: "file", path: file, want: "not a directory"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := Resolve(test.path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Resolve() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestSlugifyHandlesEmptyAndLongNames(t *testing.T) {
	t.Parallel()

	if got := slugify("---"); got != "project" {
		t.Fatalf("slugify(---) = %q, want project", got)
	}
	long := strings.Repeat("a", 45) + "-"
	if got := slugify(long); len(got) != 40 || strings.HasSuffix(got, "-") {
		t.Fatalf("slugify(long) = %q, want trimmed 40-character slug", got)
	}
}
