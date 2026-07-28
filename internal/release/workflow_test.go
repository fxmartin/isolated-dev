package release

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// workflowPath resolves the release workflow, which is the only automated path
// that publishes an artifact to a user.
func workflowPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(repositoryRoot(t), ".github", "workflows", "release.yml")
}

func readWorkflow(t *testing.T) string {
	t.Helper()
	content, err := os.ReadFile(workflowPath(t))
	if err != nil {
		t.Fatalf("read release workflow: %v", err)
	}
	return string(content)
}

// TestReleaseWorkflowBuildsThroughTheReleaseScript is the property that makes
// "CI runs the same script, so a release is reproducible locally" true rather
// than aspirational. The moment the workflow grows a build command of its own,
// what is published stops being what `scripts/release.sh` produces, and every
// test in this package stops describing the released artifact.
func TestReleaseWorkflowBuildsThroughTheReleaseScript(t *testing.T) {
	workflow := readWorkflow(t)

	requireContains(t, "release workflow", workflow, "scripts/release.sh")
	for _, bespoke := range []string{"go build", "go test", "GOARCH=", "CGO_ENABLED="} {
		if strings.Contains(workflow, bespoke) {
			t.Fatalf("the workflow runs %q itself instead of through scripts/release.sh:\n%s",
				bespoke, workflow)
		}
	}
}

// TestReleaseWorkflowRunsOnAMacRunner pins the host. The script executes the
// binary it just built to confirm the stamped version, and only macOS can run a
// darwin/arm64 executable — on any other runner that check would silently
// degrade to a note and publish an unverified artifact.
func TestReleaseWorkflowRunsOnAMacRunner(t *testing.T) {
	workflow := readWorkflow(t)

	requireContains(t, "release workflow", workflow, "runs-on: macos-")
	requireContains(t, "release workflow", workflow, "go-version-file: go.mod")
}

// TestReleaseWorkflowTakesItsVersionFromTheTagOrTheDispatchInput covers both
// ways a release is started. A tag push must stamp the tag, and a manual run
// must stamp what the operator typed; neither may fall through to the Git
// description, which is why the checkout fetches the full history for the case
// where it does.
func TestReleaseWorkflowTakesItsVersionFromTheTagOrTheDispatchInput(t *testing.T) {
	workflow := readWorkflow(t)

	requireContains(t, "release workflow", workflow, "ISOLATED_DEV_VERSION:")
	requireContains(t, "release workflow", workflow, "inputs.version")
	requireContains(t, "release workflow", workflow, "github.ref_name")
	requireContains(t, "release workflow", workflow, "fetch-depth: 0")

	// Both triggers, so a release can be cut from a tag and re-cut by hand.
	requireContains(t, "release workflow", workflow, "workflow_dispatch:")
	requireContains(t, "release workflow", workflow, `- "v*"`)
}

// TestReleaseWorkflowPublishesTheArchiveWithItsChecksum keeps the two halves of
// a verifiable download together. An archive published without its `.sha256` is
// one a user cannot check, and a silent "no files matched" would publish a
// release with nothing attached at all.
func TestReleaseWorkflowPublishesTheArchiveWithItsChecksum(t *testing.T) {
	workflow := readWorkflow(t)

	for _, published := range []string{
		"dist/*.tar.gz",
		"dist/*.sha256",
		"if-no-files-found: error",
		"dist/isolated-dev-*.tar.gz",
		"dist/isolated-dev-*.tar.gz.sha256",
	} {
		requireContains(t, "release workflow", workflow, published)
	}
}

// TestReleaseWorkflowPublishesAReleaseOnlyForATag keeps a manual dispatch a
// build rather than a publication, and keeps write access scoped to the job
// that needs it. A dispatch that created a GitHub release would publish an
// untagged, unreproducible version to everyone watching the repository.
func TestReleaseWorkflowPublishesAReleaseOnlyForATag(t *testing.T) {
	workflow := readWorkflow(t)

	requireContains(t, "release workflow", workflow, "gh release create")
	requireContains(t, "release workflow", workflow, "startsWith(github.ref, 'refs/tags/v')")

	// The default token is read-only; only the release job is granted more.
	requireContains(t, "release workflow", workflow, "permissions:\n  contents: read")
	requireContains(t, "release workflow", workflow, "contents: write")

	guard := strings.Index(workflow, "startsWith(github.ref, 'refs/tags/v')")
	create := strings.Index(workflow, "gh release create")
	if guard < 0 || create < guard {
		t.Fatalf("`gh release create` is not guarded by the tag condition:\n%s", workflow)
	}
}
