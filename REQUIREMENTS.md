# Isolated Dev Requirements

## Problem and Goals

Web projects often require language runtimes, package managers, databases,
CLIs, and system libraries that are useful only for that project. Installing
them on macOS creates version conflicts, leaves stale tooling behind, and makes
project cleanup unreliable.

`isolated-dev` will provide a reusable, single-user CLI for running each project
in a persistent Apple Container Machine. macOS will retain only the
`isolated-dev` binary, Zed, a browser, Apple's `container` CLI, and the SSH
client used by Zed. Project source remains on macOS and is mounted read-write
into Linux. Git, project toolchains, Docker Engine, Docker Compose, and
application services run inside Linux.

The MVP must prove that the existing `../forge` DEV Docker Compose profile can
run from this environment without changing its `docker-compose.yml`.

## Target User

The initial user is FX, developing web applications on one Apple-silicon Mac
running macOS 26. Team onboarding, shared fleet management, and non-macOS hosts
are outside the MVP.

## Core Workflow

1. FX invokes one command with a macOS repository path.
2. The tool validates host prerequisites and reads optional shared and local
   `isolated-dev` configuration.
3. It creates or starts a stable, project-specific Container Machine.
4. It mounts the repository, idempotently provisions guest tooling, configures
   SSH-agent forwarding, and exposes declared ports to macOS.
5. It opens the mounted project in Zed's SSH remote-development mode.
6. FX explicitly starts project services from a Zed terminal or a declared
   project command; `up` does not automatically execute repository code.
7. Zed terminals, tasks, language servers, Git, tests, and builds execute in
   Linux. Browser traffic reaches guest services through localhost forwarding.
8. Stopping preserves the machine and project data. Destruction is an explicit,
   confirmed action.

## Functional Requirements

### Host CLI

- Implement the host-side tool in Go and distribute it as one self-contained
  Apple-silicon macOS binary.
- Require no host language runtime, package manager, Docker installation, or
  other project-development dependency.
- Build and test release binaries in CI or an isolated build environment rather
  than requiring Go on the developer's Mac.
- Report the CLI version and validate compatibility with the installed Apple
  Container CLI before mutating state.

### Project and Configuration

- Accept any existing macOS repository path; resolve and validate it before
  making changes.
- Derive a deterministic, collision-resistant machine name from the repository.
- Support an optional, committed `.isolated-dev.toml` for portable project
  settings such as the guest image, packages, bootstrap commands, mount target,
  and forwarded ports.
- Support an optional, Git-ignored `.isolated-dev.local.toml` for host-specific
  CPU, memory, and port overrides.
- Merge local overrides over shared configuration only for documented
  host-specific fields, and report the effective configuration through
  `status`.
- Provide useful defaults when no configuration exists.
- Do not auto-detect or start Docker Compose files during `up`.
- Allow configuration to declare explicit, opt-in project commands without
  running them implicitly.
- Reject invalid configuration with an actionable error before mutating state.
- Validate shared and local files independently so errors identify the exact
  file and field.
- Allow configuration to reference environment variable names or secret file
  paths, but reject inline secret values.
- Never place secrets in generated configuration or command output.

### Machine Lifecycle

- Provide idempotent `up` and `open` behavior for new, running, and stopped
  machines.
- Create each project machine from a shared, versioned base image maintained by
  `isolated-dev`.
- Record the base-image version in project-machine state and keep existing
  machines pinned when a newer version becomes available.
- Wait for guest readiness and retry the first command when Container Machine
  reports a transient startup error.
- Provide status, stop, upgrade, and destroy operations.
- Make `upgrade` show the current and target image versions, identify guest-only
  state that recreation may discard, and require confirmation before replacing
  a machine.
- Never recreate or upgrade an existing project machine automatically.
- Stop and destroy must terminate the project machine's managed SSH tunnel;
  repeated cleanup must be safe.
- Detect incompatible existing machine configuration and explain the required
  migration or recreation.
- Preserve guest packages, Docker images, Compose volumes, and project service
  data across stop/start cycles.
- Require confirmation before deleting a machine or persistent data.

### Source and Credentials

- Keep the repository on macOS and expose it read-write at a stable Linux path.
- Create a dedicated non-root guest user whose numeric UID and GID match the
  invoking macOS user.
- Preserve normal Git operations, executable bits, symlinks, and file-watch
  events on the mounted source tree without creating root-owned project files.
- Prefer exposing only the selected project and explicitly required paths.
- If Container Machine 1.x only supports a full-home mount, allow it for the
  single-user MVP after displaying a clear warning and recording the active
  mount scope in `status`.
- Keep all mounts generated by `isolated-dev` project-scoped. Existing
  repository Dockerfiles and Compose files remain developer-controlled.
- Leave existing ignored secret files, such as `.env`, in the mounted project;
  `isolated-dev` may check that a referenced file exists but must not parse,
  copy, persist, or print its contents.
- Run Git inside Linux and authenticate by forwarding the macOS SSH agent.
- Connect Zed and ordinary SSH sessions as the dedicated guest user with
  password authentication and direct root login disabled.
- Grant the guest user passwordless `sudo` and Docker-group membership for
  explicit administration and container workflows.
- Never copy private SSH keys into the machine.

### Guest Tooling and Compose

- Build and cache a shared Ubuntu 24.04 base image containing Docker Engine, the
  Docker Compose v2 plugin, Git, an SSH server, certificates, and common build
  utilities.
- Pin the base-image definition by version and report the version used by each
  project machine.
- Allow project-specific runtimes and CLIs to be installed through
  `.isolated-dev.toml`.
- Run existing Compose files and Dockerfiles without translation to Apple
  Container commands.
- Support Compose builds, private networks, health checks, bind mounts, named
  volumes, background services, and registry access.
- Verify Docker daemon readiness with `docker info` before invoking Compose;
  provisioning must not assume systemd is available after first boot.
- Make bootstrap repeatable and safe after partial failure.

### Zed and Networking

- Resolve the current machine address and configure a stable SSH connection for
  Zed.
- Add at most one idempotent `Include` directive to the developer-owned
  `~/.ssh/config`, pointing to a separate file managed exclusively by
  `isolated-dev`.
- Write managed SSH configuration atomically with restrictive permissions;
  never rewrite, reorder, or remove developer-owned SSH entries.
- Keep project-machine host keys in a tool-owned known-hosts file so machine
  recreation and address changes do not alter the developer's global file.
- Open the remote project from the host command without manual Zed setup.
- Create a managed background SSH tunnel that forwards configured guest ports
  to macOS loopback and remains active after the CLI exits.
- Reconcile the tunnel idempotently during `up` and `open`, including after a
  machine address changes.
- Report tunnel state and forwarding failures through `status`.
- Detect port conflicts and report the process or mapping involved.
- Preserve access to the guest terminal, tasks, language servers, and debugger
  through Zed.

### Forge Acceptance Workload

- Use the real `../forge` repository as the upper-bound acceptance workload.
- Start `docker compose --profile dev up -d` inside the machine without changing
  `forge/docker-compose.yml`.
- Build and run its PostgreSQL 16 database, FastAPI backend, Python worker, and
  React/Vite-to-Nginx frontend.
- Preserve its named database and application-data volumes across machine
  restart.
- Reach the frontend at macOS localhost port `3001` and the backend health
  endpoint through port `8001`.
- Support ordinary development commands and source edits from Zed against the
  mounted repository.

## Non-Functional Requirements

- **Isolation:** project-only executables and services must not be required on
  macOS; the existing host Docker installation must not be used.
- **Least privilege:** routine editor, shell, Git, build, and test operations
  run as the non-root guest user.
- **Safety:** operations must be scoped to a resolved project machine and must
  not silently delete machines, volumes, source, or credentials.
- **Repeatability:** rerunning a command must converge on the declared state.
- **Observability:** each phase must report concise progress and preserve useful
  diagnostics on failure without reading or leaking secret values.
- **Performance:** cached startup should connect Zed within 30 seconds and make
  the cached Forge DEV stack healthy within 2 minutes.
- **Maintainability:** begin CLI-first with minimal abstraction; add automation
  only for repeated operations demonstrated by the Forge workflow.
- **Portability:** the host CLI must be a single executable with no dynamically
  managed language runtime.

## Constraints and Dependencies

- Apple silicon and macOS 26 or newer.
- Apple Container CLI 1.x with `container machine` support.
- Go is the implementation language, but the Go toolchain is a build-time
  dependency only and is not required on the target Mac.
- Zed on macOS with SSH remote development and local port forwarding.
- Internet access for initial guest packages, Zed's remote server, OCI images,
  and Forge dependency builds.
- The validated host uses Apple Container CLI and API server 1.1.0 with the
  Kata ARM64 3.28.0 kernel bundle.
- Ubuntu 24.04 is the default guest base. Common tooling is built once and
  reused; project-specific dependencies remain inside the project machine.
- A full macOS home-directory mount is an accepted, warned fallback when the
  installed Container Machine API cannot create a repository-only mount.
- Fresh provisioning, image downloads, and the first Forge build are measured
  separately from cached startup.
- The MVP supports one local user and one machine per project.

## Validated Feasibility Findings

A smoke test on July 27, 2026 established the minimum nested-Compose baseline:

- An Ubuntu 24.04 Container Machine ran Docker Engine 29.1.3 and Docker Compose
  2.40.3 using the `overlay2` storage driver and cgroup v2.
- Compose ran inside the guest from the macOS-mounted repository and started
  `busybox:1.37` and `nginx:1.27-alpine` on a private Compose network.
- Nginx reached BusyBox by service name, and the response was accessible both
  inside the guest and from macOS through the published guest port.
- A macOS source file survived both mount layers and was served from the inner
  BusyBox container, proving basic nested bind-mount functionality.
- Container Machine 1.1.0 returned `Operation not supported by device` on the
  first remote command and left systemd unavailable. Starting `dockerd`
  directly allowed the test to pass, so startup recovery is an MVP requirement.

## Success Metrics

- A single command completes machine startup, repository mounting, guest
  provisioning, SSH/port setup, and Zed launch.
- A downloaded release binary runs on a supported clean Mac with only the
  documented Apple and Zed prerequisites installed.
- A fresh clone recreates the shared project environment from the committed
  configuration without carrying another Mac's local resource overrides.
- No project runtime, package manager, Docker daemon, database, or project CLI
  is required on macOS.
- No secret value is stored in `isolated-dev` configuration, state, logs, or
  generated files.
- Git fetch/pull from Linux succeeds through agent forwarding without guest key
  files.
- A Zed edit triggers expected file-watch and rebuild behavior in Linux.
- Files created from Zed, guest shells, and nested Compose bind mounts retain
  usable ownership from both macOS and Linux.
- Configured localhost ports remain reachable after the CLI exits and become
  unreachable after `stop`.
- `status` reports whether the machine uses repository-only or full-home host
  access.
- Forge's four DEV services become healthy and remain usable from the macOS
  browser.
- The two-image baseline Compose smoke test is automated and passes from a
  fresh machine before Forge is used as the full acceptance workload.
- Creating a project environment from an already-built base image does not
  reinstall common guest packages.
- Publishing a new base image does not alter an existing project machine;
  `upgrade` requires an explicit confirmation before recreation.
- Forge data survives stop/start, while explicit destroy removes the intended
  machine without touching source.
- Cached runs meet the 30-second Zed and 2-minute Forge readiness targets.

## Risks and Open Questions

- Container Machine is new and its CLI or configuration format may change.
- Binary signing, notarization, and macOS Gatekeeper behavior must be defined
  before distributing the tool beyond the initial user.
- Docker's basic cgroup v2, OverlayFS, networking, bind-mount, and published-port
  behavior is proven; Forge-specific kernel and networking behavior remains
  unverified.
- Container Machine 1.1.0 can fail its first remote command and terminate
  systemd, so the lifecycle flow requires a reliable readiness strategy or a
  daemon-start fallback.
- Forge may require `linux/amd64` execution for image build steps. Rosetta or
  binfmt behavior inside Docker running in a Container Machine is unverified.
- The accepted full-home fallback weakens filesystem isolation; generated
  mounts must remain project-scoped and the broader exposure must stay visible.
- Docker-group membership and passwordless `sudo` provide effective guest-root
  access; this is accepted inside the per-project VM but must not weaken host
  credential or filesystem boundaries.
- Two layers of mounting—macOS into the machine, then the machine path into
  Compose containers—may affect permissions, file events, and build speed.
- Services bound to guest loopback require reliable SSH forwarding to macOS.
- Machine IP changes must not leave stale tunnels, Zed SSH configuration, or
  host keys.
- SSH configuration updates must preserve valid existing configuration and
  remain recoverable if the process is interrupted.
- Base-image recreation can discard guest-only packages, images, volumes, and
  service data. The MVP must disclose this accurately and must not claim to
  migrate state it does not preserve.
- Default CPU, memory, and disk allocation remain to be selected after
  measuring Forge.

## Recommended Next Steps

1. Automate the versioned Ubuntu base-image build and proven machine, Docker,
   and baseline Compose flow with explicit readiness checks; then validate
   SSH-agent forwarding, Zed connection, stable localhost forwarding, file
   watching, and stop/start persistence.
2. Run the unmodified Forge DEV profile and record architecture compatibility,
   cold and warm timings, resource usage, and mount/file-watch behavior.
3. Use the Forge measurements to set default CPU, memory, and disk allocations
   and document the accepted mount and upgrade tradeoffs.
4. Use `autonomous-sdlc:create-epic` to split the validated requirements into
   implementation stories.
