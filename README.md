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

Inline secret values and unknown fields are rejected before any host or guest
state changes. `secrets.environment` entries must be environment variable
names, and `secrets.files` entries must be project-relative paths that stay
inside the repository. During `up`, a referenced file is only checked for
existence and a missing one is reported as a warning; `isolated-dev` never
opens, copies, or prints its contents.

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

`status` is read-only. It validates the canonical Git repository path and host
prerequisites, then reports effective resources, the guest identity and mounted
project path, and the project machine, base-image, mount, and tunnel state
without displaying secret references.
