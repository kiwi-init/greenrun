# Contributing

Requirements: Go from `go.mod`, Git, and Docker.

```sh
go test ./...
go vet ./...
test -z "$(gofmt -l .)"
go build ./cmd/greenrun
```

Use `GREENRUN_IMAGE_22` and `GREENRUN_IMAGE_24` to test against development
runner images. The compatibility suite is under `testdata/compat`.

Do not add repository-specific Greenrun configuration. Workflow behavior must
continue to derive from GitHub Actions YAML.
