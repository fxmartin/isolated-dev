package app

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fxmartin/isolated-dev/internal/guest"
)

func TestUpReportsHomeDirectoryResolutionFailures(t *testing.T) {
	tests := []struct {
		name    string
		homeDir func(*testing.T) string
		want    string
	}{
		{
			name:    "undefined home",
			homeDir: func(*testing.T) string { return "" },
			want:    "resolve home directory",
		},
		{
			name: "unresolvable home",
			homeDir: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "absent")
			},
			want: "resolve home directory",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			repository := appRepository(t)
			application := upApp(t, filepath.Dir(repository), repository, &lifecycleStub{})
			application.HomeDir = test.homeDir(t)
			if application.HomeDir == "" {
				t.Setenv("HOME", "")
			}

			err := application.Up(context.Background(), repository, io.Discard)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Up() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestUpReportsUnstattableSecretReferences(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository := filepath.Join(home, "app")
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(repository, ".isolated-dev.toml"),
		[]byte("version = 1\n[secrets]\nfiles = [\".env/inner\"]\n"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repository, ".env"), []byte("x\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	lifecycle := &lifecycleStub{}
	application := upApp(t, home, repository, lifecycle)

	err := application.Up(context.Background(), repository, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "check secret file reference") {
		t.Fatalf("Up() error = %v, want an unreadable reference reported", err)
	}
	if len(lifecycle.upRequests) != 0 {
		t.Fatalf("up requests = %+v, want no lifecycle mutation", lifecycle.upRequests)
	}
}

func TestUpReportsSecretWarningWriteFailures(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository := filepath.Join(home, "app")
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(repository, ".isolated-dev.toml"),
		[]byte("version = 1\n[secrets]\nfiles = [\"missing.env\"]\n"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	application := upApp(t, home, repository, &lifecycleStub{})
	application.WarningOutput = &writeAfter{failAfter: 1}

	err := application.Up(context.Background(), repository, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "write secret reference warning") {
		t.Fatalf("Up() error = %v, want the warning failure reported", err)
	}
}

// The shipped binary never injects ResolveIdentity, so the fallback to the
// invoking macOS user is the branch that runs in production.
func TestGuestIdentityFallsBackToTheInvokingUser(t *testing.T) {
	t.Parallel()

	want, wantErr := guest.ResolveIdentity()
	got, err := App{}.guestIdentity()
	if (err == nil) != (wantErr == nil) {
		t.Fatalf("guestIdentity() error = %v, want %v", err, wantErr)
	}
	if err == nil && got != want {
		t.Fatalf("guestIdentity() = %+v, want %+v", got, want)
	}
}
