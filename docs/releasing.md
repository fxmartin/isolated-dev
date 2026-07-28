# Releasing isolated-dev

A release is one Apple-silicon macOS binary plus its archive and checksum.
[`scripts/release.sh`](../scripts/release.sh) is the only thing that produces
it, and CI runs that same script, so what is published is exactly what a
developer can reproduce locally.

## What the script does

```sh
scripts/release.sh --version 1.4.0
```

In order:

1. `gofmt -l .`, `go vet ./...`, and `go test ./...`. The artifact is only ever
   built after the suite has passed. Host-backed tests need Apple Container and
   stay skipped unless `ISOLATED_DEV_RUN_HOST_TESTS=1` is set, so CI runs the
   portable suite.
2. `go build` for `darwin/arm64` with `CGO_ENABLED=0`, `-trimpath`, and
   `-ldflags "-s -w -X main.version=<version>"`.
3. Verification of the result: it must be a `Mach-O 64-bit executable arm64`,
   it must link nothing outside `/usr/lib` and `/System/Library`, and — when
   the build host can execute it — it must report the version it was stamped
   with.
4. `dist/isolated-dev-<version>-darwin-arm64.tar.gz` and a `.sha256` file that
   verifies from the directory it ships in.

`CGO_ENABLED=0` is what the "no toolchain required" promise rests on: the
binary links only macOS system libraries, so a clean supported Mac needs no Go
toolchain, language runtime, package manager, or host Docker installation.
Step 3 is what keeps that a checked property rather than an assumption; the
same properties are asserted independently by the Go test suite in
[`internal/release`](../internal/release/release_test.go).

Useful flags:

- `--dry-run` prints the ordered steps instead of running them.
- `--skip-checks` builds only. For iterating on the build itself — never for a
  published release, and the script says so on standard error.
- `--output-dir DIR` writes elsewhere than `dist/`.
- `--help` (or `-h`) prints the usage line and the flags.
- The version defaults to `$ISOLATED_DEV_VERSION`, then to
  `git describe --tags --always --dirty`. A leading `v` is always stripped, so
  tag `v1.4.0` produces a binary reporting `1.4.0`.

## Cutting a release

```sh
git tag -a v1.4.0 -m "isolated-dev 1.4.0"
git push origin v1.4.0
```

[`.github/workflows/release.yml`](../.github/workflows/release.yml) runs on an
Apple-silicon macOS runner — the binary must be executed to verify it — builds
through `scripts/release.sh`, and attaches the archive and checksum to the
GitHub release. The workflow can also be dispatched manually with an explicit
version, which uploads the artifact without publishing a release.

## Signing, notarization, and Gatekeeper

**Current status: releases are unsigned and un-notarized.** This is a stated
limitation, not something worked around in the tooling, and it is the one
release-readiness item that distribution beyond the first user requires.

What that means in practice:

- An archive downloaded through a browser carries the `com.apple.quarantine`
  extended attribute. What matters for the installed binary is how the archive
  is *extracted*, not how it was fetched: `tar -xzf` does not propagate the
  attribute to the extracted file, while Finder's Archive Utility applies it to
  everything it unpacks. So the README's `tar` install path is unaffected, and
  a double-clicked archive produces a binary Gatekeeper refuses to run,
  reporting that it cannot be verified.
- The documented workaround, for the Finder case, is for the user to remove
  that attribute themselves, after verifying the published checksum:
  `xattr -d com.apple.quarantine /usr/local/bin/isolated-dev`. It is deliberate
  and informed; nothing in the build strips quarantine, disables Gatekeeper, or
  ships an installer that does either. On the `tar` path the same command exits
  non-zero with `No such xattr`, because there is no attribute to remove.
- Neither the download method nor the extraction method is a security guarantee
  — the checksum is what you actually verify against.

Making releases pass Gatekeeper unaided requires all of the following:

1. **Apple Developer Program membership**, which is what issues the identity.
   There is no unpaid path to a certificate Gatekeeper trusts.
2. **A Developer ID Application certificate** in the signing keychain, plus an
   App Store Connect API key or an app-specific password for notarization.
3. **Signing with a hardened runtime and a secure timestamp**, which
   notarization requires:

   ```sh
   codesign --sign "Developer ID Application: <team>" \
     --options runtime --timestamp dist/isolated-dev
   ```

4. **Notarization of an archive containing the signed binary**, waiting for the
   result rather than assuming it:

   ```sh
   ditto -c -k --keepParent dist/isolated-dev dist/isolated-dev.zip
   xcrun notarytool submit dist/isolated-dev.zip \
     --keychain-profile "<profile>" --wait
   ```

5. **A decision about stapling.** A notarization ticket cannot be stapled to a
   bare Mach-O executable — `xcrun stapler staple` accepts `.app` bundles,
   `.dmg`, and `.pkg`. A CLI shipped as a tarball therefore relies on
   Gatekeeper's online ticket lookup, which fails on a machine with no network
   on first launch. Distributing a signed and stapled `.pkg` is the way to make
   first launch work offline, and it is the natural shape for this if
   distribution grows past the initial user.

Until step 1 is done, none of the rest can be, which is why the release
workflow does not attempt a partial version of it: a build that signs with an
untrusted identity would look protected while giving a user nothing.
