# Isolated Dev

## Objective

Explore whether Apple Container's `container machine` can provide fast,
isolated, reproducible Linux development environments for local web
application work on macOS.

The project should establish where container machines improve on the current
local-development workflow, where they fall short, and what a practical
per-project workflow would look like.

## Initial Scope

- Evaluate a persistent Linux machine dedicated to each web project.
- Exercise representative web-development needs: package installation,
  source sharing, file watching, networking, ports, databases, background
  services, editor and terminal integration, and teardown/recreation.
- Test at least one representative TypeScript application and one Python
  application.
- Compare the experience with a conventional OCI container workflow and the
  current macOS-native workflow.
- Capture setup steps, limitations, performance observations, and a
  recommendation.

## Proposed Stack

- macOS 26+ on Apple silicon
- Apple Container CLI 1.x and `container machine`
- OCI-compatible Linux images
- Shell scripts for repeatable experiments
- TypeScript/Node.js and Python web applications as representative workloads

## Architecture Hypothesis

Use one persistent container machine per project, with the source repository
available inside the guest and language runtimes, package caches, databases,
and supporting services isolated from macOS.

Keep the first iteration CLI-first and configuration-light. Add wrapper
scripts or project manifests only after the experiments reveal repeated,
stable operations worth automating.

## Questions to Resolve

- How reproducible is machine creation across projects and developers?
- How do source mounts and file watchers behave under real web workloads?
- How are host-to-guest and guest-to-guest networking exposed?
- Can editors, debuggers, browser workflows, and Git credentials integrate
  cleanly without weakening isolation?
- What are the CPU, memory, disk, startup-time, and battery tradeoffs?
- How should secrets, package caches, and persistent service data be handled?
- Which workflows still require Docker-compatible orchestration or another
  local runtime?

## Constraints

- Favor native Apple tooling and standard OCI artifacts.
- Do not couple application code to the local environment.
- Keep experiments removable and avoid modifying global developer tooling
  unless necessary.
- Treat Apple Container CLI 1.x behavior and limitations as evolving until
  validated against the installed version.

## First Follow-on

Run a focused product and workflow discovery session, then turn the resulting
requirements into a small experiment matrix with measurable success criteria.
