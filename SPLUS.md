# SPLUS.md - Greenrun review contract

This contract is read first on every Splus review. Review only grounded,
diff-scoped findings; a wrong comment costs more than a missed nit.

## Policy

- Precision first. Lead with the highest-risk behavioral regression, not style.
- Be explicit about fidelity. Never describe approximate local execution as
  GitHub-exact, and never describe `remote_only` or `partial` as green.
- Treat Greenrun as infrastructure. Bugs in event inference, snapshots,
  checkout shape, Docker execution, masking, or result classification can make
  users trust the wrong signal.
- No hidden configuration. Workflow behavior must derive from GitHub Actions
  YAML, not repository-specific Greenrun config files.

## High-Scrutiny Areas

- `internal/repo/**`, `internal/event/**`, and `internal/snapshot/**`:
  branch/base detection, PR metadata, changed-file computation, synthetic
  commits, remotes, object availability, and dirty worktree handling.
- `internal/workflow/**`: trigger matching, branch/path filters, runner
  classification, secret detection, dependency blocking, and fidelity labels.
- `internal/engine/**`: Docker/act wiring, runner images, cancellation,
  artifact/cache compatibility servers, log collection, and secret masking.
- `internal/output/**`, `internal/store/**`, and `schema/**`: stable compact
  output, canonical JSON, exit status mapping, and agent-readable evidence.
- `install.sh`, `scripts/**`, `images/**`, and `.github/workflows/**`:
  installation safety, supply-chain assumptions, release behavior, and CI
  compatibility.

## Nits & Conventions

- Do not flag unsupported GitHub features merely because Greenrun reports them
  as `remote_only`; flag only misclassification or false confidence.
- Flag any implicit secret loading, unmasked secret exposure, implicit GitHub
  token retrieval for workflow execution, or unexpected network dependency.
- Tests should use tempdirs and in-memory fixtures. They must not depend on or
  mutate the developer's real checkout, home directory, Docker state, or remote
  GitHub state unless the test is explicitly scoped for that.
- Prefer narrow fixes that preserve GitHub Actions as the source of truth.
- Result wording matters: `pass`, `fail`, `partial`, `error`, `blocked`, and
  `remote_only` must remain distinct.

## Skip

- skip: .greenrun/**
- skip: dist-release/**
- skip: testdata/compat/**

## Voice

Terse, technical, evidence-first. No praise padding.
