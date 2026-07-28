package upgrade

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func testPlan() Plan {
	return Plan{
		ProjectPath:      "/Users/fx/dev/app",
		MachineName:      "isolated-dev-app-abcd1234",
		CurrentBaseImage: "local/isolated-dev-base:1",
		CurrentVersion:   "1",
		TargetBaseImage:  "local/isolated-dev-base:2",
		TargetVersion:    "2",
	}
}

func TestWriteShowsCurrentAndTargetVersions(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	if err := Write(&output, testPlan()); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	got := output.String()
	for _, expected := range []string{
		"Upgrade: /Users/fx/dev/app",
		"Machine: isolated-dev-app-abcd1234",
		"Current base image: local/isolated-dev-base:1 (version 1)",
		"Target base image: local/isolated-dev-base:2 (version 2)",
	} {
		if !strings.Contains(got, expected) {
			t.Errorf("upgrade preview missing %q:\n%s", expected, got)
		}
	}
}

// The preview is the only warning a developer gets before persistent guest
// state is discarded, so every category the story names must be listed.
func TestWriteListsEveryDiscardedCategory(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	if err := Write(&output, testPlan()); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	got := output.String()
	for _, expected := range []string{"packages", "images", "volumes", "data"} {
		if !strings.Contains(got, expected) {
			t.Errorf("upgrade preview missing the %q category:\n%s", expected, got)
		}
	}
	for _, category := range DiscardedCategories() {
		if !strings.Contains(got, "  - "+category) {
			t.Errorf("upgrade preview missing category %q:\n%s", category, got)
		}
	}
}

// The mounted macOS source survives a recreation; saying so keeps the preview
// from reading as though the repository itself were at risk.
func TestWriteReportsThePreservedProjectSource(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	if err := Write(&output, testPlan()); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if !strings.Contains(output.String(), "Preserved: the macOS project source at /Users/fx/dev/app") {
		t.Errorf("upgrade preview missing the preserved source line:\n%s", output.String())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("broken pipe")
}

func TestWriteReportsOutputFailures(t *testing.T) {
	t.Parallel()

	err := Write(failingWriter{}, testPlan())
	if err == nil || !strings.Contains(err.Error(), "write upgrade preview") {
		t.Fatalf("Write() error = %v, want an upgrade preview write failure", err)
	}
}
