package history

import (
	"testing"

	"github.com/kiwi-init/greenrun/internal/model"
	"github.com/stretchr/testify/require"
)

func TestApplyPrioritizesFrequentFastFailures(t *testing.T) {
	plan := model.Plan{Workflows: []model.Workflow{{
		ID: "ci",
		Jobs: []model.Job{
			{ID: "slow-pass", WorkflowID: "ci", Rank: 70},
			{ID: "fast-fail", WorkflowID: "ci", Rank: 70},
		},
	}}}
	stats := Stats{Jobs: map[string]JobStats{
		"ci/slow-pass": {Runs: 10, Durations: []int64{600000}},
		"ci/fast-fail": {Runs: 10, Failures: 8, Durations: []int64{1000}},
	}}
	Apply(&plan, stats)
	require.Greater(t, plan.Workflows[0].Jobs[1].Rank, plan.Workflows[0].Jobs[0].Rank)
}
