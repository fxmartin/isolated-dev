# Repository Guidelines

## Project Structure & Module Organization

This repository is currently a documentation-first technical spike. The root
contains `PROJECT-SEED.md`, which defines the objective and experiment scope,
and `.gitignore`, which excludes local configuration and generated results.

As implementation grows, place repeatable host-side automation in `scripts/`,
representative web workloads in `experiments/<stack>/`, and automated checks in
`tests/`. Keep generated benchmarks and logs under `.artifacts/`; never commit
them. Prefer small, independent experiments over a shared framework until a
stable workflow emerges.

## Build, Test, and Development Commands

No project build system or dependency manifest exists yet. Document new
commands alongside the code that introduces them. Useful baseline commands are:

- `container system start` — start Apple Container services.
- `container machine ls` — inspect available persistent Linux environments.
- `git diff --check` — detect whitespace errors before committing.

Automation must be non-interactive where practical and safe to rerun. Provide a
short usage example in the script header or adjacent README.

## Coding Style & Naming Conventions

Use two spaces for Markdown, YAML, and JSON indentation; use four spaces for
Python. TypeScript should use the formatter and lint configuration committed
with its experiment. Format Python with Ruff when Python tooling is introduced.
Shell scripts must start with `#!/usr/bin/env bash`, enable strict error
handling, and pass ShellCheck.

Name scripts and directories with lowercase kebab-case, such as
`scripts/create-machine.sh`. Use descriptive environment variables in
`UPPER_SNAKE_CASE`.

## Testing Guidelines

Write a failing test or reproducible check before changing observable behavior.
Each experiment should test machine creation, application startup, host access,
and clean teardown. Name Python tests `test_<behavior>.py` and TypeScript tests
`<behavior>.test.ts`. Add the narrowest test command to the experiment README;
do not claim coverage that was not measured.

## Commit & Pull Request Guidelines

There is no commit history from which to infer a convention. Use short,
imperative commit subjects, optionally with a focused prefix, for example
`docs: define networking experiment`.

Pull requests must explain the hypothesis tested, commands run, observed
results, and known limitations. Link relevant issues and include benchmark
data or screenshots only when they help reviewers verify behavior. Never
commit secrets, `.env` files, credentials, or machine-specific paths.
