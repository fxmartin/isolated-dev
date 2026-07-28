// Package release contains the tests for `scripts/release.sh`, the single
// path that turns a checkout into the distributable macOS artifact. The script
// is the implementation; these tests are its specification. They drive it the
// way CI and a local isolated build environment do, so a release that would
// ship a binary needing a Go toolchain — or a binary carrying no version —
// fails here rather than on a clean Mac.
package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// scriptPath resolves `scripts/release.sh` from this package's directory, so
// the tests locate it the same way regardless of the caller's directory.
func scriptPath(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	script := filepath.Join(root, "scripts", "release.sh")
	info, err := os.Stat(script)
	if err != nil {
		t.Fatalf("stat %s: %v", script, err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("%s is not executable (mode %v)", script, info.Mode().Perm())
	}
	return script
}

type scriptResult struct {
	stdout   string
	stderr   string
	exitCode int
}

func runScript(t *testing.T, env []string, args ...string) scriptResult {
	t.Helper()
	command := exec.Command(scriptPath(t), args...)
	command.Env = append(os.Environ(), env...)
	var stdout, stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	result := scriptResult{stdout: stdout.String(), stderr: stderr.String()}
	switch typed := err.(type) {
	case nil:
	case *exec.ExitError:
		result.exitCode = typed.ExitCode()
	default:
		t.Fatalf("run %s: %v", scriptPath(t), err)
	}
	return result
}

func requireContains(t *testing.T, label, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("%s does not contain %q:\n%s", label, needle, haystack)
	}
}

// TestReleasePlanRunsTheGoTestSuiteAndBuildsAVersionedAppleSiliconBinary
// pins the release plan itself: the artifact is only ever produced after the
// Go test suite has run, and it is always a versioned Apple-silicon binary
// built without cgo.
func TestReleasePlanRunsTheGoTestSuiteAndBuildsAVersionedAppleSiliconBinary(t *testing.T) {
	result := runScript(t, nil, "--dry-run", "--version", "1.2.3", "--output-dir", "dist")
	if result.exitCode != 0 {
		t.Fatalf("dry run exit code = %d, want 0; stderr: %s", result.exitCode, result.stderr)
	}

	plan := result.stdout
	for _, step := range []string{
		"gofmt -l .",
		"go vet ./...",
		"go test ./...",
		"CGO_ENABLED=0",
		"GOOS=darwin",
		"GOARCH=arm64",
		"go build",
		"-X main.version=1.2.3",
		"./cmd/isolated-dev",
		"isolated-dev-1.2.3-darwin-arm64.tar.gz",
	} {
		requireContains(t, "release plan", plan, step)
	}

	// The plan is ordered: shipping an artifact built before the suite ran
	// would defeat the point of running it at all.
	if strings.Index(plan, "go test ./...") > strings.Index(plan, "go build") {
		t.Fatalf("go build is planned before go test:\n%s", plan)
	}
}

// TestReleasePlanSkipsOnlyTheChecksWhenAsked keeps `--skip-checks` an
// explicitly narrowed build: it drops the checks and nothing else, so it can
// never quietly become the path a real release is cut from.
func TestReleasePlanSkipsOnlyTheChecksWhenAsked(t *testing.T) {
	result := runScript(t, nil, "--dry-run", "--skip-checks", "--version", "1.2.3")
	if result.exitCode != 0 {
		t.Fatalf("dry run exit code = %d, want 0; stderr: %s", result.exitCode, result.stderr)
	}

	plan := result.stdout
	for _, skipped := range []string{"gofmt -l .", "go vet ./...", "go test ./..."} {
		if strings.Contains(plan, skipped) {
			t.Fatalf("--skip-checks still plans %q:\n%s", skipped, plan)
		}
	}
	for _, kept := range []string{"go build", "isolated-dev-1.2.3-darwin-arm64.tar.gz"} {
		requireContains(t, "skipped-checks plan", plan, kept)
	}
	if !strings.Contains(result.stderr, "skipping") {
		t.Fatalf("--skip-checks is silent; stderr: %s", result.stderr)
	}
}

// TestReleaseVersionResolution covers the three ways a version reaches the
// binary. A release cut from tag `v1.4.0` must report `1.4.0`, not `v1.4.0`.
func TestReleaseVersionResolution(t *testing.T) {
	for _, testCase := range []struct {
		name string
		env  []string
		args []string
		want string
	}{
		{
			name: "flag wins",
			env:  []string{"ISOLATED_DEV_VERSION=9.9.9"},
			args: []string{"--version", "1.4.0"},
			want: "1.4.0",
		},
		{
			name: "tag prefix is stripped",
			args: []string{"--version", "v1.4.0"},
			want: "1.4.0",
		},
		{
			name: "environment is the fallback",
			env:  []string{"ISOLATED_DEV_VERSION=v2.0.0-rc.1"},
			want: "2.0.0-rc.1",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			args := append([]string{"--dry-run"}, testCase.args...)
			result := runScript(t, testCase.env, args...)
			if result.exitCode != 0 {
				t.Fatalf("exit code = %d, want 0; stderr: %s", result.exitCode, result.stderr)
			}
			requireContains(t, "plan", result.stdout, "-X main.version="+testCase.want)
			requireContains(t, "plan", result.stdout, "isolated-dev-"+testCase.want+"-darwin-arm64.tar.gz")
		})
	}
}

// TestReleaseRejectsUnusableInvocations makes every misuse fail before the
// build rather than produce a mislabelled artifact.
func TestReleaseRejectsUnusableInvocations(t *testing.T) {
	for _, testCase := range []struct {
		name string
		args []string
	}{
		{name: "unknown flag", args: []string{"--publish"}},
		{name: "version without a value", args: []string{"--version"}},
		{name: "output directory without a value", args: []string{"--output-dir"}},
		{name: "version that is not a single token", args: []string{"--version", "1.2.3 dirty"}},
		{name: "version that is a path", args: []string{"--version", "../1.2.3"}},
		{name: "positional argument", args: []string{"1.2.3"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			result := runScript(t, []string{"ISOLATED_DEV_VERSION="}, append([]string{"--dry-run"}, testCase.args...)...)
			if result.exitCode != 2 {
				t.Fatalf("exit code = %d, want 2; stdout: %s stderr: %s",
					result.exitCode, result.stdout, result.stderr)
			}
			requireContains(t, "stderr", result.stderr, "usage: release.sh")
		})
	}
}

// TestReleaseHelpIsNotAnError keeps `--help` a way to learn the script rather
// than a failure, which matters because it is the first thing anyone runs.
func TestReleaseHelpIsNotAnError(t *testing.T) {
	result := runScript(t, nil, "--help")
	if result.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", result.exitCode, result.stderr)
	}
	requireContains(t, "stdout", result.stdout, "usage: release.sh")
}

// TestReleaseBuildsASelfContainedAppleSiliconArtifact is the acceptance test
// for the packaged MVP: it runs the real script and inspects what a developer
// would download. The binary must be an Apple-silicon Mach-O executable that
// links nothing but macOS system libraries — that is what makes a clean Mac
// with no Go toolchain, language runtime, or package manager sufficient — and
// it must report the version it was built with.
func TestReleaseBuildsASelfContainedAppleSiliconArtifact(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and packages the release artifact; skipped in short mode")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("artifact inspection needs the macOS file and otool tools")
	}

	const version = "0.0.0-releasetest"
	outputDir := t.TempDir()
	result := runScript(t, nil, "--skip-checks", "--version", version, "--output-dir", outputDir)
	if result.exitCode != 0 {
		t.Fatalf("release exit code = %d, want 0; stdout: %s stderr: %s",
			result.exitCode, result.stdout, result.stderr)
	}

	binary := filepath.Join(outputDir, "isolated-dev")
	info, err := os.Stat(binary)
	if err != nil {
		t.Fatalf("stat built binary: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("built binary is not executable (mode %v)", info.Mode().Perm())
	}

	description := commandOutput(t, "file", "-b", binary)
	if !strings.Contains(description, "Mach-O 64-bit executable arm64") {
		t.Fatalf("built binary is not an Apple-silicon Mach-O executable: %s", description)
	}

	for _, library := range linkedLibraries(t, binary) {
		if !strings.HasPrefix(library, "/usr/lib/") && !strings.HasPrefix(library, "/System/Library/") {
			t.Fatalf("built binary is not self-contained: it links %s", library)
		}
	}

	if runtime.GOARCH == "arm64" {
		reported := strings.TrimSpace(commandOutput(t, binary, "--version"))
		if reported != "isolated-dev "+version {
			t.Fatalf("binary reports %q, want %q", reported, "isolated-dev "+version)
		}
	}

	archive := "isolated-dev-" + version + "-darwin-arm64.tar.gz"
	if _, err := os.Stat(filepath.Join(outputDir, archive)); err != nil {
		t.Fatalf("stat release archive: %v", err)
	}
	// The checksum is what a developer verifies a download against, so it has
	// to be checkable exactly as published, from the directory it ships in.
	verify := exec.Command("shasum", "-a", "256", "-c", archive+".sha256")
	verify.Dir = outputDir
	if output, err := verify.CombinedOutput(); err != nil {
		t.Fatalf("published checksum does not verify: %v\n%s", err, output)
	}
}

func commandOutput(t *testing.T, name string, args ...string) string {
	t.Helper()
	output, err := exec.Command(name, args...).Output()
	if err != nil {
		t.Fatalf("run %s %s: %v", name, strings.Join(args, " "), err)
	}
	return string(output)
}

// linkedLibraries returns the dynamic libraries the binary loads, which is the
// evidence that it needs nothing installed beside it.
func linkedLibraries(t *testing.T, binary string) []string {
	t.Helper()
	if _, err := exec.LookPath("otool"); err != nil {
		t.Skip("otool is unavailable; cannot inspect linked libraries")
	}
	var libraries []string
	// otool prints the inspected file on the first line, then one indented
	// library per line.
	for _, line := range strings.Split(commandOutput(t, "otool", "-L", binary), "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		libraries = append(libraries, fields[0])
	}
	if len(libraries) == 0 {
		t.Fatalf("otool reported no linked libraries for %s", binary)
	}
	return libraries
}
