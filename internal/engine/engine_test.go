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

func TestSingleJobPlanPinsMatrixParallelismToWeight(t *testing.T) {
	workflow := &actmodel.Workflow{
		Name: "CI",
		Jobs: map[string]*actmodel.Job{
			"build": {Strategy: &actmodel.Strategy{RawMatrix: mappingNode("node", "18", "20", "22")}},
		},
	}
	plan := singleJobPlan(workflow, "build", 2)
	require.Len(t, plan.Stages, 1)
	require.Len(t, plan.Stages[0].Runs, 1)
	require.Equal(t, "build", plan.Stages[0].Runs[0].JobID)
	require.Equal(t, 2, workflow.Jobs["build"].Strategy.MaxParallel)
	require.Equal(t, "2", workflow.Jobs["build"].Strategy.MaxParallelString)
}

func TestJobWeightUsesMatrixBreadthWithCaps(t *testing.T) {
	plain := &actmodel.Job{}
	require.Equal(t, 1, jobWeight(plain, 8))

	matrix := &actmodel.Job{Strategy: &actmodel.Strategy{RawMatrix: mappingNode("node", "18", "20", "22")}}
	require.Equal(t, 3, jobWeight(matrix, 8))
	require.Equal(t, 2, jobWeight(matrix, 2))

	declared := &actmodel.Job{Strategy: &actmodel.Strategy{
		RawMatrix:         mappingNode("node", "18", "20", "22"),
		MaxParallelString: "1",
	}}
	require.Equal(t, 1, jobWeight(declared, 8))

	reusable := &actmodel.Job{Uses: "./.github/workflows/child.yml"}
	require.Equal(t, 1, jobWeight(reusable, 8))
}

func TestApplyOutcomesMapsResultsHonestly(t *testing.T) {
	parsed := &actmodel.Workflow{Jobs: map[string]*actmodel.Job{
		"lint":    {Result: "failure"},
		"test":    {Result: "failure"},
		"build":   {Result: "success"},
		"publish": {Result: "skipped", RawNeeds: scalarNode("lint")},
		"docs":    {},
	}}
	plan := &model.Workflow{ID: "ci", Jobs: []model.Job{
		{ID: "lint", Status: model.StatusPending},
		{ID: "test", Status: model.StatusPending, Steps: []model.Step{
			{Name: "teardown casualty", Status: model.StatusFail},
		}},
		{ID: "build", Status: model.StatusPending},
		{ID: "publish", Status: model.StatusPending},
		{ID: "docs", Status: model.StatusPending},
	}}
	collector := &logCollector{started: map[string]time.Time{
		"lint": time.Now(), "test": time.Now(), "build": time.Now(), "publish": time.Now(),
	}}
	states := []*workflowState{{plan: plan, parsed: parsed, collector: collector}}
	nodes := []*node{
		{workflowIndex: 0, jobID: "lint"},
		{workflowIndex: 0, jobID: "test"},
		{workflowIndex: 0, jobID: "build"},
		{workflowIndex: 0, jobID: "publish"},
		{workflowIndex: 0, jobID: "docs"},
	}
	failures := &failureState{}
	failures.record("ci", "lint")
	results := []nodeResult{
		{dispatched: true, failed: true},
		{dispatched: true, failed: true}, // torn down by lint's cancellation
		{dispatched: true},
		{dispatched: true},
		{dispatched: false},
	}

	err := applyOutcomes(states, nodes, results, Options{}, failures, false)
	require.NoError(t, err)
	require.Equal(t, model.StatusFail, plan.Jobs[0].Status)
	require.Equal(t, model.StatusCancelled, plan.Jobs[1].Status)
	require.Equal(t, "cancelled after lint failed", plan.Jobs[1].Reason)
	require.Equal(t, model.StatusCancelled, plan.Jobs[1].Steps[0].Status,
		"steps of torn-down jobs must not read as failures")
	require.Equal(t, model.StatusPass, plan.Jobs[2].Status)
	require.Equal(t, model.StatusSkip, plan.Jobs[3].Status)
	require.Equal(t, "dependency lint did not succeed", plan.Jobs[3].Reason)
	require.Equal(t, model.StatusCancelled, plan.Jobs[4].Status)
	require.Equal(t, "cancelled after lint failed", plan.Jobs[4].Reason)
}

func TestApplyOutcomesReportsFirstFailureEvenWithoutRecordedResult(t *testing.T) {
	// The global cancel can tear down the failing job's plan before act
	// records its result; the collector's observation still counts.
	parsed := &actmodel.Workflow{Jobs: map[string]*actmodel.Job{"lint": {}}}
	plan := &model.Workflow{ID: "ci", Jobs: []model.Job{{ID: "lint", Status: model.StatusPending}}}
	collector := &logCollector{started: map[string]time.Time{"lint": time.Now()}}
	states := []*workflowState{{plan: plan, parsed: parsed, collector: collector}}
	nodes := []*node{{workflowIndex: 0, jobID: "lint"}}
	failures := &failureState{}
	failures.record("ci", "lint")

	err := applyOutcomes(states, nodes, []nodeResult{{dispatched: true}}, Options{}, failures, false)
	require.NoError(t, err)
	require.Equal(t, model.StatusFail, plan.Jobs[0].Status)
}

func TestApplyOutcomesCompleteModeKeepsEveryFailure(t *testing.T) {
	parsed := &actmodel.Workflow{Jobs: map[string]*actmodel.Job{
		"lint": {Result: "failure"},
		"test": {Result: "failure"},
	}}
	plan := &model.Workflow{ID: "ci", Jobs: []model.Job{
		{ID: "lint", Status: model.StatusPending},
		{ID: "test", Status: model.StatusPending},
	}}
	collector := &logCollector{started: map[string]time.Time{"lint": time.Now(), "test": time.Now()}}
	states := []*workflowState{{plan: plan, parsed: parsed, collector: collector}}
	nodes := []*node{
		{workflowIndex: 0, jobID: "lint"},
		{workflowIndex: 0, jobID: "test"},
	}
	failures := &failureState{}
	failures.record("ci", "lint")
	results := []nodeResult{
		{dispatched: true, failed: true},
		{dispatched: true, failed: true},
	}

	err := applyOutcomes(states, nodes, results, Options{Complete: true}, failures, false)
	require.NoError(t, err)
	require.Equal(t, model.StatusFail, plan.Jobs[0].Status)
	require.Equal(t, model.StatusFail, plan.Jobs[1].Status)
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
