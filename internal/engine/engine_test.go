package engine

import (
	"testing"
	"time"

	"github.com/kiwi-init/greenrun/internal/model"
	actmodel "github.com/nektos/act/pkg/model"
	"github.com/stretchr/testify/require"
)

func TestDependencyOnRemoteJobIsBlocked(t *testing.T) {
	workflows := []model.Workflow{{
		Jobs: []model.Job{
			{ID: "remote", Status: model.StatusRemote, Fidelity: model.FidelityRemoteOnly},
			{ID: "build", Status: model.StatusPending, Needs: []string{"remote"}},
		},
	}}
	markDependencyBlocks(workflows)
	require.Equal(t, model.StatusBlocked, workflows[0].Jobs[1].Status)
}

func TestMakeActPlanUsesDependencyStagesAndRank(t *testing.T) {
	workflow := &actmodel.Workflow{
		Name: "CI",
		Jobs: map[string]*actmodel.Job{
			"lint":  {},
			"test":  {RawNeeds: scalarNode("lint")},
			"build": {RawNeeds: scalarNode("lint")},
		},
	}
	jobs := []model.Job{
		{ID: "lint", Status: model.StatusPending, Rank: 100},
		{ID: "test", Status: model.StatusPending, Rank: 80},
		{ID: "build", Status: model.StatusPending, Rank: 60},
	}
	plan, selected, err := makeActPlan(workflow, jobs)
	require.NoError(t, err)
	require.Len(t, selected, 3)
	require.Len(t, plan.Stages, 2)
	require.Equal(t, "lint", plan.Stages[0].Runs[0].JobID)
	require.Equal(t, []string{"test", "build"}, plan.Stages[1].GetJobIDs())
}

func TestSkippedWorkDoesNotLowerRunFidelity(t *testing.T) {
	workflows := []model.Workflow{
		{
			Triggered: true,
			Fidelity:  model.FidelityExact,
			Jobs: []model.Job{{
				ID:       "test",
				Status:   model.StatusPass,
				Fidelity: model.FidelityCompatible,
			}},
		},
		{
			Triggered: false,
			Fidelity:  model.FidelityExact,
			Jobs: []model.Job{{
				ID:       "windows-release",
				Status:   model.StatusSkip,
				Fidelity: model.FidelityRemoteOnly,
			}},
		},
	}

	require.Equal(t, model.FidelityCompatible, lowestFidelity(workflows))
}

func TestReconcilePlannedStepsRecordsSkippedSteps(t *testing.T) {
	workflow := model.Workflow{Jobs: []model.Job{{
		ID:    "test",
		Steps: []model.Step{{Name: "ran", Status: model.StatusPass}},
	}}}
	parsed := &actmodel.Workflow{Jobs: map[string]*actmodel.Job{
		"test": {Steps: []*actmodel.Step{
			{Name: "skipped"},
			{Name: "ran"},
			{Run: `echo "${{ matrix.value }}"`},
		}},
	}}
	collector := &logCollector{started: map[string]time.Time{"test": time.Now()}}

	reconcilePlannedSteps(&workflow, parsed, map[string]bool{"test": true}, collector)

	require.Len(t, workflow.Jobs[0].Steps, 2)
	require.Equal(t, "skipped", workflow.Jobs[0].Steps[1].Name)
	require.Equal(t, model.StatusSkip, workflow.Jobs[0].Steps[1].Status)
}

func TestReusableFallbackRequiresExactlyOneCaller(t *testing.T) {
	workflow := &actmodel.Workflow{Jobs: map[string]*actmodel.Job{
		"call": {Uses: "owner/repo/.github/workflows/ci.yml@main"},
		"lint": {},
	}}
	require.Equal(t, "call", reusableFallback(workflow, map[string]bool{"call": true, "lint": true}))

	workflow.Jobs["other"] = &actmodel.Job{Uses: "./.github/workflows/other.yml"}
	require.Empty(t, reusableFallback(workflow, map[string]bool{"call": true, "other": true}))
}
