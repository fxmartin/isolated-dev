# isolated-dev

`isolated-dev` is a macOS CLI for persistent, per-project Linux development
environments powered by Apple Container Machine. Project source stays on macOS;
toolchains, Docker Engine, Docker Compose, and application services run in the
guest.

The MVP is under active development. See [REQUIREMENTS.md](REQUIREMENTS.md) and
[STORIES.md](STORIES.md) for scope and progress.

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
