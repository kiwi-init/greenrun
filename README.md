<div align="center">

# Greenrun

**Fail GitHub Actions as fast as possible, before the push.**

</div>

Greenrun reads the workflows already in `.github/workflows`, executes supported
Ubuntu jobs in isolated Docker runners, stops on the first useful failure, and
saves a compact result for humans and coding agents. It makes no model calls,
needs no account, and never reads `.env` implicitly.

## Install

```sh
curl -fsSL https://greenrun.sh/install.sh | sh
```

Requirements: Git and Docker. GitHub CLI is optional and enables real pull
request events plus remote run imports.

If Claude Code or Codex is detected, the installer also wires in a greenrun
skill so the agent runs `greenrun` after edits (`GREENRUN_NO_WIRE=1` to skip).

## Use

```sh
greenrun                         # infer event and fail fast
greenrun plan                    # inspect what will run
greenrun run --complete          # collect all failures
greenrun --jobs 8                # widen the global job pool
greenrun show latest             # compact agent-readable result
greenrun logs latest --failed    # failed logs only
greenrun github latest-failed    # import a remote failure
greenrun hook install            # verify every push from this machine
greenrun doctor
```

Greenrun has no repository configuration. GitHub Actions remains the only
pipeline definition.

## Scheduling

Jobs from every triggered workflow compete in one global queue sized to the
host (`--jobs` to override). The queue is ordered by expected failure yield:
each job's recorded failure rate divided by its recorded time-to-fail, seeded
by a name heuristic when a repository has no history, with the job that
failed last run probed first. Matrix jobs reserve slots for their breadth.
The first failure cancels everything; `run --complete` collects them all.

Local runs train the ranking automatically. Importing hosted evidence with
`greenrun github` trains it too, so a fresh clone can bootstrap its scheduler
from a remote failure.

The pre-push hook from `greenrun hook install` lives in `.git/hooks`, never
in the checkout. It blocks the push only on a real local CI failure; honest
`partial` results and Greenrun runtime errors never lock a push.

Secrets are explicit:

```sh
greenrun --secret NPM_TOKEN
greenrun --secret TOKEN=value
greenrun --secret-file ./local-ci.secrets
```

## Results

Runs are stored outside the checkout:

```text
~/.greenrun/repos/<repo>-<identity>/runs/<timestamp>-<suffix>/
├── result.gr
├── result.json
├── events.jsonl
├── event.json
├── plan.json
├── logs/
└── artifacts/
```

`result.gr` is compact and optimized for agent context. `result.json` is the
versioned canonical format. `greenrun path latest` resolves the location, so
agents do not need to understand the storage layout.

## Support Boundary

Greenrun 1.x executes `ubuntu-latest`, `ubuntu-24.04`, and `ubuntu-22.04`.
macOS, Windows, self-hosted, OIDC, and approval-gated jobs are reported as
`remote_only`, producing a `partial` result instead of false confidence.

Apple Silicon uses native arm64 images by default and labels the result
`compatible`. Use `greenrun --arch github` for `linux/amd64` emulation.

## Development

```sh
go test ./...
go build ./cmd/greenrun
```

The execution engine embeds a pinned MIT-licensed
[`nektos/act`](https://github.com/nektos/act) dependency behind
`internal/engine`.

## License

MIT
