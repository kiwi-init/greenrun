package output

import (
	"strings"
	"testing"

	"github.com/kiwi-init/greenrun/internal/model"
	"github.com/stretchr/testify/require"
)

func TestCompactResult(t *testing.T) {
	result := model.Result{
		ID: "run-1", Status: model.StatusFail, Source: model.SourceLocal,
		Fidelity:   model.FidelityExact,
		Repository: model.Repository{Slug: "kiwi-init/greenrun"},
		Event:      model.Event{Name: "pull_request"},
		Workflows: []model.Workflow{{
			ID: "ci", Jobs: []model.Job{{
				ID: "test", Status: model.StatusFail, DurationMS: 42,
				ErrorMessage: "tests failed", Log: "logs/ci-test.log",
			}},
		}},
		Artifacts: []model.Artifact{{
			Name: "coverage", SizeBytes: 128, DownloadedTo: "/tmp/coverage.zip",
		}},
	}
	compact := Compact(result)
	require.True(t, strings.HasPrefix(compact, "greenrun 1\n"))
	require.Contains(t, compact, "FAIL ci/test 42")
	require.Contains(t, compact, `msg "tests failed"`)
	require.Contains(t, compact, `artifact "coverage" 128 path="/tmp/coverage.zip"`)
}
