# isolated-dev

`isolated-dev` is a macOS CLI for persistent, per-project Linux development
environments powered by Apple Container Machine. Project source stays on macOS;
toolchains, Docker Engine, Docker Compose, and application services run in the
guest.

It ships as one self-contained binary. See [REQUIREMENTS.md](REQUIREMENTS.md)
and [STORIES.md](STORIES.md) for scope and progress, and
[docs/releasing.md](docs/releasing.md) for how a release is built and what its
signing status is today.

## Requirements

On the Mac that runs `isolated-dev`:

- Apple silicon running macOS 26 or newer.
- [Apple Container](https://github.com/apple/container) 1.x, with its services
  started (`container system start`).
- An SSH key pair; `up` installs your public keys into the guest and refuses to
  create a machine without a usable one. Create one with `ssh-keygen -t ed25519`.
- [Zed](https://zed.dev) with its `zed` command on `PATH`, for `open` only.
  Install it from Zed's command palette with `cli: install cli`.
- Internet access on the first `up` of a base-image version, which downloads
  the Ubuntu packages and images the guest is built from.

Nothing else. The binary is statically linked against macOS system libraries
alone, so no Go toolchain, language runtime, package manager, or host Docker
installation is required — Docker Engine and Compose run inside the guest,
which is the point. The guest toolchain your project needs is declared in
`packages` and installed in the machine, not on macOS.

## Install

Download the Apple-silicon archive and its checksum from the
[releases page](https://github.com/fxmartin/isolated-dev/releases), verify it,
and put the binary on your `PATH`:

```sh
shasum -a 256 -c isolated-dev-<version>-darwin-arm64.tar.gz.sha256
tar -xzf isolated-dev-<version>-darwin-arm64.tar.gz
sudo install -d /usr/local/bin
sudo install -m 0755 isolated-dev /usr/local/bin/isolated-dev
isolated-dev --version
```

`/usr/local/bin` is root-owned on macOS and does not exist at all on a Mac
without Homebrew, which is why both `install` steps need `sudo`. To stay
unprivileged, install to `~/.local/bin` instead — `install -d ~/.local/bin &&
install -m 0755 isolated-dev ~/.local/bin/isolated-dev` — and make sure that
directory is on your `PATH`.

Releases are not yet signed or notarized, so macOS's quarantine attribute is
worth understanding. `tar -xzf` does **not** carry the attribute from the
archive onto the extracted binary, so the commands above run as written no
matter how you downloaded the archive. Finder is the case that differs: if you
extract by double-clicking the archive, Archive Utility applies
`com.apple.quarantine` to the binary, and Gatekeeper then refuses to run it and
reports that it cannot be verified. Remove the attribute deliberately in that
case, after you have verified the checksum above:

```sh
xattr -d com.apple.quarantine /usr/local/bin/isolated-dev
```

On the `tar` path that command exits non-zero with `No such xattr`, which means
there was nothing to remove — not a failed install.
[docs/releasing.md](docs/releasing.md) documents what signing and notarization
would require, and why neither is silently worked around here.

To build from source instead — which needs the Go toolchain, and only for the
build:

```sh
scripts/release.sh --version 0.0.0-local
```

## Commands

Every command takes the path of a Git repository and resolves it to one stable
project machine. All of them are safe to repeat: they reconcile toward the
declared state rather than assuming what they last left behind.

| Command | Effect |
| --- | --- |
| `isolated-dev up PROJECT` | Create or restart the machine and reconcile its image, resources, guest account, SSH host, and port tunnels. Starts no services. |
| `isolated-dev open PROJECT` | Everything `up` does, then open the mounted project in Zed over SSH. |
| `isolated-dev run PROJECT COMMAND` | Run one command declared in `[commands.<name>]` inside the guest. Nothing undeclared runs. |
| `isolated-dev status PROJECT` | Report configuration, machine, base image, mount, guest identity, SSH, and tunnel state. Read-only; changes nothing. |
| `isolated-dev stop PROJECT` | Stop the machine, preserving it and all persistent guest data. |
| `isolated-dev upgrade PROJECT` | Preview recreating the machine on a newer base image. Changes nothing. |
| `isolated-dev upgrade --yes PROJECT` | Recreate the machine on the configured base image, discarding guest-only state. |
| `isolated-dev destroy --yes PROJECT` | Remove the machine, its persistent data, its SSH host block, and local state. |
| `isolated-dev --version` | Print the version this binary was built from. |

A typical first session:

```sh
container system start
isolated-dev up ~/dev/app       # create the machine
isolated-dev status ~/dev/app   # confirm what was applied
isolated-dev open ~/dev/app     # edit it in Zed
isolated-dev run ~/dev/app dev  # start the declared DEV stack
isolated-dev stop ~/dev/app     # end of day; data survives
```

### Destructive operations

Two commands can destroy data, and neither ever acts on the bare verb. Both
require `--yes` in the same invocation; there is no interactive prompt, because
a prompt is not something an automated caller can be trusted to have answered.

- `destroy --yes` removes the project machine and everything that lives only
  inside it. It verifies both the canonical repository path and the derived
  machine name first, so it cannot delete a machine belonging to another
  project. Your macOS source is mounted, not copied, and is never touched.
- `upgrade --yes` recreates the machine on a new base image, which discards
  guest packages, Docker images and build cache, Compose volumes and their
  contents, and the guest home outside the mounted project. The bare
  `upgrade` preview lists exactly that before you commit to it, and every
  precondition is checked — and the target image built — before the existing
  machine is removed.

### Warnings you should expect

`up` reports these on standard error and continues; each names a condition on
the host that `isolated-dev` will not silently decide for you:

- `warning: this machine receives read-write access to your full home
  directory` — the Apple Container 1.x mount scope. Every `up` says so, because
  it is the security property most worth not forgetting. See
  [Project Machine Lifecycle](#project-machine-lifecycle).
- `warning: the mounted project at ... is not owned by ...` — files you create
  in Linux may not match your macOS ownership. See
  [Guest Identity and Credentials](#guest-identity-and-credentials).
- `warning: referenced secret file ... is not present in the project` — a
  `secrets.files` entry that is missing or cannot be checked. Its contents are
  never read either way.
- `warning: ...: macOS port ... is already in use` — that mapping is not
  forwarded; free the port and rerun `up`. See
  [Localhost Port Tunnels](#localhost-port-tunnels).

Some conditions are errors rather than warnings, and stop before any machine or
guest state changes: a repository outside your home directory, no usable SSH
public key, an unmanaged `base_image`, an invalid or unknown configuration key,
and an inline secret value.

## Configuration

Commit `.isolated-dev.toml` for portable project settings:

```toml
version = 1
base_image = "local/isolated-dev-base:1"
packages = ["nodejs"]

[resources]
cpus = 4
memory_gb = 8

[[ports]]
name = "web"
guest = 3000
host = 3000

[commands.dev]
args = ["docker", "compose", "--profile", "dev", "up", "-d"]
compose = true

[secrets]
environment = ["API_TOKEN"]
files = [".env"]
```

Each `[[ports]]` entry declares one forwarded mapping; see
[Localhost Port Tunnels](#localhost-port-tunnels). Names must be unique, and so
must `host` ports, because one macOS port can carry one forward.

Each `packages` entry names an Ubuntu package that `up` installs inside the
machine — the toolchain a plain Python, Go, or Node project needs without any
Compose involvement. The step is idempotent: packages already present are left
alone, so a repeated `up` is fast and needs no network; only the first install
of a package downloads anything. Installed packages survive `stop`, and
`upgrade --yes` recreates the machine through the same `up` path, so they are
reinstalled on the new base image automatically.

Use the Git-ignored `.isolated-dev.local.toml` only for local resources and host
port overrides:

```toml
[resources]
cpus = 6
memory_gb = 12

[[ports]]
name = "web"
host = 3100
```

A `[commands.<name>]` section declares a command; it never runs on its own. See
[Project Commands](#project-commands). `args` is the argument vector, with the
program to run first — no shell parses it. The optional `workdir` is a
project-relative directory the command runs in, and the optional `compose` flag
marks a command that needs the guest Docker daemon. Command names must be
usable as a single `isolated-dev run` argument.

Inline secret values and unknown fields are rejected before any host or guest
state changes. `secrets.environment` entries must be environment variable
names, and `secrets.files` entries must be project-relative paths that stay
inside the repository. During `up`, a referenced file is only checked for
existence, and one that is missing — or that cannot be checked at all, such as
a path nested under a regular file — is reported as a warning rather than
blocking machine creation; `isolated-dev` never opens, copies, or prints its
contents.

The full-home fallback is restricted to versioned
`local/isolated-dev-base:<version>` images maintained by `isolated-dev`;
repository configuration cannot substitute an external image with access to
the developer's home directory.

## Guest Base Image

The versioned `local/isolated-dev-base:1` image is built from
[`images/base/Dockerfile`](images/base/Dockerfile). It contains Ubuntu 24.04,
Docker Engine, Compose v2, Git, OpenSSH, and common utilities. Image inspection
is used as the cache key, so common packages are installed once rather than per
project.

Apple Container 1.1.0 can fail its first machine command while the guest
restarts. Docker readiness is retried for 30 seconds before the embedded direct
daemon fallback is used.

## Project Machine Lifecycle

Create or restart the stable machine derived from a Git repository:

```sh
isolated-dev up /path/to/repository
```

`up` reconciles the machine, the guest account, and SSH access, and stops
there: it never inspects the repository for services to start. Project commands
are explicit — see [Project Commands](#project-commands).

The current Apple Container 1.x integration uses a read-write full-home mount.
The active `home` mount scope is recorded in project state and reported by
`status`. Existing machines remain pinned to their original image and resource
settings; change those settings only by explicitly destroying and recreating
the machine. Because Apple Container Machine 1.1.0 cannot mount an arbitrary
host path, `up` rejects repositories outside the canonical home directory
before changing lifecycle state.

Apple Container Machine 1.1.0 does not expose disk allocation during machine
creation, so `disk_gb` is not a supported configuration field and status does
not present a disk size as applied or pinned. For the same reason the optional
`mount_target` key is accepted and validated but not yet applied: the home mount
keeps the repository at its host path inside the guest.

## Guest Identity and Credentials

Every `up` idempotently provisions a dedicated non-root guest account whose
numeric UID and GID match the invoking macOS user, so files created from Zed,
guest shells, and nested Compose bind mounts keep usable ownership on both
sides of the mount. The macOS user name is lowercased and reduced to a valid
Linux login name; `up` refuses to provision root.

The guest user receives passwordless `sudo` and Docker-group membership for
explicit administration and container workflows. Its password is locked, and
the managed `/etc/ssh/sshd_config.d/10-isolated-dev.conf` drop-in disables
password authentication and direct root login, restricts SSH to the guest user,
and enables agent forwarding so Git authenticates from Linux through the macOS
SSH agent.

Only public keys ever reach the machine. `up` collects `~/.ssh/*.pub` on macOS,
rejects any file containing private key material, and installs the result as
the guest user's `authorized_keys`. Entries it cannot use as login keys — an
SSH-CA certificate, for example, which OpenSSH requires to sit beside the key
it certifies — are skipped, leaving the valid keys next to them intact. If no
usable public key exists, `up` stops before creating or starting a machine:

```sh
ssh-keygen -t ed25519
```

`up` prints the provisioned identity and the Linux path at which the repository
is readable and writable, and warns when the mounted project does not carry the
guest user's ownership:

```
created /Users/fx/dev/app
guest fx (501:20) at /Users/fx/dev/app
```

Both values are recorded in project state and reported by `status`. Machines
created before guest provisioning existed report `not-provisioned` until the
next `up`.

The repository is located inside the guest before the guest user is configured.
Provisioning owns `/home/<user>` — it sets that directory's mode and writes the
guest `authorized_keys` — so if the home mount ever exposed the macOS home at
that same path, `up` stops instead of overwriting the developer's own
`~/.ssh/authorized_keys`.

Stopping preserves the machine and its persistent guest data:

```sh
isolated-dev stop /path/to/repository
```

Destruction verifies both the canonical repository path and its
collision-resistant derived machine name before removing that machine and local
lifecycle state. It requires an explicit confirmation flag:

```sh
isolated-dev destroy --yes /path/to/repository
```

## Zed and SSH Access

Every `up` also reconciles the SSH host that Zed connects through, and reports
it alongside the guest identity:

```
created /Users/fx/dev/app
guest fx (501:20) at /Users/fx/dev/app
ssh isolated-dev-app-abcd1234 (fx@192.168.64.5)
```

The alias is the project machine name, so `ssh isolated-dev-app-abcd1234` works
from any terminal. Apple Container Machine 1.1.0 does not report a machine
address, so it is read from inside the guest after provisioning starts `sshd`;
addresses on the Docker bridges the guest runs itself are ignored because macOS
cannot reach them.

Everything `isolated-dev` generates lives in `~/.ssh/isolated-dev/`:
`config` holds one host block per project machine, and `known_hosts` holds their
host keys. Your `~/.ssh/config` receives a single `Include` directive, added
once at the top; no developer-owned entry is ever rewritten, reordered, or
removed. Both files are written atomically with `0600` permissions. Because the
host block pins `HostKeyAlias` to the machine name, a machine that comes back at
a different address keeps working, and a recreated machine has its stale host
key dropped before the first connection. `destroy` removes the machine's host
block and host keys; repeating it is safe.

`open` reconciles the machine, its guest identity, and its SSH host, then opens
the mounted project in Zed's SSH remote-development mode:

```sh
isolated-dev open /path/to/repository
```

```
ready /Users/fx/dev/app
guest fx (501:20) at /Users/fx/dev/app
ssh isolated-dev-app-abcd1234 (fx@192.168.64.5)
opening /Users/fx/dev/app in Zed over isolated-dev-app-abcd1234
```

A stopped machine, a machine that moved, and a machine that was never created
all reach the same state, so `open` needs no separate `up`. It requires Zed's
`zed` command on `PATH`; install it from Zed's command palette with
`cli: install cli`.

`status` reports the managed host, and `not-configured` until the first `up`
has established one:

```
SSH: isolated-dev-app-abcd1234 (fx@192.168.64.5)
```

## Localhost Port Tunnels

Every `up` and `open` reconciles a single background SSH tunnel that binds the
declared `[[ports]]` to macOS loopback, and reports it after the SSH host it
connects through:

```
ready /Users/fx/dev/app
guest fx (501:20) at /Users/fx/dev/app
ssh isolated-dev-app-abcd1234 (fx@192.168.64.5)
tunnel pid 4242 (web localhost:3001 -> guest:3000, api localhost:8001 -> guest:8000)
```

The tunnel runs in a session of its own, with no stream attached to the command
that started it, so `http://localhost:3001` keeps working after `isolated-dev`
exits and after Zed is closed — the machine is what has to stay running, not the
CLI or the editor. Each forward binds `127.0.0.1` only, so guest services are
never published to the local network. The target is the managed host alias, so
the address, the guest account, and the tool-owned host keys all come from the
SSH configuration `up` maintains.

Reconciliation is idempotent and never leaves two processes on the same ports.
A tunnel that already forwards exactly the declared mappings from the machine's
current address is left running; a machine that moved to a new address, a
machine that was recreated by `upgrade`, a changed `[[ports]]` list, and a
tunnel whose process died all replace the recorded tunnel with exactly one new
one. Each machine's tunnel is recorded
under `~/Library/Application Support/isolated-dev/tunnels/`, and the recorded
process is only ever signalled while its command line still identifies it as
that machine's tunnel, so a reused process ID is never mistaken for one.

A macOS port that something else already listens on is reported rather than
seized. The existing listener keeps its socket, the remaining ports are still
forwarded, and the conflict is recorded so `status` explains it later:

```
warning: web: macOS port 3001 is already in use, so guest port 3000 is not forwarded; free the port and rerun up
```

```
Tunnel: running (pid 4242): api localhost:8001 -> guest:8000; web not forwarded (macOS port 3001 in use)
```

`status` otherwise reports the live tunnel, or `stopped` when no process is
running:

```
Tunnel: running (pid 4242): web localhost:3001 -> guest:3000
```

`stop` and `destroy` terminate the tunnel once the machine operation succeeds,
and leave it alone when the machine survives the attempt. Cleanup waits for the
process to release its ports, and repeating it is safe.

## Project Commands

`isolated-dev` never runs repository code on its own. `up`, `open`, `stop`,
`upgrade`, and `destroy` do not look for a `docker-compose.yml`, a task-runner
manifest, or a script directory, and they never start project services. A
command exists only because `.isolated-dev.toml` declares it, and it runs only
when you invoke it by that name:

```toml
[commands.dev]
args = ["docker", "compose", "--profile", "dev", "up", "-d"]
compose = true

[commands.test]
args = ["npm", "test"]
workdir = "services/api"
```

```sh
isolated-dev run /path/to/repository dev
```

The command runs inside the project machine as the provisioned non-root guest
user, in the mounted project — or in the declared `workdir` beneath it. Its
`args` are passed as a literal argument vector, so no shell on either side
re-interprets them. Output streams to your terminal as it is produced, standard
input is forwarded, and the command's exit status becomes `isolated-dev`'s own,
so `run` composes with scripts and `&&` exactly as the underlying command
would.

A command declared with `compose = true` needs the guest Docker daemon, so
`docker info` must succeed inside the machine before it starts. Readiness is
retried, and the direct-daemon fallback is used if the daemon has not come up
on its own. If Docker never answers, nothing is executed and the failure names
the machine and how to inspect the daemon:

```
run: Docker is not ready in machine "isolated-dev-app-abcd1234": `docker info` did not
succeed, so "dev" was not run; check the daemon with `ssh isolated-dev-app-abcd1234 sudo docker info`
```

A name the project does not declare is rejected before anything runs, and the
rejection lists what the project does offer:

```
run: command "deploy" is not declared by this project; declared commands: dev, test
```

`run` needs a machine, so it reports which command to use when none exists yet:

```
run: no project machine exists for /Users/fx/dev/app; run `isolated-dev up /Users/fx/dev/app` first
```

## Base-Image Upgrades

A machine keeps running the base image it was created from. Pointing
`base_image` at a newer version in `.isolated-dev.toml` never migrates an
existing machine on its own; `status` reports the newer image as available
while the machine stays pinned:

```
Base image: local/isolated-dev-base:1 (pinned; local/isolated-dev-base:2 available, run upgrade)
```

Apple Container Machine cannot move a machine between base images, so an
upgrade recreates it. A bare `upgrade` is the preview: it changes nothing and
reports the current and target versions plus the guest-only state a recreation
would discard.

```sh
isolated-dev upgrade /path/to/repository
```

```
Upgrade: /Users/fx/dev/app
Machine: isolated-dev-app-abcd1234
Current base image: local/isolated-dev-base:1 (version 1)
Target base image: local/isolated-dev-base:2 (version 2)
Recreating the machine discards state that exists only inside it:
  - guest packages installed after provisioning
  - Docker images and build cache
  - Docker Compose volumes and the data inside them
  - guest home directory contents outside the mounted project
  - guest-only data such as databases, shell history, and tool caches
Preserved: the macOS project source at /Users/fx/dev/app is mounted, not copied.
No changes made. Re-run with --yes to recreate isolated-dev-app-abcd1234 on local/isolated-dev-base:2.
```

Declining the preview — that is, simply not re-running it — leaves the machine,
its tunnel, and its persistent state untouched. Recreation happens only with an
explicit confirmation flag:

```sh
isolated-dev upgrade --yes /path/to/repository
```

Every precondition that `up` enforces — a managed base image, a repository
inside the home mount, a resolvable guest identity, and a usable public key —
is checked before the existing machine is destroyed, and the target base image
is built first as well, so an offline host or a broken image build fails while
the machine and its guest-only data are still intact. A rejected upgrade never
leaves you without a machine. The replacement is created through the
ordinary `up` path, so mount, identity, SSH, and tunnel reconciliation behave
exactly as they do for any other machine. If the configured image already
matches the pinned one, `upgrade` reports that and does nothing, even with
`--yes`.

## Development

```sh
go test ./...
go vet ./...
go run ./cmd/isolated-dev --version
go run ./cmd/isolated-dev status .
```

[`scripts/release.sh`](scripts/release.sh) runs those checks and then builds,
verifies, and packages the distributable binary. CI runs the same script, so a
release is reproducible locally; `--dry-run` prints the steps it would take:

```sh
scripts/release.sh --dry-run
scripts/release.sh --version 0.0.0-local
```

See [docs/releasing.md](docs/releasing.md) for cutting a release and for the
signing, notarization, and Gatekeeper requirements.

Run the destructive host-backed lifecycle test only on a disposable Apple
Container development host. It creates and removes one uniquely named project
machine and verifies package, image, Compose-volume, guest, and mounted-project
data across stop and restart:

```sh
ISOLATED_DEV_RUN_HOST_TESTS=1 go test ./internal/machine \
  -run TestHostLifecyclePersistsProjectMachineData -count=1 -v
```

The equally destructive guest test creates one machine, provisions it twice to
prove convergence, and verifies guest ownership, `sudo`, Docker membership,
SSH policy, and that no private key material reaches the machine:

```sh
ISOLATED_DEV_RUN_HOST_TESTS=1 go test ./internal/guest \
  -run TestHostProvisionsGuestIdentityAndCredentials -count=1 -v
```

The SSH test is destructive in the same way. It creates one machine, configures
the managed host with a throwaway key, and connects through the developer-facing
configuration — the path Zed takes — to verify login, a writable mounted
project, agent forwarding, and tool-owned host keys:

```sh
ISOLATED_DEV_RUN_HOST_TESTS=1 go test ./internal/sshconfig \
  -run TestHostConnectsOverManagedSSH -count=1 -v
```

The project-command test is destructive in the same way. It creates one
machine, provisions it, and runs declared commands through the path `run`
takes, verifying the non-root guest user, the mounted working directory, the
declared `workdir`, preserved output and exit status, and a Compose command
after `docker info` succeeds:

```sh
ISOLATED_DEV_RUN_HOST_TESTS=1 go test ./internal/projectcmd \
  -run TestHostRunsDeclaredCommandsInTheGuest -count=1 -v
```

The baseline nested-Compose test is the automated proof that Docker still runs
inside an Apple Container Machine, and it should pass before any real workload
is used as an acceptance test. It builds a base image of its own, creates one
fresh machine from it, starts pinned `busybox:1.37` and `nginx:1.27-alpine`
images on a private Compose network, and reads a macOS-authored marker file
back both from inside the guest and from macOS through the published guest
port — which exercises Compose, service-name networking, and both mount layers
at once. Whether it passes or fails it removes its own containers, network,
machine, base image, and temporary fixtures, and it touches nothing else: it
refuses to run on the shared `local/isolated-dev-base:1` version precisely
because teardown deletes the image it builds. A failing step first captures the
machine list, `systemctl is-system-running`, `docker info`, and the Compose
state and logs, which is what makes the known Apple Container 1.1.0 startup
race — a terminated systemd and an absent Docker daemon — visible rather than
just a timeout. That race is also what the readiness fallback recovers from:
`dockerd` is started directly, and readiness is confirmed again immediately
before Compose runs. It is destructive and slow, so run it only on a disposable
Apple Container development host:

```sh
ISOLATED_DEV_RUN_HOST_TESTS=1 go test ./internal/smoke \
  -run TestHostRunsTheBaselineNestedComposeWorkload -count=1 -v -timeout 40m
```

The Forge DEV acceptance run is the MVP's upper-bound workload. It needs the
`../forge` repository to declare its DEV command and its two forwarded ports in
its own `.isolated-dev.toml`:

```toml
[[ports]]
name = "frontend"
guest = 3001
host = 3001

[[ports]]
name = "backend"
guest = 8001
host = 8001

[commands.dev]
args = ["docker", "compose", "--profile", "dev", "up", "-d"]
compose = true
```

The declared command must be exactly that argument vector, with no added
`--file` and no `workdir`, so what starts is the repository's own Compose file
and nothing else; the run digests that file before and after, which makes
"unmodified" checked rather than assumed. It reconciles the project machine with
`up`, invokes the declared command through the same path as `isolated-dev run`,
waits for PostgreSQL 16, the FastAPI backend, the Python worker, and the
React/Vite-to-Nginx frontend to be running and healthy, and then reads the
frontend at `http://127.0.0.1:3001/` and the backend health endpoint at
`http://127.0.0.1:8001/health` from macOS through the managed tunnel.

Unlike the baseline test it creates nothing and removes nothing: Forge is a real
repository with real named volumes, and its DEV stack is meant to keep running
afterwards. A failure captures the machine list, `docker info`, and the Compose
state and logs, and when that output shows an architecture incompatibility the
result names the affected image or build step and whether `linux/amd64` image
selection, Rosetta, or a binfmt handler is what the guest is missing:

```
architecture incompatibility: image postgres:16-alpine; linux/amd64 support is
required: the image is published without a linux/arm64 variant, so it can only
run as linux/amd64, which needs Rosetta or a binfmt handler registered inside
the machine
```

Set `ISOLATED_DEV_FORGE_PATH` when the repository is not at `../forge`. The
first run builds the Forge images inside the machine, so allow for it:

```sh
ISOLATED_DEV_RUN_HOST_TESTS=1 go test ./internal/forge \
  -run TestHostRunsTheUnmodifiedForgeDevStack -count=1 -v -timeout 70m
```

The Forge persistence run proves the day after startup rather than startup
itself. It brings the DEV stack up, then stops and restarts the project machine
through `stop` and `up` and checks that:

- the `rosetta-db-dev` and `rosetta-data-dev` named volumes come back as the
  same volumes — same driver, mount point, and Docker creation timestamp — still
  holding everything they held before, so a restart never silently reinitialises
  the database;
- macOS ports 3001 and 8001 answer while no CLI command is running, refuse
  connections once the machine is stopped, and answer again through the tunnel
  the restart reconciles, whatever address the machine came back on. Only a
  refused connection counts as released: a port that answers anything at all,
  including an error status, is still held by something;
- an edit macOS writes into the mounted repository is readable in Linux as the
  provisioned guest user, a file that user creates is readable from macOS, and
  both carry the developer's own ownership on both sides;
- the cached restart meets the 30-second machine and 2-minute stack readiness
  targets. A missed target is reported as a finding about the host rather than
  as a broken environment, and the test names it.

Like the acceptance run it owns nothing and removes nothing: the machine, the
containers, and the named volumes holding real data are left as they were found.
The only things it writes into the repository are two marker files, which it
refuses to create if the names are already taken and removes from both sides of
the mount on every path, including a failing one. It restarts the Forge DEV
stack, so run it only where that is what you want to happen — and a run that
fails between `stop` and `up` leaves the machine stopped, which it says, naming
the `isolated-dev up` that brings the stack back:

```sh
ISOLATED_DEV_RUN_HOST_TESTS=1 go test ./internal/forge \
  -run TestHostKeepsForgeUsableAcrossARestart -count=1 -v -timeout 100m
```

The Zed check is not destructive and needs no machine. It resolves the real
`zed` CLI the way `open` does, confirms the installed build is invocable, and
verifies the `ssh://` target decodes back to exactly the managed alias and guest
project path. It stops before launching a window, which no unattended run can
assert on:

```sh
ISOLATED_DEV_RUN_HOST_TESTS=1 go test ./internal/zed \
  -run TestHostZedCLIOpensTheManagedTarget -count=1 -v
```

`status` is read-only. It validates the canonical Git repository path and host
prerequisites, then reports effective resources, the guest identity and mounted
project path, and the project machine, base-image, mount, SSH, and tunnel state
without displaying secret references.
