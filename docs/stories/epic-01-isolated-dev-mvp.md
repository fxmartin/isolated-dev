# Epic 01: Isolated Dev MVP

## Epic Overview

Deliver a reusable, single-user macOS CLI that creates one persistent Apple
Container Machine per web project. Source remains on macOS while Git, project
toolchains, Docker Engine, Docker Compose, and application services run inside
Linux. The MVP is accepted when the unmodified Forge DEV Compose profile works
through Zed and macOS localhost forwarding.

## Business Value

FX can work across web projects without installing project-only runtimes,
package managers, databases, or Docker on macOS. Each project gains a
reproducible Linux environment that is fast to resume, explicit to destroy, and
compatible with its existing Docker Compose workflow.

## Success Metrics

- A self-contained Apple-silicon binary performs setup and opens Zed without a
  host language runtime or Docker installation.
- Cached startup connects Zed within 30 seconds.
- The cached Forge DEV stack becomes healthy within 2 minutes.
- Git, file watching, nested bind mounts, persisted Compose data, and managed
  localhost forwarding work end to end.
- Stop/start preserves project state; upgrade and destroy never remove state
  without an explicit confirmation.

## Scope and Non-Goals

The MVP covers one local user, one machine per project, macOS 26 or newer,
Apple Container CLI 1.x, Zed, and Apple-silicon workloads. It does not cover
teams, fleet management, non-macOS hosts, automatic project-service startup,
secret storage, automatic machine upgrades, or transparent migration of
guest-only state.

## Feature Breakdown

- **CLI and configuration:** Stories 01.1-01.2
- **Guest platform and lifecycle:** Stories 01.3-01.5 and 01.9
- **Developer workflow:** Stories 01.6-01.8
- **Integration and release:** Stories 01.10-01.13

## Stories

### Story 01.1: Establish the Go CLI and Configuration Model

**Value:** Provide a testable host application with deterministic,
implementation-ready configuration behavior.

- **Points:** 5
- **Risk:** Medium
- **Dependencies:** None

**Acceptance Criteria**

- Given the binary, when `--version` is invoked, then it reports its version
  without changing host or machine state.
- Given valid shared and local TOML files, when configuration is loaded, then
  documented local CPU, memory, and port fields override shared values.
- Given an unknown field, invalid value, disallowed local override, or inline
  secret, when validation runs, then it identifies the exact file and field
  before any mutation.
- Given no configuration files, when a command starts, then documented defaults
  produce a valid effective configuration.

### Story 01.2: Validate Host Prerequisites and Track Project State

**Value:** Fail safely and associate every operation with the intended project.

- **Points:** 3
- **Risk:** Low
- **Dependencies:** Story 01.1

**Acceptance Criteria**

- Given a repository path, when it is resolved, then the CLI derives a stable,
  collision-resistant machine identity from its canonical path.
- Given missing or incompatible Apple Container or SSH prerequisites, when a
  mutating command runs, then it exits with an actionable message before
  changing state.
- Given an initialized project, when `status` runs, then it reports CLI,
  base-image, machine, effective-configuration, mount, and tunnel state without
  exposing secret values.

### Story 01.3: Build and Cache the Versioned Guest Base Image

**Value:** Avoid reinstalling common tooling for every project machine.

- **Points:** 5
- **Risk:** High
- **Dependencies:** Stories 01.1-01.2

**Acceptance Criteria**

- Given no cached image for the selected version, when it is requested, then an
  Ubuntu 24.04 image is built with Docker Engine, Compose v2, Git, SSH,
  certificates, and documented common utilities.
- Given an existing matching image, when another project starts, then the image
  is reused without reinstalling common packages.
- Given the observed Container Machine first-command failure, when the guest
  boots, then readiness is retried and Docker becomes available even if
  systemd is unavailable.
- Given a ready guest, when `docker info` runs, then it confirms a functional
  daemon using cgroup v2 and `overlay2`.

### Story 01.4: Implement Safe Project-Machine Lifecycle

**Value:** Make project environments persistent, repeatable, and removable.

- **Points:** 8
- **Risk:** High
- **Dependencies:** Stories 01.2-01.3

**Acceptance Criteria**

- Given no project machine, when `up` runs, then it creates one from the pinned
  base image, mounts the source, waits for readiness, and records state.
- Given a running or stopped machine, when `up` runs again, then it converges on
  the declared state without creating a duplicate.
- Given a stopped and restarted machine, when inspected, then installed
  packages, Docker images, Compose volumes, and project data remain present.
- Given incompatible immutable settings, when `up` runs, then it explains the
  required migration or recreation without performing it.
- Given `destroy`, when confirmation is declined, then no machine or persistent
  data is removed; when confirmed, only the resolved project machine is
  deleted.

### Story 01.5: Configure Guest Identity, Mounts, and Credentials

**Value:** Preserve source ownership and usable credentials without routine
root access or copied private keys.

- **Points:** 5
- **Risk:** High
- **Dependencies:** Stories 01.3-01.4

**Acceptance Criteria**

- Given a new machine, when provisioning completes, then a non-root guest user
  matches the invoking macOS UID/GID and has passwordless sudo and Docker-group
  access.
- Given SSH access, when a session starts, then password login and direct root
  login are disabled and the macOS SSH agent is forwarded without copying keys.
- Given repository-only mounting is supported, when `up` runs, then only
  required paths are exposed; otherwise the full-home fallback is clearly
  warned and reported by `status`.
- Given files created through Zed, a guest shell, or a nested bind mount, when
  viewed from macOS, then they have usable ownership and preserve executable
  bits and symlinks.
- Given a referenced `.env` or secret file, when provisioning runs, then only
  its existence is checked and its contents are never parsed, copied, or
  logged.

### Story 01.6: Integrate Managed SSH Access with Zed

**Value:** Open any prepared project remotely without manual SSH or Zed setup.

- **Points:** 5
- **Risk:** Medium
- **Dependencies:** Stories 01.4-01.5

**Acceptance Criteria**

- Given no integration, when `open` first runs, then the CLI adds one
  idempotent include to `~/.ssh/config` and writes separate tool-owned SSH and
  known-hosts files atomically with restrictive permissions.
- Given developer-owned SSH entries, when integration is reconciled, then their
  content and ordering remain unchanged.
- Given a ready machine, when `open` runs, then Zed opens the mounted project as
  the non-root guest user without manual configuration.
- Given a recreated machine or changed address, when `open` runs, then managed
  connection and host-key state is refreshed without altering global
  known-hosts entries.

### Story 01.7: Manage Persistent Localhost Port Tunnels

**Value:** Keep browser and API access stable independently of Zed's lifetime.

- **Points:** 5
- **Risk:** Medium
- **Dependencies:** Stories 01.4 and 01.6

**Acceptance Criteria**

- Given configured ports, when `up` or `open` succeeds, then one managed
  background SSH tunnel binds them to macOS loopback only.
- Given the CLI and Zed have exited, when the machine remains running, then the
  forwarded ports remain reachable.
- Given a changed machine address or dead tunnel, when reconciliation runs,
  then stale state is replaced without creating duplicate tunnel processes.
- Given a host-port conflict, when forwarding starts, then the CLI reports the
  affected mapping and does not disrupt the existing listener.
- Given `stop` or `destroy`, when cleanup completes, then the tunnel is gone and
  repeated cleanup remains safe.

### Story 01.8: Execute Only Explicit Project Commands

**Value:** Support convenient guest workflows without unexpectedly executing
  repository code.

- **Points:** 5
- **Risk:** Medium
- **Dependencies:** Stories 01.1 and 01.4-01.5

**Acceptance Criteria**

- Given a repository containing a Compose file, when `up` runs, then it neither
  discovers nor starts project services.
- Given a named command in shared configuration, when FX explicitly invokes it,
  then it runs in the mounted project as the non-root guest user with output
  and exit status preserved.
- Given an explicitly invoked Compose command, when Docker is not ready, then
  the CLI waits for `docker info` or exits with useful daemon diagnostics.
- Given a command not declared by the project, when invoked by name, then it is
  rejected without executing repository content.

### Story 01.9: Provide Explicit Base-Image Upgrades

**Value:** Allow controlled platform updates without surprising data loss.

- **Points:** 5
- **Risk:** High
- **Dependencies:** Stories 01.3-01.4

**Acceptance Criteria**

- Given a newer base image, when `status` runs, then the existing machine stays
  pinned and the available version is reported.
- Given `upgrade`, when previewed, then current and target versions plus
  potentially discarded guest packages, images, volumes, and data categories
  are shown.
- Given the preview is declined, when the command exits, then machine, tunnel,
  and persistent state are unchanged.
- Given recreation is confirmed, when it completes, then the replacement uses
  the target image and normal mount, identity, SSH, and tunnel reconciliation.

### Story 01.10: Automate the Baseline Nested-Compose Test

**Value:** Continuously prove that Docker-in-Container-Machine remains viable.

- **Points:** 5
- **Risk:** High
- **Dependencies:** Stories 01.3-01.5 and 01.8

**Acceptance Criteria**

- Given a fresh project machine, when the smoke test runs inside it, then
  Compose starts pinned BusyBox and Nginx images on a private network.
- Given a marker file on the macOS-mounted source path, when Nginx proxies to
  BusyBox, then the marker is returned inside the guest and through the macOS
  published port.
- Given the test completes or fails, when teardown runs, then its containers,
  network, machine, image, and temporary fixtures are removed without touching
  unrelated resources.
- Given Apple Container 1.1.0, when the known startup race occurs, then the test
  captures diagnostics and exercises the supported readiness fallback.

### Story 01.11: Run the Unmodified Forge DEV Stack

**Value:** Prove compatibility with the most complex representative project.

- **Points:** 8
- **Risk:** High
- **Dependencies:** Stories 01.5-01.8 and 01.10

**Acceptance Criteria**

- Given `../forge` mounted in a project machine, when its explicit DEV command
  runs, then `docker compose --profile dev up -d` uses the existing Compose file
  without modifications.
- Given the stack starts, when health is checked, then PostgreSQL 16, FastAPI,
  the Python worker, and the React/Vite-to-Nginx frontend are running.
- Given managed tunnels, when checked from macOS, then the frontend is reachable
  on localhost `3001` and backend health on localhost `8001`.
- Given any architecture incompatibility, when startup fails, then the result
  identifies the affected image or build step and whether `linux/amd64`,
  Rosetta, or binfmt support is required.

### Story 01.12: Validate Forge Persistence and Development Experience

**Value:** Confirm that the environment supports daily work, not only startup.

- **Points:** 5
- **Risk:** High
- **Dependencies:** Stories 01.6-01.7 and 01.11

**Acceptance Criteria**

- Given a running Forge stack with seeded data, when the project machine is
  stopped and restarted, then named database and application volumes retain
  their data.
- Given Forge opened in Zed, when source is edited on macOS or through Zed, then
  expected file watchers rebuild or reload the affected service.
- Given the forwarded SSH agent, when Git fetch or pull runs inside Linux, then
  authentication succeeds without guest private-key files.
- Given cold and cached runs, when measured, then timings and CPU, memory, disk,
  and mount-performance observations are recorded; cached Zed and Forge
  readiness are evaluated against 30-second and 2-minute targets.
- Given the measurements, when defaults are selected, then CPU, memory, and disk
  values are documented with their rationale.

### Story 01.13: Package and Document the MVP

**Value:** Make the validated workflow repeatable from a clean supported Mac.

- **Points:** 3
- **Risk:** Medium
- **Dependencies:** Stories 01.1-01.12

**Acceptance Criteria**

- Given the release workflow, when it runs in CI or an isolated build
  environment, then it produces a versioned self-contained Apple-silicon
  macOS binary and runs the Go test suite.
- Given a supported clean Mac with Apple Container, SSH, and Zed, when the
  binary is installed, then no Go toolchain, language runtime, package manager,
  or host Docker installation is required.
- Given the documentation, when followed, then FX can configure, start, open,
  inspect, stop, upgrade, and destroy an environment and understand all
  destructive confirmations and mount warnings.
- Given distribution beyond the initial user, when release readiness is
  assessed, then signing, notarization, and Gatekeeper requirements are
  documented rather than silently bypassed.

## Dependencies

- Apple silicon with macOS 26 or newer
- Apple Container CLI/API server 1.x and compatible ARM64 kernel bundle
- Zed with SSH remote development
- Internet access for initial packages, images, and remote editor components
- The adjacent `../forge` repository for final acceptance

## Definition of Done

- Business behavior is developed test-first where it can be captured cleanly.
- Unit tests cover configuration, identity, state, lifecycle decisions, and
  command construction.
- Integration tests cover Apple Container, SSH/tunnel lifecycle, nested
  Compose, and the Forge acceptance path.
- Operations are idempotent, scoped to the resolved project, and provide
  actionable diagnostics without secret values.
- `go test ./...`, formatting, static analysis, and documentation checks pass.

## Assumptions

- The explicit named-command interface will use `run` semantics; final argument
  ordering may follow the established CLI framework conventions.
- The MVP may use the warned full-home mount fallback because it has one trusted
  local user.
- Base-image recreation does not migrate guest-only state; the upgrade preview
  must state that limitation accurately.
