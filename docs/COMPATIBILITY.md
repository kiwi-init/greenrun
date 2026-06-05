# Compatibility

Greenrun 1.0 supports GitHub Actions jobs that resolve entirely to
`ubuntu-latest`, `ubuntu-24.04`, or `ubuntu-22.04`.

## Supported

- Shell steps and workflow/job environment values
- JavaScript, Docker, and local composite actions
- Local and remote reusable workflows supported by the embedded runtime
- Matrices whose runner values are all supported Ubuntu labels
- Job dependencies, outputs, conditions, services, and job containers
- Actions cache and uploaded artifacts through local compatibility servers
- Workflow dispatch, push, and pull request event contexts
- Branch, tag-only, and path trigger filtering
- Explicit secrets with persisted-log masking

## Reported As Remote Only

- macOS and Windows runners
- `self-hosted` and custom runner groups
- Mixed-OS runner matrices
- GitHub OIDC
- GitHub environments that may require approval

Unsupported work is never silently treated as passed. It yields a `partial`
result unless an executed job fails.

## Fidelity

- `exact`: matching operating system and architecture semantics
- `compatible`: supported environment with a known architecture difference
- `approximate`: event or expression inference was required
- `remote_only`: execution requires GitHub or unsupported infrastructure
