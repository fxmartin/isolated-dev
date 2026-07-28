#!/usr/bin/env bash
#
# Build the distributable isolated-dev release artifact: a versioned,
# self-contained Apple-silicon macOS binary plus its archive and checksum.
#
# The same script runs in CI and in a local isolated build environment, so what
# a release publishes is what a developer can reproduce.
#
#   scripts/release.sh --version 1.4.0
#   scripts/release.sh --dry-run
#
# usage: release.sh [--version VERSION] [--output-dir DIR] [--skip-checks] [--dry-run] [--help]
#
#   --version VERSION  version to stamp into the binary; a leading `v` is
#                      stripped. Defaults to $ISOLATED_DEV_VERSION, then to
#                      `git describe --tags --always --dirty`.
#   --output-dir DIR   where artifacts are written; relative paths resolve
#                      against the repository root. Default: dist
#   --skip-checks      build only, skipping formatting, vet, and the test
#                      suite. For iterating on the build itself; never for a
#                      published release.
#   --dry-run          print the ordered steps instead of running them.
#   --help, -h         print this usage line.

set -euo pipefail

readonly BINARY_NAME="isolated-dev"
readonly TARGET_OS="darwin"
readonly TARGET_ARCH="arm64"
readonly MAIN_PACKAGE="./cmd/isolated-dev"

version=""
output_dir="dist"
skip_checks=0
dry_run=0

die() {
	printf 'release.sh: %s\n' "$1" >&2
	exit "${2:-1}"
}

usage() {
	printf 'usage: release.sh [--version VERSION] [--output-dir DIR] [--skip-checks] [--dry-run] [--help]\n'
}

usage_error() {
	printf 'release.sh: %s\n' "$1" >&2
	usage >&2
	exit 2
}

note() {
	printf 'release.sh: %s\n' "$1" >&2
}

# step runs a command, or prints it when this is a dry run. Every command that
# shapes the artifact goes through here, so the printed plan and the executed
# release cannot drift apart.
step() {
	if ((dry_run)); then
		printf '%s\n' "$*"
		return 0
	fi
	"$@"
}

# require_value guards a flag that takes an argument. Checking only that an
# argument follows is not enough: `--version --dry-run` would consume the very
# flag that was meant to keep the run from producing anything, so a value that
# looks like a flag is a mistake rather than a value.
require_value() {
	local flag="$1"
	local count="$2"
	local value="${3-}"
	((count >= 2)) || usage_error "$flag needs a value"
	[[ "$value" != -* ]] || usage_error "$flag needs a value, but was given the flag $value"
}

parse_arguments() {
	while (($# > 0)); do
		case "$1" in
		--version)
			require_value --version "$#" "${2-}"
			version="$2"
			shift 2
			;;
		--output-dir)
			require_value --output-dir "$#" "${2-}"
			output_dir="$2"
			shift 2
			;;
		--skip-checks)
			skip_checks=1
			shift
			;;
		--dry-run)
			dry_run=1
			shift
			;;
		-h | --help)
			usage
			exit 0
			;;
		*)
			usage_error "unexpected argument: $1"
			;;
		esac
	done
}

# resolve_version prefers the explicit flag, then the environment, then the Git
# description, so a tag push, a manual dispatch, and a local build all produce a
# binary that can say which source it came from.
resolve_version() {
	local resolved="$version"
	[[ -n "$resolved" ]] || resolved="${ISOLATED_DEV_VERSION:-}"
	[[ -n "$resolved" ]] || resolved="$(git describe --tags --always --dirty 2>/dev/null || true)"
	[[ -n "$resolved" ]] ||
		usage_error "cannot determine a version; pass --version or set ISOLATED_DEV_VERSION"

	resolved="${resolved#v}"
	# The version becomes part of a file name and of the binary's own output,
	# so anything that is not a single plain token is a mistake worth naming.
	# A leading dash is excluded for the same reason a file name would not want
	# one: it reads as a flag wherever the version is passed on.
	[[ "$resolved" =~ ^[A-Za-z0-9._+][A-Za-z0-9._+-]*$ ]] ||
		usage_error "version must be a single token of letters, digits, and .+-_ : $resolved"
	[[ "$resolved" != .* ]] || usage_error "version must not start with a dot: $resolved"

	version="$resolved"
}

run_checks() {
	if ((skip_checks)); then
		note "skipping formatting, vet, and the test suite (--skip-checks); not a publishable build"
		return 0
	fi

	if ((dry_run)); then
		printf 'gofmt -l .\n'
	else
		local unformatted
		unformatted="$(gofmt -l .)"
		[[ -z "$unformatted" ]] || die "unformatted files:"$'\n'"$unformatted"
	fi

	step go vet ./...
	step go test ./...
}

# build_binary produces the artifact a clean Mac runs. CGO_ENABLED=0 is what
# keeps it self-contained: it links only macOS system libraries, so no Go
# toolchain, language runtime, or package manager has to exist on the host.
build_binary() {
	local binary="$1"

	step mkdir -p "$output_dir"
	step env \
		CGO_ENABLED=0 \
		GOOS="$TARGET_OS" \
		GOARCH="$TARGET_ARCH" \
		go build \
		-trimpath \
		-ldflags "-s -w -X main.version=$version" \
		-o "$binary" \
		"$MAIN_PACKAGE"
}

# verify_binary refuses to publish an artifact that does not match what the
# documentation promises: an Apple-silicon executable, dependency-free, and
# stamped with the version this run was asked for.
verify_binary() {
	local binary="$1"

	local description
	description="$(file -b "$binary")"
	[[ "$description" == *"Mach-O 64-bit executable arm64"* ]] ||
		die "built binary is not an Apple-silicon Mach-O executable: $description"

	if command -v otool >/dev/null 2>&1; then
		local foreign
		foreign="$(otool -L "$binary" | tail -n +2 | awk '{print $1}' |
			grep -vE '^(/usr/lib/|/System/Library/)' || true)"
		[[ -z "$foreign" ]] ||
			die "built binary is not self-contained; it links:"$'\n'"$foreign"
	else
		note "otool is unavailable; linked libraries were not inspected"
	fi

	# Only an Apple-silicon macOS host can execute what was just built.
	if [[ "$(uname -s)" == "Darwin" && "$(uname -m)" == "$TARGET_ARCH" ]]; then
		local reported
		reported="$("$binary" --version)"
		[[ "$reported" == "$BINARY_NAME $version" ]] ||
			die "built binary reports '$reported', expected '$BINARY_NAME $version'"
	else
		note "host cannot execute $TARGET_OS/$TARGET_ARCH; the reported version was not checked"
	fi
}

# package_artifact publishes the archive together with a checksum that verifies
# from the directory it ships in, which is how a download is checked.
package_artifact() {
	local archive="$1"

	step tar -czf "$output_dir/$archive" -C "$output_dir" "$BINARY_NAME"
	if ((dry_run)); then
		printf 'shasum -a 256 %s > %s.sha256\n' "$archive" "$archive"
	else
		(cd "$output_dir" && shasum -a 256 "$archive" >"$archive.sha256")
	fi
}

main() {
	parse_arguments "$@"

	cd "$(dirname "${BASH_SOURCE[0]}")/.."
	resolve_version

	local binary="$output_dir/$BINARY_NAME"
	local archive="$BINARY_NAME-$version-$TARGET_OS-$TARGET_ARCH.tar.gz"

	run_checks
	build_binary "$binary"
	if ((dry_run)); then
		printf 'verify %s is a self-contained %s/%s binary reporting version %s\n' \
			"$binary" "$TARGET_OS" "$TARGET_ARCH" "$version"
	else
		verify_binary "$binary"
	fi
	package_artifact "$archive"

	printf '%s %s\n' "$output_dir/$archive" "$version"
}

main "$@"
