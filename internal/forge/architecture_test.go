package forge

import (
	"strings"
	"testing"
)

// The samples below are the shapes Docker, BuildKit, and Compose actually print
// when an image or a build step cannot execute on the guest architecture. The
// acceptance run has to name the affected image or build step and the support it
// needs, and that answer is only ever recoverable from this output.

func TestClassifyArchitectureIdentifiesAnImageWithoutAnArm64Variant(t *testing.T) {
	output := strings.Join([]string{
		"[+] Building 0.4s (2/2) FINISHED",
		" => ERROR [rosetta-dev-db internal] load metadata for docker.io/library/postgres:16-alpine",
		"------",
		" > [internal] load metadata for docker.io/library/postgres:16-alpine:",
		"------",
		"ERROR: failed to solve: postgres:16-alpine: no matching manifest for linux/arm64/v8 in the manifest list entries",
	}, "\n")

	issue, found := ClassifyArchitecture(output)
	if !found {
		t.Fatalf("ClassifyArchitecture() found = false, want an architecture incompatibility in:\n%s", output)
	}
	if issue.Requirement != RequirementPlatform {
		t.Errorf("Requirement = %q, want %q", issue.Requirement, RequirementPlatform)
	}
	if issue.Image != "postgres:16-alpine" {
		t.Errorf("Image = %q, want the image with no arm64 variant", issue.Image)
	}
	if !strings.Contains(issue.String(), "postgres:16-alpine") ||
		!strings.Contains(issue.String(), string(RequirementPlatform)) {
		t.Errorf("String() = %q, want it to name the image and the required support", issue.String())
	}
}

func TestClassifyArchitectureIdentifiesTheFailingBuildStep(t *testing.T) {
	output := strings.Join([]string{
		"#12 [rosetta-dev-backend builder 4/9] RUN uv sync --frozen --no-dev",
		"------",
		" > [rosetta-dev-backend builder 4/9] RUN uv sync --frozen --no-dev:",
		"0.191 exec /bin/sh: exec format error",
		"------",
		`ERROR: failed to solve: process "/bin/sh -c uv sync --frozen --no-dev" did not complete successfully: exit code: 1`,
	}, "\n")

	issue, found := ClassifyArchitecture(output)
	if !found {
		t.Fatalf("ClassifyArchitecture() found = false, want the failing build step reported")
	}
	if issue.Requirement != RequirementBinfmt {
		t.Errorf("Requirement = %q, want %q", issue.Requirement, RequirementBinfmt)
	}
	want := "[rosetta-dev-backend builder 4/9] RUN uv sync --frozen --no-dev"
	if issue.BuildStep != want {
		t.Errorf("BuildStep = %q, want %q", issue.BuildStep, want)
	}
	if !strings.Contains(issue.Affected(), want) {
		t.Errorf("Affected() = %q, want it to name the build step", issue.Affected())
	}
}

func TestClassifyArchitectureIdentifiesARequestedAmd64Image(t *testing.T) {
	output := "WARN[0000] image rosetta-backend:latest: The requested image's platform (linux/amd64) " +
		"does not match the detected host platform (linux/arm64/v8) and no specific platform was requested"

	issue, found := ClassifyArchitecture(output)
	if !found {
		t.Fatalf("ClassifyArchitecture() found = false, want the platform mismatch reported")
	}
	if issue.Requirement != RequirementPlatform {
		t.Errorf("Requirement = %q, want %q", issue.Requirement, RequirementPlatform)
	}
	if issue.Image != "rosetta-backend:latest" {
		t.Errorf("Image = %q, want the mismatched image", issue.Image)
	}
}

func TestClassifyArchitectureIdentifiesRosetta(t *testing.T) {
	output := strings.Join([]string{
		"rosetta-dev-backend  | rosetta error: failed to open elf at /lib64/ld-linux-x86-64.so.2",
		"rosetta-dev-backend exited with code 1",
	}, "\n")

	issue, found := ClassifyArchitecture(output)
	if !found {
		t.Fatalf("ClassifyArchitecture() found = false, want the Rosetta failure reported")
	}
	if issue.Requirement != RequirementRosetta {
		t.Errorf("Requirement = %q, want %q", issue.Requirement, RequirementRosetta)
	}
}

func TestClassifyArchitectureIdentifiesAMissingBinfmtHandler(t *testing.T) {
	for name, output := range map[string]string{
		"exec format error": "exec /usr/local/bin/python: exec format error",
		"qemu":              "qemu: uncaught target signal 11 (Segmentation fault) - core dumped",
		"binfmt":            "cannot execute binary: binfmt_misc handler is not registered",
	} {
		t.Run(name, func(t *testing.T) {
			issue, found := ClassifyArchitecture(output)
			if !found {
				t.Fatalf("ClassifyArchitecture(%q) found = false, want an emulation requirement", output)
			}
			if issue.Requirement != RequirementBinfmt {
				t.Errorf("Requirement = %q, want %q", issue.Requirement, RequirementBinfmt)
			}
			if issue.Signature == "" {
				t.Error("Signature is empty, want the line the classification came from")
			}
		})
	}
}

func TestClassifyArchitectureLeavesUnrelatedFailuresUnclassified(t *testing.T) {
	for name, output := range map[string]string{
		"empty":            "",
		"application":      "npm ERR! code ELIFECYCLE\nnpm ERR! errno 1",
		"missing env file": "env file /home/fx/dev/forge/.env.dev not found: stat .env.dev: no such file or directory",
	} {
		t.Run(name, func(t *testing.T) {
			if issue, found := ClassifyArchitecture(output); found {
				t.Errorf("ClassifyArchitecture() = %+v, true; want no architecture claim for %q", issue, output)
			}
		})
	}
}

func TestArchitectureIssueReportsWhatItCouldNotName(t *testing.T) {
	issue, found := ClassifyArchitecture("exec /app/server: exec format error")
	if !found {
		t.Fatal("ClassifyArchitecture() found = false, want the emulation requirement")
	}
	if issue.Image != "" || issue.BuildStep != "" {
		t.Fatalf("issue = %+v, want no image or build step recovered from a bare runtime failure", issue)
	}
	if !strings.Contains(issue.Affected(), "neither an image nor a build step") {
		t.Errorf("Affected() = %q, want it to say what could not be identified", issue.Affected())
	}
	if !strings.Contains(issue.String(), "exec format error") {
		t.Errorf("String() = %q, want the matched output in it", issue.String())
	}
}

// TestArchitectureIssueNamesTheImageAndTheBuildStepTogether covers the report
// the acceptance criteria ask for when the output carries both: a build that
// stopped on an image it could not pull is located by the pair, not by either
// half of it.
func TestArchitectureIssueNamesTheImageAndTheBuildStepTogether(t *testing.T) {
	output := strings.Join([]string{
		" > [rosetta-dev-db internal] load metadata for docker.io/library/postgres:16-alpine:",
		"------",
		"ERROR: failed to solve: postgres:16-alpine: no matching manifest for linux/arm64/v8 in the manifest list entries",
	}, "\n")

	issue, found := ClassifyArchitecture(output)
	if !found {
		t.Fatal("ClassifyArchitecture() found = false, want the missing arm64 manifest reported")
	}
	if issue.Image == "" || issue.BuildStep == "" {
		t.Fatalf("issue = %+v, want both the image and the build step recovered", issue)
	}
	affected := issue.Affected()
	if !strings.Contains(affected, "image "+issue.Image) ||
		!strings.Contains(affected, "build step "+issue.BuildStep) {
		t.Errorf("Affected() = %q, want it to name the image and the build step it failed at", affected)
	}
}

func TestClassifyArchitecturePrefersTheRootCauseOverItsConsequence(t *testing.T) {
	// A missing arm64 manifest is why the following step could not execute, so
	// the report has to name the image rather than the failure it caused.
	output := strings.Join([]string{
		"exec /bin/sh: exec format error",
		"ERROR: failed to solve: postgres:16-alpine: no matching manifest for linux/arm64/v8 in the manifest list entries",
	}, "\n")

	issue, _ := ClassifyArchitecture(output)
	if issue.Requirement != RequirementPlatform || issue.Image != "postgres:16-alpine" {
		t.Errorf("issue = %+v, want the missing arm64 manifest named as the cause", issue)
	}
}
