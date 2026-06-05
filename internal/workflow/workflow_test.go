package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiwi-init/greenrun/internal/model"
	actmodel "github.com/nektos/act/pkg/model"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestBuildPlanFiltersAndClassifiesJobs(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".github", "workflows")
	require.NoError(t, os.MkdirAll(path, 0o755))
	content := `name: CI
on:
  pull_request:
    branches: [main]
    paths: ["src/**"]
jobs:
  test:
    runs-on: ubuntu-24.04
    steps:
      - run: echo "${{ secrets.TEST_TOKEN }}"
  docs:
    runs-on: ubuntu-24.04
    steps:
      - run: echo docs
  mac:
    runs-on: macos-15
    steps:
      - run: echo mac
`
	require.NoError(t, os.WriteFile(filepath.Join(path, "ci.yml"), []byte(content), 0o644))
	repository := model.Repository{
		Root: root, Slug: "owner/repo", Branch: "feature", DefaultBranch: "main",
		ChangedFiles: []string{"src/main.go"},
	}
	event := model.Event{Name: "pull_request", BaseRef: "main", HeadRef: "feature", Fidelity: model.FidelityExact}
	plan, err := BuildPlan(context.Background(), repository, event, root, Options{})
	require.NoError(t, err)
	require.Len(t, plan.Workflows, 1)
	require.True(t, plan.Workflows[0].Triggered)
	require.Equal(t, model.StatusBlocked, findJob(plan.Workflows[0], "test").Status)
	require.Equal(t, model.StatusPending, findJob(plan.Workflows[0], "docs").Status)
	require.Equal(t, model.StatusRemote, findJob(plan.Workflows[0], "mac").Status)
}

func TestPathFilterSkipsWorkflow(t *testing.T) {
	parsed, err := actmodel.ReadWorkflow(strings.NewReader(`on:
  pull_request:
    branches: [main]
    paths: ["src/**"]
jobs:
  test:
    runs-on: ubuntu-24.04
    steps:
      - run: echo ok
`), false)
	require.NoError(t, err)
	triggered, _, _ := matchesEvent(parsed, model.Event{
		Name: "pull_request", BaseRef: "main",
	}, []string{"docs/readme.md"})
	require.False(t, triggered)
}

func TestTagOnlyPushDoesNotRunForBranch(t *testing.T) {
	parsed, err := actmodel.ReadWorkflow(strings.NewReader(`on:
  push:
    tags: ["v*"]
jobs:
  release:
    runs-on: ubuntu-24.04
    steps:
      - run: echo release
`), false)
	require.NoError(t, err)
	triggered, reason, _ := matchesEvent(parsed, model.Event{
		Name: "push", HeadRef: "main",
	}, []string{"src/main.go"})
	require.False(t, triggered)
	require.Contains(t, reason, "tag")
}

func TestMixedOSRunnerMatrixIsRemoteOnly(t *testing.T) {
	parsed, err := actmodel.ReadWorkflow(strings.NewReader(`on: push
jobs:
  build:
    runs-on: ${{ matrix.os }}
    strategy:
      matrix:
        os: [ubuntu-24.04, macos-15]
    steps:
      - run: echo build
`), false)
	require.NoError(t, err)
	fidelity, status, reason := classifyJob(parsed.GetJob("build"), false, yaml.Node{}, "native")
	require.Equal(t, model.FidelityRemoteOnly, fidelity)
	require.Equal(t, model.StatusRemote, status)
	require.Contains(t, reason, "unsupported")
}

func TestCompatibilityFixturesPlanCleanly(t *testing.T) {
	for _, fixture := range []string{"basic", "advanced"} {
		t.Run(fixture, func(t *testing.T) {
			root, err := filepath.Abs(filepath.Join("..", "..", "testdata", "compat", fixture))
			require.NoError(t, err)
			repository := model.Repository{
				Root: root, Slug: "greenrun/" + fixture, Branch: "main", DefaultBranch: "main",
				ChangedFiles: []string{"README.md"},
			}
			plan, err := BuildPlan(context.Background(), repository, model.Event{
				Name: "push", HeadRef: "main", Fidelity: model.FidelityExact,
			}, root, Options{Arch: "github"})
			require.NoError(t, err)
			for _, diagnostic := range plan.Diagnostics {
				require.NotEqual(t, "error", diagnostic.Level, diagnostic.Message)
			}
		})
	}
}

func findJob(workflow model.Workflow, id string) model.Job {
	for _, job := range workflow.Jobs {
		if job.ID == id {
			return job
		}
	}
	return model.Job{}
}
