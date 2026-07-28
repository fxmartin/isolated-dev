package guest

import (
	"strings"
	"testing"
)

func TestNewIdentityDerivesALinuxUserName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		given string
		want  string
	}{
		{name: "already valid", given: "fxmartin", want: "fxmartin"},
		{name: "upper case", given: "FXMartin", want: "fxmartin"},
		{name: "dotted directory service name", given: "first.last", want: "first-last"},
		{name: "spaces collapse", given: "  First   Last  ", want: "first-last"},
		{name: "leading digit gains a prefix", given: "42dev", want: "u42dev"},
		{
			name:  "over long name is truncated",
			given: strings.Repeat("a", 40),
			want:  strings.Repeat("a", 32),
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			identity, err := NewIdentity(test.given, 501, 20)
			if err != nil {
				t.Fatalf("NewIdentity() error = %v", err)
			}
			if identity.Username != test.want {
				t.Errorf("Username = %q, want %q", identity.Username, test.want)
			}
			if identity.UID != 501 || identity.GID != 20 {
				t.Errorf("identity = %+v, want host UID 501 and GID 20 preserved", identity)
			}
		})
	}
}

func TestNewIdentityRejectsUnusableHostIdentities(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		given string
		uid   int
		gid   int
		want  string
	}{
		{name: "root name", given: "root", uid: 501, gid: 20, want: "root"},
		{name: "root uid", given: "fxmartin", uid: 0, gid: 20, want: "must not be root"},
		{name: "root gid", given: "fxmartin", uid: 501, gid: 0, want: "must not be root"},
		{name: "negative uid", given: "fxmartin", uid: -1, gid: 20, want: "UID"},
		{name: "negative gid", given: "fxmartin", uid: 501, gid: -1, want: "GID"},
		{name: "reserved uid", given: "fxmartin", uid: 65534, gid: 20, want: "UID"},
		{name: "reserved gid", given: "fxmartin", uid: 501, gid: 65534, want: "GID"},
		{name: "undeducible name", given: "!!!", uid: 501, gid: 20, want: "user name"},
		{name: "empty name", given: "   ", uid: 501, gid: 20, want: "user name"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			identity, err := NewIdentity(test.given, test.uid, test.gid)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewIdentity() = %+v, error = %v, want containing %q", identity, err, test.want)
			}
		})
	}
}

func TestResolveIdentityMatchesTheInvokingHostUser(t *testing.T) {
	t.Parallel()

	identity, err := ResolveIdentity()
	if err != nil {
		t.Fatalf("ResolveIdentity() error = %v", err)
	}
	if identity.UID <= 0 || identity.GID <= 0 {
		t.Fatalf("identity = %+v, want the numeric host UID and GID", identity)
	}
	if identity.Username == "" || identity.Username == "root" {
		t.Fatalf("identity = %+v, want a dedicated non-root guest user", identity)
	}
}
