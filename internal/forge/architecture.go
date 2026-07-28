package forge

import (
	"fmt"
	"strings"
)

// Requirement is the support a failing image or build step needs before it can
// run on the guest. Apple silicon is arm64, and Forge is the first workload
// large enough to contain an image or a build step that is not.
type Requirement string

const (
	// RequirementPlatform means the image exists only for linux/amd64, so it has
	// to be selected — and then emulated — explicitly.
	RequirementPlatform Requirement = "linux/amd64"
	// RequirementRosetta means Rosetta itself is what failed.
	RequirementRosetta Requirement = "Rosetta"
	// RequirementBinfmt means the guest has no handler registered for foreign
	// binaries at all.
	RequirementBinfmt Requirement = "binfmt"
)

// maxSignatureLength keeps a quoted output line readable inside an error. A
// BuildKit line can carry a whole command; the beginning of it is what
// identifies the failure.
const maxSignatureLength = 300

// buildStepLookback bounds the search for the BuildKit step heading above a
// failure. BuildKit prints the step, then its output, so the heading is a few
// lines up at most.
const buildStepLookback = 40

// ArchitectureIssue is an architecture incompatibility recovered from the
// output of a failed startup, and what it needs to run.
type ArchitectureIssue struct {
	Requirement Requirement
	// Signature is the output line the classification came from, so the claim
	// can be checked against what Docker actually said.
	Signature string
	// Image and BuildStep are the affected image reference and the BuildKit step
	// they were recovered from. Either can be empty: a runtime failure names no
	// build step, and a build failure often names no image.
	Image     string
	BuildStep string
	Detail    string
}

// Affected names what could not run.
func (issue ArchitectureIssue) Affected() string {
	switch {
	case issue.Image != "" && issue.BuildStep != "":
		return fmt.Sprintf("image %s at build step %s", issue.Image, issue.BuildStep)
	case issue.Image != "":
		return "image " + issue.Image
	case issue.BuildStep != "":
		return "build step " + issue.BuildStep
	default:
		return "neither an image nor a build step is named in the output"
	}
}

func (issue ArchitectureIssue) String() string {
	return fmt.Sprintf(
		"architecture incompatibility: %s; %s support is required: %s\noutput: %s",
		issue.Affected(),
		issue.Requirement,
		issue.Detail,
		issue.Signature,
	)
}

// architectureMatchers are the shapes Docker, BuildKit, and Compose print when
// an image or a build step is for another architecture, in the order a report
// should prefer them: a missing arm64 manifest explains the exec failures it
// causes, so it is looked for first.
var architectureMatchers = []struct {
	fragments   []string
	requirement Requirement
	// namesImage marks the messages that carry the affected image reference
	// before the message itself. The execution failures do not: what precedes
	// them is the path of the binary that could not run.
	namesImage bool
	detail     string
}{
	{
		fragments:   []string{"no matching manifest for linux/arm"},
		requirement: RequirementPlatform,
		namesImage:  true,
		detail: "the image is published without a linux/arm64 variant, so it can only run as linux/amd64, " +
			"which needs Rosetta or a binfmt handler registered inside the machine",
	},
	{
		fragments: []string{
			"does not match the detected host platform",
			"does not match the specified platform",
		},
		requirement: RequirementPlatform,
		namesImage:  true,
		detail: "the image that was pulled is linux/amd64 while the guest is linux/arm64, so it runs only " +
			"under Rosetta or a binfmt handler inside the machine",
	},
	{
		fragments:   []string{"rosetta error", "rosetta is not", "rosetta failed"},
		requirement: RequirementRosetta,
		detail: "linux/amd64 code is being translated by Rosetta and Rosetta itself failed, so Rosetta " +
			"support inside the machine is what has to be fixed",
	},
	{
		fragments:   []string{"exec format error", "qemu:", "binfmt"},
		requirement: RequirementBinfmt,
		detail: "the guest tried to execute a binary built for another architecture with no handler " +
			"registered for it, so binfmt support — Rosetta or qemu-user-static — is missing inside the machine",
	},
}

// ClassifyArchitecture recovers an architecture incompatibility from command
// output, and reports whether it found one.
//
// It deliberately claims nothing when it recognises nothing: a failure that is
// the project's own is more common than an architecture problem, and reporting
// the wrong cause is worse than reporting none.
func ClassifyArchitecture(output string) (ArchitectureIssue, bool) {
	lines := strings.Split(output, "\n")
	for _, matcher := range architectureMatchers {
		for index, line := range lines {
			fragment, matched := matchFragment(line, matcher.fragments)
			if !matched {
				continue
			}
			issue := ArchitectureIssue{
				Requirement: matcher.requirement,
				Signature:   shorten(strings.TrimSpace(line)),
				BuildStep:   buildStep(lines, index),
				Detail:      matcher.detail,
			}
			if matcher.namesImage {
				issue.Image = imageBefore(line, fragment)
			}
			return issue, true
		}
	}
	return ArchitectureIssue{}, false
}

// matchFragment reports the fragment a line carries, ignoring case: the same
// message is printed capitalised by the daemon and lower case by BuildKit.
func matchFragment(line string, fragments []string) (string, bool) {
	lowered := strings.ToLower(line)
	for _, fragment := range fragments {
		if strings.Contains(lowered, fragment) {
			return fragment, true
		}
	}
	return "", false
}

// imageBefore recovers the image reference a failure line names before the
// message itself, which is where Docker puts it: `failed to solve: IMAGE: no
// matching manifest ...`, `image IMAGE: The requested image's platform ...`.
func imageBefore(line string, fragment string) string {
	position := strings.Index(strings.ToLower(line), fragment)
	if position < 0 {
		return ""
	}
	fields := strings.Fields(line[:position])
	for index := len(fields) - 1; index >= 0; index-- {
		candidate := strings.Trim(fields[index], `:,;"'`)
		if isImageReference(candidate) {
			return candidate
		}
	}
	return ""
}

// isImageReference keeps prose, absolute paths, parenthesised platforms, and
// URLs from being reported as the affected image.
func isImageReference(candidate string) bool {
	if candidate == "" || strings.ContainsAny(candidate, "()[]<>") {
		return false
	}
	if strings.HasPrefix(candidate, "-") || strings.HasPrefix(candidate, "/") {
		return false
	}
	if strings.Contains(candidate, "://") {
		return false
	}
	return strings.Contains(candidate, ":") || strings.Contains(candidate, "/")
}

// buildStep recovers the BuildKit step a failure happened in. BuildKit prints
// the step heading above the output of that step, so the search runs upwards
// from the failing line; a `failed to solve: process "..."` line anywhere in the
// output is the fallback.
func buildStep(lines []string, index int) string {
	for position := index; position >= 0 && index-position <= buildStepLookback; position-- {
		trimmed := strings.TrimSpace(lines[position])
		if strings.HasPrefix(trimmed, "> [") {
			return shorten(strings.TrimSuffix(strings.TrimPrefix(trimmed, "> "), ":"))
		}
	}
	for _, line := range lines {
		if start := strings.Index(line, `process "`); start >= 0 {
			remainder := line[start+len(`process "`):]
			if end := strings.Index(remainder, `"`); end >= 0 {
				return shorten(`process "` + remainder[:end] + `"`)
			}
		}
	}
	return ""
}

func shorten(value string) string {
	if len(value) <= maxSignatureLength {
		return value
	}
	return value[:maxSignatureLength] + "…"
}
