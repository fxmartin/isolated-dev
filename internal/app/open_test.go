package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type zedCall struct {
	alias     string
	guestPath string
}

type zedStub struct {
	opened []zedCall
	err    error
}

func (stub *zedStub) Open(_ context.Context, alias string, guestPath string) error {
	stub.opened = append(stub.opened, zedCall{alias: alias, guestPath: guestPath})
	return stub.err
}

func TestOpenReconcilesTheMachineAndOpensItInZed(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository, machineName := sshRepository(t, home)
	launcher := &zedStub{}
	sshManager := &sshStub{}
	lifecycle := &lifecycleStub{}
	application := upApp(t, home, repository, lifecycle)
	application.GuestProvisioner = &guestStub{guestPath: "/home/fx/app"}
	application.SSHConfig = sshManager
	application.Zed = launcher

	var summary bytes.Buffer
	if err := application.Open(context.Background(), repository, &summary); err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	// Opening reconciles the machine and its SSH access first, so a stopped or
	// moved machine needs no separate `up`.
	if len(lifecycle.upRequests) != 1 || len(sshManager.applied) != 1 {
		t.Errorf(
			"up requests = %+v, applied entries = %+v, want both reconciled",
			lifecycle.upRequests,
			sshManager.applied,
		)
	}
	want := zedCall{alias: machineName, guestPath: "/home/fx/app"}
	if len(launcher.opened) != 1 || launcher.opened[0] != want {
		t.Fatalf("opened = %+v, want %+v", launcher.opened, want)
	}
	if !strings.Contains(summary.String(), "opening /home/fx/app in Zed over "+machineName) {
		t.Errorf("summary = %q, want the Zed target reported", summary.String())
	}
}

func TestOpenRequiresZedBeforeLifecycleMutation(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository, _ := sshRepository(t, home)
	lifecycle := &lifecycleStub{}
	application := upApp(t, home, repository, lifecycle)
	application.Zed = nil

	err := application.Open(context.Background(), repository, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "Zed integration is not configured") {
		t.Fatalf("Open() error = %v, want a Zed configuration rejection", err)
	}
	if len(lifecycle.upRequests) != 0 {
		t.Fatalf("up requests = %+v, want no lifecycle mutation", lifecycle.upRequests)
	}
}

func TestOpenStopsWhenTheMachineCannotBeReconciled(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository, _ := sshRepository(t, home)
	launcher := &zedStub{}
	application := upApp(t, home, repository, &lifecycleStub{
		upErr: errors.New("machine did not become ready"),
	})
	application.Zed = launcher

	err := application.Open(context.Background(), repository, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "machine did not become ready") {
		t.Fatalf("Open() error = %v, want the lifecycle failure reported", err)
	}
	if len(launcher.opened) != 0 {
		t.Errorf("opened = %+v, want no Zed launch", launcher.opened)
	}
}

func TestOpenReportsAFailedSummary(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository, _ := sshRepository(t, home)
	application := upApp(t, home, repository, &lifecycleStub{})

	// The three `up` lines land before the Zed report.
	err := application.Open(context.Background(), repository, &writeAfter{failAfter: 3})
	if err == nil || !strings.Contains(err.Error(), "write open summary") {
		t.Fatalf("Open() error = %v, want the report failure surfaced", err)
	}
}

func TestOpenReportsAFailedLaunch(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository, _ := sshRepository(t, home)
	application := upApp(t, home, repository, &lifecycleStub{})
	application.Zed = &zedStub{err: errors.New("zed exited with status 1")}

	err := application.Open(context.Background(), repository, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "zed exited with status 1") {
		t.Fatalf("Open() error = %v, want the launch failure reported", err)
	}
}
