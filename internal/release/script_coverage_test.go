package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repositoryRoot resolves the checkout this package lives in, so tests can
// assert where the script writes independently of the caller's directory.
func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	return root
}

// TestReleasePlansEveryArtifactUnderTheDefaultOutputDirectory pins the paths a
// developer following the README will find. `dist` is the directory the
// documentation, the workflow's upload globs, and the checksum instructions all
// name, so the default has to stay exactly that everywhere it appears.
func TestReleasePlansEveryArtifactUnderTheDefaultOutputDirectory(t *testing.T) {
	result := runScript(t, nil, "--dry-run", "--version", "1.2.3")
	if result.exitCode != 0 {
		t.Fatalf("dry run exit code = %d, want 0; stderr: %s", result.exitCode, result.stderr)
	}

	for _, step := range []string{
		"mkdir -p dist",
		"-o dist/isolated-dev",
		"tar -czf dist/isolated-dev-1.2.3-darwin-arm64.tar.gz -C dist isolated-dev",
	} {
		requireContains(t, "default-output plan", result.stdout, step)
	}
}

// TestReleaseThreadsTheOutputDirectoryThroughEveryStep keeps `--output-dir` a
// single decision. A flag that moved the binary but not the archive, or the
// archive but not the checksum, would scatter half a release across two
// directories.
func TestReleaseThreadsTheOutputDirectoryThroughEveryStep(t *testing.T) {
	result := runScript(t, nil, "--dry-run", "--version", "1.2.3", "--output-dir", "build/out")
	if result.exitCode != 0 {
		t.Fatalf("dry run exit code = %d, want 0; stderr: %s", result.exitCode, result.stderr)
	}

	for _, step := range []string{
		"mkdir -p build/out",
		"-o build/out/isolated-dev",
		"tar -czf build/out/isolated-dev-1.2.3-darwin-arm64.tar.gz -C build/out isolated-dev",
	} {
		requireContains(t, "custom-output plan", result.stdout, step)
	}
	if strings.Contains(result.stdout, "mkdir -p dist") {
		t.Fatalf("--output-dir still plans the default directory:\n%s", result.stdout)
	}
}

// TestReleasePlansTheChecksumBesideTheArchive covers the last step of the plan.
// The checksum is what a developer verifies a download against, and the README
// tells them to run it from the directory the archive ships in, so it must be
// written against the bare archive name rather than a path.
func TestReleasePlansTheChecksumBesideTheArchive(t *testing.T) {
	result := runScript(t, nil, "--dry-run", "--version", "1.2.3")
	if result.exitCode != 0 {
		t.Fatalf("dry run exit code = %d, want 0; stderr: %s", result.exitCode, result.stderr)
	}

	const archive = "isolated-dev-1.2.3-darwin-arm64.tar.gz"
	requireContains(t, "checksum plan", result.stdout, "shasum -a 256 "+archive+" > "+archive+".sha256")
}

// TestReleaseVerifiesTheBinaryBeforePackagingIt pins the rest of the plan's
// order. Verification exists to stop an unusable binary from being published;
// an archive rolled before that check would ship whatever the build produced.
func TestReleaseVerifiesTheBinaryBeforePackagingIt(t *testing.T) {
	result := runScript(t, nil, "--dry-run", "--version", "1.2.3")
	if result.exitCode != 0 {
		t.Fatalf("dry run exit code = %d, want 0; stderr: %s", result.exitCode, result.stderr)
	}

	plan := result.stdout
	verify := strings.Index(plan, "verify dist/isolated-dev is a self-contained darwin/arm64 binary reporting version 1.2.3")
	if verify < 0 {
		t.Fatalf("plan does not announce the verification step:\n%s", plan)
	}
	if build := strings.Index(plan, "go build"); build > verify {
		t.Fatalf("verification is planned before the build:\n%s", plan)
	}
	if archive := strings.Index(plan, "tar -czf"); archive < verify {
		t.Fatalf("the archive is planned before verification:\n%s", plan)
	}
}

// TestReleaseFallsBackToTheGitDescription covers the last of the three version
// sources. A local build with no flag and no environment still has to stamp the
// binary with something that identifies its source, which is what makes a
// developer's report of "version X misbehaves" actionable.
func TestReleaseFallsBackToTheGitDescription(t *testing.T) {
	described, err := exec.Command("git", "describe", "--tags", "--always", "--dirty").Output()
	if err != nil {
		t.Skipf("git cannot describe this checkout: %v", err)
	}
	want := strings.TrimPrefix(strings.TrimSpace(string(described)), "v")
	if want == "" {
		t.Skip("git described this checkout as nothing")
	}

	result := runScript(t, []string{"ISOLATED_DEV_VERSION="}, "--dry-run")
	if result.exitCode != 0 {
		t.Fatalf("dry run exit code = %d, want 0; stderr: %s", result.exitCode, result.stderr)
	}
	requireContains(t, "plan", result.stdout, "-X main.version="+want)
}

// TestReleaseRefusesToBuildAnUnidentifiableArtifact covers the case where no
// version source answers at all — a source archive with no Git metadata, for
// instance. Stamping "dev" into a published binary would leave nobody able to
// say what it was built from, so the script stops and names both ways to fix it.
func TestReleaseRefusesToBuildAnUnidentifiableArtifact(t *testing.T) {
	// A copy outside any repository: the script resolves its own directory, so
	// this is what "no Git metadata" looks like from its point of view.
	source, err := os.ReadFile(scriptPath(t))
	if err != nil {
		t.Fatalf("read release.sh: %v", err)
	}
	detached := t.TempDir()
	if err := os.Mkdir(filepath.Join(detached, "scripts"), 0o755); err != nil {
		t.Fatalf("Mkdir(scripts) error = %v", err)
	}
	copied := filepath.Join(detached, "scripts", "release.sh")
	if err := os.WriteFile(copied, source, 0o755); err != nil {
		t.Fatalf("WriteFile(release.sh) error = %v", err)
	}

	command := exec.Command(copied, "--dry-run")
	command.Env = append(os.Environ(), "ISOLATED_DEV_VERSION=")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("release.sh built without any version source:\n%s", output)
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("run %s: %v", copied, err)
	}
	if exitErr.ExitCode() != 2 {
		t.Fatalf("exit code = %d, want 2; output: %s", exitErr.ExitCode(), output)
	}
	for _, want := range []string{"cannot determine a version", "--version", "ISOLATED_DEV_VERSION"} {
		requireContains(t, "output", string(output), want)
	}
}

// TestReleaseRejectsAVersionThatIsNotAPlainToken covers the two shapes that
// survive the character check but would still poison a file name: a version
// that is, or begins with, a relative path segment. The version becomes part of
// the published archive's name, so `..` must fail here rather than write an
// archive one directory up.
func TestReleaseRejectsAVersionThatIsNotAPlainToken(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		version string
	}{
		{name: "parent directory", version: ".."},
		{name: "leading dot", version: ".1.2.3"},
		{name: "current directory", version: "."},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			result := runScript(t, []string{"ISOLATED_DEV_VERSION="}, "--dry-run", "--version", testCase.version)
			if result.exitCode != 2 {
				t.Fatalf("exit code = %d, want 2; stdout: %s stderr: %s",
					result.exitCode, result.stdout, result.stderr)
			}
			requireContains(t, "stderr", result.stderr, "must not start with a dot")
		})
	}
}

// TestReleaseTreatsAnEmptyVersionFlagAsUnset keeps an unexpanded shell variable
// — `--version "$TAG"` with no tag — from stamping an empty version rather than
// falling through to the sources that can still answer.
func TestReleaseTreatsAnEmptyVersionFlagAsUnset(t *testing.T) {
	result := runScript(t, []string{"ISOLATED_DEV_VERSION=3.1.4"}, "--dry-run", "--version", "")
	if result.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", result.exitCode, result.stderr)
	}
	requireContains(t, "plan", result.stdout, "-X main.version=3.1.4")
	requireContains(t, "plan", result.stdout, "isolated-dev-3.1.4-darwin-arm64.tar.gz")
}

// TestReleaseShortHelpFlagIsNotAnError keeps `-h` equivalent to `--help`, since
// it is the form a developer reaches for first.
func TestReleaseShortHelpFlagIsNotAnError(t *testing.T) {
	result := runScript(t, nil, "-h")
	if result.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", result.exitCode, result.stderr)
	}
	requireContains(t, "stdout", result.stdout, "usage: release.sh")
}

// TestReleaseBuildsTheSameArtifactFromAnyDirectory is the packaging promise
// stated the other way round: CI checks out and runs `scripts/release.sh`, a
// developer runs it from wherever they happen to be, and both get the same
// artifact. The script resolves the repository from its own location, so a
// relative output directory lands under the repository root rather than under
// whatever directory the caller was in.
func TestReleaseBuildsTheSameArtifactFromAnyDirectory(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and packages the release artifact; skipped in short mode")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("artifact verification needs the macOS file tool")
	}

	const version = "0.0.0-cwdtest"
	// `dist` is the ignored release output directory, so a run that fails
	// before cleanup cannot leave anything the repository would track.
	relativeOutput := filepath.Join("dist", "release-cwd-test")
	absoluteOutput := filepath.Join(repositoryRoot(t), relativeOutput)
	t.Cleanup(func() {
		if err := os.RemoveAll(absoluteOutput); err != nil {
			t.Errorf("remove %s: %v", absoluteOutput, err)
		}
	})

	elsewhere := t.TempDir()
	command := exec.Command(scriptPath(t), "--skip-checks", "--version", version, "--output-dir", relativeOutput)
	command.Dir = elsewhere
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("release from %s: %v\n%s", elsewhere, err, output)
	}

	archive := "isolated-dev-" + version + "-darwin-arm64.tar.gz"
	for _, name := range []string{"isolated-dev", archive, archive + ".sha256"} {
		if _, err := os.Stat(filepath.Join(absoluteOutput, name)); err != nil {
			t.Fatalf("stat %s under the repository root: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "dist")); !os.IsNotExist(err) {
		t.Fatalf("the caller's directory received release output (stat error = %v)", err)
	}

	// The final line is what a caller parses to find what was just built.
	reported := strings.Fields(lastLine(string(output)))
	if len(reported) != 2 {
		t.Fatalf("release reported %q, want the artifact path and version", output)
	}
	if got, want := reported[0], filepath.Join(relativeOutput, archive); got != want {
		t.Errorf("reported artifact = %q, want %q", got, want)
	}
	if got := reported[1]; got != version {
		t.Errorf("reported version = %q, want %q", got, version)
	}
}

func lastLine(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	return lines[len(lines)-1]
}
