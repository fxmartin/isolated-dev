package host

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDefaultCheckerIsConfigured(t *testing.T) {
	t.Parallel()

	checker := DefaultChecker()
	if checker.LookPath == nil || checker.Run == nil {
		t.Fatal("DefaultChecker() returned nil dependency")
	}
}

func TestCheckReportsPrerequisiteInspectionFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		lookPath func(string) (string, error)
		run      func(context.Context, string, ...string) ([]byte, error)
		want     string
	}{
		{
			name:     "unconfigured",
			lookPath: nil,
			run:      nil,
			want:     "not configured",
		},
		{
			name: "missing ssh",
			lookPath: func(name string) (string, error) {
				if name == "ssh" {
					return "", errors.New("not found")
				}
				return "/usr/bin/" + name, nil
			},
			run:  func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
			want: "OpenSSH",
		},
		{
			name:     "version command",
			lookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
			run: func(context.Context, string, ...string) ([]byte, error) {
				return nil, errors.New("exit 1")
			},
			want: "inspect Apple Container CLI",
		},
		{
			name:     "unparseable version",
			lookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
			run: func(context.Context, string, ...string) ([]byte, error) {
				return []byte("development build"), nil
			},
			want: "could not determine",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := (Checker{LookPath: test.lookPath, Run: test.run}).Check(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Check() error = %v, want containing %q", err, test.want)
			}
		})
	}
}
