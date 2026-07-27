# Story Index

## Epic 01: Isolated Dev MVP

- **Status:** Planned
- **Stories:** 13
- **Total points:** 67
- **Risk:** High
- **Epic:** [docs/stories/epic-01-isolated-dev-mvp.md](docs/stories/epic-01-isolated-dev-mvp.md)

| Story | Title | Points | Risk | Dependencies |
| --- | --- | ---: | --- | --- |
| 01.1 | Establish the Go CLI and Configuration Model | 5 | Medium | None |
| 01.2 | Validate Host Prerequisites and Track Project State | 3 | Low | 01.1 |
| 01.3 | Build and Cache the Versioned Guest Base Image | 5 | High | 01.1-01.2 |
| 01.4 | Implement Safe Project-Machine Lifecycle | 8 | High | 01.2-01.3 |
| 01.5 | Configure Guest Identity, Mounts, and Credentials | 5 | High | 01.3-01.4 |
| 01.6 | Integrate Managed SSH Access with Zed | 5 | Medium | 01.4-01.5 |
| 01.7 | Manage Persistent Localhost Port Tunnels | 5 | Medium | 01.4, 01.6 |
| 01.8 | Execute Only Explicit Project Commands | 5 | Medium | 01.1, 01.4-01.5 |
| 01.9 | Provide Explicit Base-Image Upgrades | 5 | High | 01.3-01.4 |
| 01.10 | Automate the Baseline Nested-Compose Test | 5 | High | 01.3-01.5, 01.8 |
| 01.11 | Run the Unmodified Forge DEV Stack | 8 | High | 01.5-01.8, 01.10 |
| 01.12 | Validate Forge Persistence and Development Experience | 5 | High | 01.6-01.7, 01.11 |
| 01.13 | Package and Document the MVP | 3 | Medium | 01.1-01.12 |
