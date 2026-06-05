# Repository Guidelines

This file is for coding agents working in this repository. Claude-specific
guidance lives in `CLAUDE.md`; Splus review preferences live in `SPLUS.md`.

## Project Shape

Greenrun is a local GitHub Actions runtime. It reads `.github/workflows`,
builds a realistic event and isolated synthetic commit, runs supported Ubuntu
jobs through Docker/act, and writes compact evidence for humans and agents.

GitHub Actions YAML is the only pipeline definition. Do not add
repository-specific Greenrun configuration.

## Important Boundaries

- Greenrun makes no model calls.
- Do not read `.env` files implicitly.
- Do not retrieve or pass a GitHub token for workflow execution unless the user
  explicitly supplied it as a secret.
- Unsupported GitHub-only jobs should be reported honestly as `remote_only` or
  `partial`, not treated as passed.
- Preserve the distinction between runtime errors, repo CI failures, blocked
  jobs, and remote-only jobs.

## Repo Map

- `cmd/greenrun`: CLI entrypoint.
- `internal/cli`: command routing, run orchestration, persisted results.
- `internal/repo`: repository discovery, default branch/base, changed files.
- `internal/event`: inferred or explicit GitHub event payloads.
- `internal/snapshot`: isolated synthetic checkout creation.
- `internal/workflow`: actionlint, trigger filtering, job classification.
- `internal/engine`: Docker/act execution, logs, cache, artifacts.
- `internal/output`: compact and plain output formatting.
- `internal/githubrun`: hosted Actions run import.
- `schema`: canonical result schema.
- `docs`: architecture and compatibility notes.
- `images`: runner image definitions.

## Commands

```sh
go test ./...
go vet ./...
test -z "$(gofmt -l .)"
go build ./cmd/greenrun
```

Use `GREENRUN_IMAGE_22` and `GREENRUN_IMAGE_24` when testing development
runner images.

## Development Notes

- Use `gofmt` for Go edits.
- Keep tests deterministic and local; prefer tempdirs and fixture repositories.
- Avoid destructive git commands in tests or implementation.
- Keep changes narrowly scoped. Greenrun's value is faithful evidence, not a
  new CI DSL.
- When a change affects event inference, snapshots, workflow filtering, status
  classification, or output schema, add or update tests.
