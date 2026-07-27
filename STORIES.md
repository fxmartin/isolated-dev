# Story Index

## Epic 01: Isolated Dev MVP

- **Status:** Planned
- **Stories:** 13
- **Total points:** 67
- **Risk:** High
- **Epic:** [docs/stories/epic-01-isolated-dev-mvp.md](docs/stories/epic-01-isolated-dev-mvp.md)

| Story | Title | Points | Risk | Dependencies |
| --- | --- | ---: | --- | --- |
| 01.1-001 | Establish the Go CLI and Configuration Model | 5 | Medium | None |
| 01.2-001 | Validate Host Prerequisites and Track Project State | 3 | Low | 01.1-001 |
| 01.3-001 | Build and Cache the Versioned Guest Base Image | 5 | High | 01.1-001, 01.2-001 |
| 01.4-001 | Implement Safe Project-Machine Lifecycle | 8 | High | 01.2-001, 01.3-001 |
| 01.5-001 | Configure Guest Identity, Mounts, and Credentials | 5 | High | 01.3-001, 01.4-001 |
| 01.6-001 | Integrate Managed SSH Access with Zed | 5 | Medium | 01.4-001, 01.5-001 |
| 01.7-001 | Manage Persistent Localhost Port Tunnels | 5 | Medium | 01.4-001, 01.6-001 |
| 01.8-001 | Execute Only Explicit Project Commands | 5 | Medium | 01.1-001, 01.4-001, 01.5-001 |
| 01.9-001 | Provide Explicit Base-Image Upgrades | 5 | High | 01.3-001, 01.4-001 |
| 01.10-001 | Automate the Baseline Nested-Compose Test | 5 | High | 01.3-001, 01.4-001, 01.5-001, 01.8-001 |
| 01.11-001 | Run the Unmodified Forge DEV Stack | 8 | High | 01.5-001, 01.6-001, 01.7-001, 01.8-001, 01.10-001 |
| 01.12-001 | Validate Forge Persistence and Development Experience | 5 | High | 01.6-001, 01.7-001, 01.11-001 |
| 01.13-001 | Package and Document the MVP | 3 | Medium | 01.1-001 through 01.12-001 |
