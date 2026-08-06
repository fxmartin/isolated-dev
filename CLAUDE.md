# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`isolated-dev` is a macOS-only Go CLI that manages persistent, per-project Linux
development machines on Apple Container Machine. Project source stays on macOS
(mounted, never copied); toolchains, Docker Engine, and Compose run in the
guest. It ships as one statically linked darwin/arm64 binary
(`cmd/isolated-dev`). See README.md for user-facing behavior — it is
authoritative and detailed, and tests in `internal/release` assert parts of it.

## Commands

```sh
go test ./...                          # full unit suite (host-backed tests self-skip)
go test ./internal/config/             # one package
go test ./internal/app/ -run TestName  # one test
gofmt -l . && go vet ./...             # the checks release.sh enforces
scripts/release.sh --version 0.0.0-local   # fmt + vet + test + build + verify + package into dist/
scripts/release.sh --dry-run           # print the release steps without running them
```

Host-backed integration tests (in `internal/guest`, `internal/forge`,
`internal/projectcmd`) skip unless `ISOLATED_DEV_RUN_HOST_TESTS=1` is set. They
need Apple Container services running (`container system start`) and really
create/destroy machines. The Forge acceptance tests additionally need a Forge
checkout, defaulting to `../forge`, overridable via `ISOLATED_DEV_FORGE_PATH`.
Never run them casually — the persistence test touches a real project's
machine.

There is no Makefile or linter config beyond `gofmt`/`go vet`; CI
(`.github/workflows/release.yml`) runs `scripts/release.sh` on macOS runners.

## Architecture

Dependency direction is strictly inward: `main` wires everything, `cli` parses,
`app` orchestrates, leaf packages talk to the outside world.

- **`cmd/isolated-dev/main.go`** — the only composition root. Constructs every
  concrete manager and injects them; nothing else calls `os.Exit` or builds
  dependencies.
- **`internal/cli`** — argument dispatch only. Takes a `Dependencies` struct of
  plain functions (`Up`, `Open`, `Run`, …); contains no business logic, so it
  tests without any host.
- **`internal/app`** — the orchestration layer. `App` (see
  `internal/app/status.go`) holds small interfaces (`MachineManager`,
  `GuestProvisioner`, `SSHConfigurator`, `TunnelManager`, `ZedLauncher`,
  `ProjectCommandRunner`, …) implemented by the leaf packages below. Unit tests
  substitute fakes for all of them.
- **Leaf packages**, each owning one external surface:
  `project` (repo path → canonical path + collision-resistant machine name),
  `config` (strict TOML loading of `.isolated-dev.toml` merged with
  `.isolated-dev.local.toml`), `host` (preflight checks), `baseimage`
  (build/cache `local/isolated-dev-base:<version>` from
  `images/base/Dockerfile`), `machine` (Apple `container` CLI lifecycle),
  `guest` (guest account + SSH key provisioning), `sshconfig` (managed
  `~/.ssh/isolated-dev/` config + `Include` directive), `tunnel` (background
  SSH port tunnels, state under `~/Library/Application Support/isolated-dev/`),
  `zed`, `projectcmd` (executes declared `[commands.<name>]` only), `state`
  (per-machine persisted state store), `status`, `upgrade`.
- **Acceptance workloads**: `internal/smoke` is the baseline nested-Compose
  test — it creates and destroys everything it touches. `internal/forge` runs a
  real project's DEV stack and deliberately owns and removes nothing.
- **`internal/release`** — tests that pin `scripts/release.sh`, the release
  workflow, and README/docs against each other. Editing the release script,
  workflow, or install docs can fail these tests; update them together.

## Invariants to preserve

These are load-bearing design rules, enforced by tests and documented in
README.md:

- Every command **reconciles toward declared state** and is safe to repeat.
  Never assume what a previous run left behind.
- **Nothing undeclared ever runs.** Lifecycle commands never inspect the
  repository for services; only `run` executes commands, and only ones declared
  in `[commands.<name>]`. Command `args` are an argument vector — no shell
  parsing.
- **Destructive operations require `--yes` in the same invocation**; there are
  no interactive prompts. `destroy` verifies both the canonical path and the
  derived machine name before removing anything.
- **Config validation is strict and fails before any state changes**: unknown
  keys, inline secret values, and unmanaged `base_image` values are errors.
  Secret files are checked for existence only — never opened, copied, or
  printed.
- **Only public SSH keys reach the guest**; files containing private key
  material are rejected. Managed SSH state lives in `~/.ssh/isolated-dev/`; the
  developer's own `~/.ssh/config` gets a single `Include` and is never
  rewritten.
- Warnings go to **stderr** and execution continues; errors stop before host or
  guest state changes.

## Conventions

- TDD: behavior changes start with a failing test. Test files follow the
  existing split — `*_test.go` for behavior, `*_coverage_test.go` and
  `*_boundary_test.go` for edge/branch coverage companions.
- Conventional Commits enforced on PRs (`feat(scope): subject`, lower-case,
  ≤72-char header). `feat` → minor, `fix` → patch.
- Story-driven development: scope lives in `REQUIREMENTS.md`, progress in
  `STORIES.md`, epics in `docs/stories/`. Commit subjects reference story IDs,
  e.g. `feat(isolated-dev-mvp): manage persistent localhost port (#01.7-001)`.
- Comments explain *why* (constraints, trade-offs), matching the existing
  heavily-reasoned comment style in packages like `forge` and `release.sh`.
