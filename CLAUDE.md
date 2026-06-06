# CLAUDE.md

This file provides guidance to Claude Code when working in this repository.
Other coding agents should also read `AGENTS.md`. Review agents should read
`SPLUS.md`.

## What Greenrun Is

Greenrun fails GitHub Actions locally as fast as possible before a push. It
derives behavior from existing GitHub Actions workflows, creates an isolated
synthetic checkout, executes supported Ubuntu jobs in Docker, and saves compact
evidence under `~/.greenrun`.

Do not introduce repository-specific Greenrun configuration. The workflows in
`.github/workflows` remain the source of truth.

## Core Invariants

- No model calls.
- No implicit `.env` loading.
- No implicit GitHub token retrieval for workflow execution.
- Secrets are explicit and must remain masked in persisted and live logs.
- Unsupported GitHub infrastructure is `remote_only` or `partial`, never a fake
  pass.
- Fidelity labels must be honest: `exact`, `compatible`, `approximate`, and
  `remote_only` mean different things.

## Architecture

- `internal/repo`, `internal/snapshot`, and `internal/event` derive a realistic
  local event without mutating the developer's checkout.
- `internal/workflow` validates workflows, applies event/path/branch filters,
  classifies runners and secrets, and builds the execution plan.
- `internal/engine` is the adapter to `nektos/act`, Docker, caches, artifacts,
  logs, and cancellation.
- `internal/store`, `internal/output`, and `schema` define persisted evidence
  and agent-readable results.
- `internal/githubrun` imports hosted GitHub Actions runs into the same result
  model.

## Common Commands

```sh
go test ./...
go vet ./...
test -z "$(gofmt -l .)"
go build ./cmd/greenrun
```

For local manual checks:

```sh
go run ./cmd/greenrun --plain
go run ./cmd/greenrun plan
go run ./cmd/greenrun show latest
go run ./cmd/greenrun logs latest --failed
```

## Implementation Guidance

- Prefer structured workflow parsing and existing helpers over ad hoc string
  handling.
- Keep synthetic checkouts self-contained for Docker containers.
- Tests should use tempdirs, fixture repos, and local state. Do not rely on live
  GitHub state unless a test explicitly covers hosted-run import behavior.
- If a change touches event inference, changed-file logic, trigger filtering,
  runner classification, log masking, or result status, add focused tests.
- For docs-only edits, tests may be unnecessary; say so when reporting.
