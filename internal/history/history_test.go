package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kiwi-init/greenrun/internal/model"
	"github.com/kiwi-init/greenrun/internal/store"
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
		"ci/slow-pass": {Runs: 10, PassDurations: []int64{600000}},
		"ci/fast-fail": {Runs: 10, Failures: 8, FailDurations: []int64{1000}},
	}}
	Apply(&plan, stats)
	require.Greater(t, plan.Workflows[0].Jobs[1].Rank, plan.Workflows[0].Jobs[0].Rank)
}

func TestRankColdStartFollowsNameHeuristic(t *testing.T) {
	lint := Rank(100, JobStats{})
	e2e := Rank(30, JobStats{})
	require.Greater(t, lint, e2e)
}

func TestRankPrefersTimeToFailOverTotalDuration(t *testing.T) {
	// Both jobs fail half the time; the one that dies in ten seconds is a
	// better probe than the one that takes five minutes to report.
	fast := Rank(70, JobStats{Runs: 10, Failures: 5, FailDurations: []int64{10_000}})
	slow := Rank(70, JobStats{Runs: 10, Failures: 5, FailDurations: []int64{300_000}})
	require.Greater(t, fast, slow)
}

func TestRankBoostsTheJobThatJustFailed(t *testing.T) {
	stats := JobStats{Runs: 10, Failures: 2, FailDurations: []int64{30_000}}
	failedLast := stats
	failedLast.LastStatus = string(model.StatusFail)
	require.Greater(t, Rank(70, failedLast), Rank(70, stats))
}

func TestRankRecordedRunsOutweighPrior(t *testing.T) {
	// A "deploy"-named job (low prior) with a strong failure record must
	// outrank a "lint"-named job (high prior) that always passes.
	deploy := Rank(10, JobStats{Runs: 20, Failures: 15, FailDurations: []int64{20_000}})
	lint := Rank(100, JobStats{Runs: 20, PassDurations: []int64{20_000}})
	require.Greater(t, deploy, lint)
}

func TestLoadMigratesV1Durations(t *testing.T) {
	storage := &store.Store{Home: t.TempDir()}
	repository := model.Repository{Slug: "owner/repo", Identity: "id"}
	path := filepath.Join(storage.RepoDir(repository), "stats.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	v1 := `{"schema_version":1,"jobs":{"ci/test":{"runs":4,"failures":1,"durations_ms":[1500,2500],"last_status":"pass"}}}`
	require.NoError(t, os.WriteFile(path, []byte(v1), 0o600))

	stats := Load(storage, repository)
	require.Equal(t, SchemaVersion, stats.SchemaVersion)
	job := stats.Jobs["ci/test"]
	require.Equal(t, []int64{1500, 2500}, job.PassDurations)
	require.Empty(t, job.LegacyDurations)
}

func TestUpdateSplitsPassAndFailDurations(t *testing.T) {
	storage := &store.Store{Home: t.TempDir()}
	repository := model.Repository{Slug: "owner/repo", Identity: "id"}
	result := model.Result{Repository: repository, Workflows: []model.Workflow{{
		ID: "ci",
		Jobs: []model.Job{
			{ID: "lint", Status: model.StatusFail, DurationMS: 1200},
			{ID: "test", Status: model.StatusPass, DurationMS: 8000},
			{ID: "skipped", Status: model.StatusSkip, DurationMS: 10},
		},
	}}}
	require.NoError(t, Update(storage, result))

	stats := Load(storage, repository)
	lint := stats.Jobs["ci/lint"]
	require.Equal(t, 1, lint.Runs)
	require.Equal(t, 1, lint.Failures)
	require.Equal(t, []int64{1200}, lint.FailDurations)
	require.Empty(t, lint.PassDurations)
	test := stats.Jobs["ci/test"]
	require.Equal(t, []int64{8000}, test.PassDurations)
	require.Empty(t, test.FailDurations)
	_, recorded := stats.Jobs["ci/skipped"]
	require.False(t, recorded)

	data, err := os.ReadFile(filepath.Join(storage.RepoDir(repository), "stats.json"))
	require.NoError(t, err)
	var written Stats
	require.NoError(t, json.Unmarshal(data, &written))
	require.Equal(t, SchemaVersion, written.SchemaVersion)
}

func TestUpdateWithKeysSkipsUnmappedJobs(t *testing.T) {
	storage := &store.Store{Home: t.TempDir()}
	repository := model.Repository{Slug: "owner/repo", Identity: "id"}
	result := model.Result{Repository: repository, Workflows: []model.Workflow{{
		ID: "CI", Name: "CI",
		Jobs: []model.Job{
			{ID: "1234", Name: "Build It", Status: model.StatusFail, DurationMS: 900},
			{ID: "5678", Name: "Unknown Job", Status: model.StatusPass, DurationMS: 100},
		},
	}}}
	index := map[string]string{RemoteKey("CI", "Build It"): Key("ci", "build")}
	require.NoError(t, UpdateWithKeys(storage, result, func(workflow model.Workflow, job model.Job) (string, bool) {
		id, ok := index[RemoteKey(workflow.Name, job.Name)]
		return id, ok
	}))

	stats := Load(storage, repository)
	require.Len(t, stats.Jobs, 1)
	build := stats.Jobs["ci/build"]
	require.Equal(t, 1, build.Failures)
	require.Equal(t, []int64{900}, build.FailDurations)
}

func TestRemoteKeyFoldsMatrixExpansions(t *testing.T) {
	require.Equal(t, RemoteKey("CI", "build"), RemoteKey("ci", "build (ubuntu-latest, 20)"))
	require.NotEqual(t, RemoteKey("CI", "build"), RemoteKey("CI", "build-arm"))
}
