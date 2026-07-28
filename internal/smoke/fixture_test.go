package smoke

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFixturePinsBothImagesOnAPrivateNetwork(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "baseline")
	fixture, err := WriteFixture(dir, "isolated-dev-baseline-abcd", "marker-abcd", 18080)
	if err != nil {
		t.Fatalf("WriteFixture() error = %v", err)
	}

	marker, err := os.ReadFile(filepath.Join(dir, MarkerFileName))
	if err != nil {
		t.Fatalf("ReadFile(marker) error = %v", err)
	}
	if strings.TrimSpace(string(marker)) != "marker-abcd" {
		t.Errorf("marker = %q, want the supplied marker", marker)
	}

	composeBytes, err := os.ReadFile(filepath.Join(dir, ComposeFileName))
	if err != nil {
		t.Fatalf("ReadFile(compose) error = %v", err)
	}
	compose := string(composeBytes)
	for _, want := range []string{
		"name: isolated-dev-baseline-abcd",
		"image: " + OriginImage,
		"image: " + ProxyImage,
		"./" + MarkerFileName + ":/srv/" + MarkerFileName + ":ro",
		`"0.0.0.0:18080:80"`,
		"driver: bridge",
	} {
		if !strings.Contains(compose, want) {
			t.Errorf("compose file does not contain %q:\n%s", want, compose)
		}
	}
	// Only the proxy may be published: the baseline proves service-name
	// reachability over the private network, not a second published port.
	if strings.Count(compose, "ports:") != 1 {
		t.Errorf("compose file publishes more than the proxy port:\n%s", compose)
	}

	nginxBytes, err := os.ReadFile(filepath.Join(dir, NginxFileName))
	if err != nil {
		t.Fatalf("ReadFile(nginx) error = %v", err)
	}
	nginx := string(nginxBytes)
	if !strings.Contains(nginx, "http://"+OriginService+":8080") {
		t.Errorf("nginx config does not proxy to the origin service by name:\n%s", nginx)
	}
	if !strings.Contains(nginx, "resolver 127.0.0.11") {
		t.Errorf("nginx config does not defer upstream resolution:\n%s", nginx)
	}

	if fixture.NetworkName() != "isolated-dev-baseline-abcd_"+NetworkName {
		t.Errorf("NetworkName() = %q", fixture.NetworkName())
	}
	if fixture.ContainerName(OriginService) != "isolated-dev-baseline-abcd-origin-1" {
		t.Errorf("ContainerName(origin) = %q", fixture.ContainerName(OriginService))
	}
}

func TestWriteFixtureRejectsAnUnusableProjectName(t *testing.T) {
	t.Parallel()

	if _, err := WriteFixture(t.TempDir(), "Baseline Project", "marker", 18080); err == nil {
		t.Fatal("WriteFixture() error = nil, want a rejected Compose project name")
	}
}

func TestFixtureRemoveDeletesOnlyItsOwnDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	unrelated := filepath.Join(root, "unrelated")
	if err := os.Mkdir(unrelated, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	fixture, err := WriteFixture(filepath.Join(root, "baseline"), "baseline", "marker", 18080)
	if err != nil {
		t.Fatalf("WriteFixture() error = %v", err)
	}

	if err := fixture.Remove(); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := os.Stat(fixture.Dir); !os.IsNotExist(err) {
		t.Errorf("Stat(fixture) error = %v, want the directory to be gone", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Errorf("Stat(unrelated) error = %v, want it untouched", err)
	}
}
