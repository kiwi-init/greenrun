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

## Use

```sh
greenrun                         # infer event and fail fast
greenrun plan                    # inspect what will run
greenrun run --complete          # collect all failures
greenrun show latest             # compact agent-readable result
greenrun logs latest --failed    # failed logs only
greenrun github latest-failed    # import a remote failure
greenrun doctor
```

Greenrun has no repository configuration. GitHub Actions remains the only
pipeline definition.

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

Greenrun 1.0 executes `ubuntu-latest`, `ubuntu-24.04`, and `ubuntu-22.04`.
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
