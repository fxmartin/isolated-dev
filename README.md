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
mount_target = "/workspace"
packages = ["nodejs"]

[resources]
cpus = 4
memory_gb = 8
disk_gb = 64

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
state changes.

## Guest Base Image

The versioned `local/isolated-dev-base:1` image is built from
[`images/base/Dockerfile`](images/base/Dockerfile). It contains Ubuntu 24.04,
Docker Engine, Compose v2, Git, OpenSSH, and common utilities. Image inspection
is used as the cache key, so common packages are installed once rather than per
project.

Apple Container 1.1.0 can fail its first machine command while the guest
restarts. Docker readiness is retried for 30 seconds before the embedded direct
daemon fallback is used.

## Development

```sh
go test ./...
go vet ./...
go run ./cmd/isolated-dev --version
go run ./cmd/isolated-dev status .
```

`status` is read-only. It validates the canonical Git repository path and host
prerequisites, then reports effective resources and the project machine,
base-image, mount, and tunnel state without displaying secret references.
