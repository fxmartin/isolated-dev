package release

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/fxmartin/isolated-dev/internal/cli"
)

// documentedFiles are the two documents a developer arriving at a clean Mac
// reads: what to install and run, and how a release is produced.
var documentedFiles = []string{"README.md", filepath.Join("docs", "releasing.md")}

func readDocument(t *testing.T, relativePath string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(repositoryRoot(t), relativePath))
	if err != nil {
		t.Fatalf("read %s: %v", relativePath, err)
	}
	return string(content)
}

var (
	markdownLink    = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)\)`)
	markdownHeading = regexp.MustCompile(`(?m)^#{1,6} +(.+?)\s*$`)
	commandTableRow = regexp.MustCompile("`isolated-dev ([^`]+)`")
	scriptFlag      = regexp.MustCompile(`--[a-z][a-z-]*`)
	nonSlugRune     = regexp.MustCompile(`[^a-z0-9 -]`)
	systemInstall   = regexp.MustCompile(`(?m)^(sudo +)?install +.*/usr/local/bin`)
)

// headingAnchor reproduces GitHub's heading slugs closely enough for the
// section links these documents use.
func headingAnchor(heading string) string {
	slug := strings.ToLower(strings.TrimSpace(heading))
	slug = nonSlugRune.ReplaceAllString(slug, "")
	return strings.ReplaceAll(slug, " ", "-")
}

// TestDocumentedLinksResolve keeps the packaged documentation navigable. The
// README hands a reader off to the requirements, the release process, and the
// base-image Dockerfile; a link that no longer resolves is a dead end for
// exactly the person this story exists for — someone starting from nothing.
func TestDocumentedLinksResolve(t *testing.T) {
	root := repositoryRoot(t)

	for _, document := range documentedFiles {
		content := readDocument(t, document)

		anchors := map[string]bool{}
		for _, heading := range markdownHeading.FindAllStringSubmatch(content, -1) {
			anchors[headingAnchor(heading[1])] = true
		}

		for _, link := range markdownLink.FindAllStringSubmatch(content, -1) {
			target := link[1]
			switch {
			case strings.HasPrefix(target, "http://"), strings.HasPrefix(target, "https://"),
				strings.HasPrefix(target, "mailto:"):
				continue
			case strings.HasPrefix(target, "#"):
				if !anchors[strings.TrimPrefix(target, "#")] {
					t.Errorf("%s links to %s, which is not a heading in it", document, target)
				}
				continue
			}
			path := filepath.Join(root, filepath.Dir(document), target)
			if _, err := os.Stat(path); err != nil {
				t.Errorf("%s links to %s, which does not exist: %v", document, target, err)
			}
		}
	}
}

// TestDocumentedArtifactNamesMatchWhatTheScriptProduces keeps the install
// instructions runnable. The README tells a developer to verify a checksum and
// extract an archive by name; if the script's naming ever changed, those
// commands would fail against a real download while still looking correct.
func TestDocumentedArtifactNamesMatchWhatTheScriptProduces(t *testing.T) {
	result := runScript(t, nil, "--dry-run", "--version", "1.2.3")
	if result.exitCode != 0 {
		t.Fatalf("dry run exit code = %d, want 0; stderr: %s", result.exitCode, result.stderr)
	}

	archive := regexp.MustCompile(`isolated-dev-1\.2\.3-[a-z0-9-]+\.tar\.gz`).FindString(result.stdout)
	if archive == "" {
		t.Fatalf("the plan names no archive:\n%s", result.stdout)
	}
	documented := strings.Replace(archive, "1.2.3", "<version>", 1)

	readme := readDocument(t, "README.md")
	requireContains(t, "README install instructions", readme, "shasum -a 256 -c "+documented+".sha256")
	requireContains(t, "README install instructions", readme, "tar -xzf "+documented)

	// The default output directory is part of what the release process
	// promises, so the published path is documented whole.
	requireContains(t, "docs/releasing.md", readDocument(t, filepath.Join("docs", "releasing.md")),
		"dist/"+documented)
}

// TestDocumentedInstallsIntoUsrLocalBinArePrivileged keeps the primary user
// journey runnable. `/usr/local/bin` is root-owned on macOS and does not exist
// at all on a Mac without Homebrew, so an unprivileged `install` into it stops
// with `Permission denied` partway through the documented block — after the
// reader has already verified a checksum and extracted an archive.
func TestDocumentedInstallsIntoUsrLocalBinArePrivileged(t *testing.T) {
	for _, document := range documentedFiles {
		commands := systemInstall.FindAllString(readDocument(t, document), -1)
		for _, command := range commands {
			if !strings.HasPrefix(command, "sudo ") {
				t.Errorf("%s documents %q, which fails because /usr/local/bin is root-owned", document, command)
			}
		}
	}
}

// TestQuarantineGuidanceNamesTheExtractionThatAppliesIt keeps the Gatekeeper
// advice true of what macOS actually does. `tar -xzf` does not propagate
// `com.apple.quarantine` from the archive to the extracted file — Finder's
// Archive Utility is what applies it — so guidance phrased around how the
// archive was *downloaded* sends a reader who followed the documented `tar`
// step to an `xattr -d` that exits non-zero with `No such xattr`.
func TestQuarantineGuidanceNamesTheExtractionThatAppliesIt(t *testing.T) {
	for _, document := range documentedFiles {
		content := readDocument(t, document)
		if !strings.Contains(content, "com.apple.quarantine") {
			continue
		}
		label := document + " quarantine guidance"
		requireContains(t, label, content, "Archive Utility")
		requireContains(t, label, content, "No such xattr")
	}
}

// TestEveryReleaseScriptFlagIsDocumented keeps the script's interface and its
// documentation in step. A flag that exists but is written down nowhere is one
// a developer can only find by reading the source, which is the opposite of a
// packaged tool.
func TestEveryReleaseScriptFlagIsDocumented(t *testing.T) {
	help := runScript(t, nil, "--help")
	if help.exitCode != 0 {
		t.Fatalf("--help exit code = %d, want 0; stderr: %s", help.exitCode, help.stderr)
	}

	documentation := readDocument(t, "README.md") +
		readDocument(t, filepath.Join("docs", "releasing.md"))

	flags := scriptFlag.FindAllString(help.stdout, -1)
	if len(flags) == 0 {
		t.Fatalf("the usage line advertises no flags:\n%s", help.stdout)
	}
	for _, flag := range flags {
		if !strings.Contains(documentation, flag) {
			t.Errorf("release.sh accepts %s, which neither README.md nor docs/releasing.md mentions", flag)
		}
	}
}

// TestDocumentedCommandsAreTheCommandsTheCLIAccepts pins the README's command
// table to the CLI itself. That table is the packaged tool's reference: a verb
// listed there that the binary rejects, or a verb the binary accepts that the
// table omits, both leave a reader with a wrong picture of what they installed.
func TestDocumentedCommandsAreTheCommandsTheCLIAccepts(t *testing.T) {
	var stderr bytes.Buffer
	if code := cli.Run(nil, cli.Dependencies{Stdout: &bytes.Buffer{}, Stderr: &stderr}); code != 2 {
		t.Fatalf("cli.Run(nil) = %d, want 2 for usage", code)
	}
	usage := stderr.String()

	accepted := map[string]bool{}
	for _, verb := range strings.Split(usage[strings.Index(usage, "<")+1:], "|") {
		fields := strings.Fields(strings.Trim(verb, "<>\n"))
		if len(fields) > 0 {
			accepted[fields[0]] = true
		}
	}
	if len(accepted) == 0 {
		t.Fatalf("no verbs parsed out of the usage line: %s", usage)
	}

	documented := map[string]bool{}
	for _, row := range commandTableRow.FindAllStringSubmatch(readDocument(t, "README.md"), -1) {
		fields := strings.Fields(row[1])
		if len(fields) > 0 {
			documented[fields[0]] = true
		}
	}

	for verb := range accepted {
		if !documented[verb] {
			t.Errorf("the CLI accepts %q, which the README does not document", verb)
		}
	}
	for verb := range documented {
		if !accepted[verb] {
			t.Errorf("the README documents %q, which the CLI does not accept: %s", verb, sortedKeys(accepted))
		}
	}
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
