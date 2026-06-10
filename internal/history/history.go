package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kiwi-init/greenrun/internal/model"
	"github.com/kiwi-init/greenrun/internal/store"
)

const (
	// SchemaVersion 2 splits pass durations from time-to-fail durations.
	SchemaVersion = 2

	maxDurations = 31

	// priorWeight is the strength of the name-heuristic prior, expressed in
	// pseudo-runs. Two real runs weigh as much as the prior; after a handful
	// of runs the observed record dominates.
	priorWeight = 2.0

	// defaultDetectMS is the assumed failure-detection time for a job with
	// no recorded durations.
	defaultDetectMS = 60_000

	// recentFailureBoost multiplies the rank of a job whose latest run
	// failed. In the edit-fix-rerun loop the job that just failed is by far
	// the most likely to fail again, and confirming the fix first is the
	// fastest path to a useful signal.
	recentFailureBoost = 3.0
)

type JobStats struct {
	Runs          int     `json:"runs"`
	Failures      int     `json:"failures"`
	PassDurations []int64 `json:"pass_durations_ms,omitempty"`
	FailDurations []int64 `json:"fail_durations_ms,omitempty"`
	LastStatus    string  `json:"last_status,omitempty"`
	UpdatedAt     string  `json:"updated_at,omitempty"`

	// LegacyDurations holds schema v1 durations until the first v2 write.
	LegacyDurations []int64 `json:"durations_ms,omitempty"`
}

type Stats struct {
	SchemaVersion int                 `json:"schema_version"`
	Jobs          map[string]JobStats `json:"jobs"`
}

// Key is the stats identifier for a job. Local runs and imported GitHub
// runs must agree on it for remote evidence to train the scheduler.
func Key(workflowID, jobID string) string {
	return workflowID + "/" + jobID
}

func Load(storage *store.Store, repository model.Repository) Stats {
	stats := Stats{SchemaVersion: SchemaVersion, Jobs: map[string]JobStats{}}
	data, err := os.ReadFile(filepath.Join(storage.RepoDir(repository), "stats.json"))
	if err == nil {
		_ = json.Unmarshal(data, &stats)
	}
	if stats.Jobs == nil {
		stats.Jobs = map[string]JobStats{}
	}
	for id, value := range stats.Jobs {
		if len(value.LegacyDurations) > 0 {
			// v1 recorded one duration list; most recorded runs pass, so it
			// approximates pass durations until fresh evidence arrives.
			value.PassDurations = append(value.PassDurations, value.LegacyDurations...)
			value.LegacyDurations = nil
			stats.Jobs[id] = value
		}
	}
	stats.SchemaVersion = SchemaVersion
	return stats
}

// Apply replaces each job's name-heuristic rank with its failure yield so
// the scheduler can compare jobs across workflows on one scale.
func Apply(plan *model.Plan, stats Stats) {
	for workflowIndex := range plan.Workflows {
		for jobIndex := range plan.Workflows[workflowIndex].Jobs {
			job := &plan.Workflows[workflowIndex].Jobs[jobIndex]
			job.Rank = Rank(job.Rank, stats.Jobs[Key(job.WorkflowID, job.ID)])
		}
	}
}

// Rank scores a job by expected failures detected per hour of runner time:
// a smoothed failure probability divided by the expected time to observe a
// failure. The name heuristic (0-100) seeds the probability prior, so a
// repository without history is ordered exactly as the heuristic suggests,
// and recorded runs take over smoothly from there.
func Rank(heuristic float64, value JobStats) float64 {
	prior := 0.10 + 0.25*clamp(heuristic, 0, 100)/100
	probability := (float64(value.Failures) + prior*priorWeight) / (float64(value.Runs) + priorWeight)
	seconds := float64(detectionMS(value)) / 1000
	score := probability / seconds * 3600
	if value.LastStatus == string(model.StatusFail) {
		score *= recentFailureBoost
	}
	return score
}

// detectionMS estimates how long the job runs before a failure is known.
// Recorded failures are the direct signal; pass durations are the upper
// bound when the job has never failed locally.
func detectionMS(value JobStats) int64 {
	if duration := median(value.FailDurations); duration > 0 {
		return max64(duration, 1000)
	}
	if duration := median(value.PassDurations); duration > 0 {
		return max64(duration, 1000)
	}
	return defaultDetectMS
}

// Update records local run outcomes under the canonical workflow/job keys.
func Update(storage *store.Store, result model.Result) error {
	return UpdateWithKeys(storage, result, func(workflow model.Workflow, job model.Job) (string, bool) {
		return Key(workflow.ID, job.ID), true
	})
}

// UpdateWithKeys records run outcomes using a caller-supplied key mapping,
// so imported GitHub runs can train the same stats that rank local jobs.
// Jobs the mapping cannot resolve are skipped.
func UpdateWithKeys(storage *store.Store, result model.Result, keyFor func(model.Workflow, model.Job) (string, bool)) error {
	stats := Load(storage, result.Repository)
	changed := false
	for _, workflow := range result.Workflows {
		for _, job := range workflow.Jobs {
			if job.Status != model.StatusPass && job.Status != model.StatusFail {
				continue
			}
			id, ok := keyFor(workflow, job)
			if !ok {
				continue
			}
			value := stats.Jobs[id]
			value.Runs++
			if job.Status == model.StatusFail {
				value.Failures++
				value.FailDurations = appendDuration(value.FailDurations, job.DurationMS)
			} else {
				value.PassDurations = appendDuration(value.PassDurations, job.DurationMS)
			}
			value.LastStatus = string(job.Status)
			value.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			stats.Jobs[id] = value
			changed = true
		}
	}
	if !changed {
		return nil
	}
	path := filepath.Join(storage.RepoDir(result.Repository), "stats.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(stats, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// RemoteKey normalizes a GitHub-displayed workflow/job name pair for index
// lookups. Matrix expansions such as "build (ubuntu-latest, 20)" fold into
// their base job name.
func RemoteKey(workflowName, jobName string) string {
	if open := strings.Index(jobName, " ("); open > 0 {
		jobName = jobName[:open]
	}
	return strings.ToLower(strings.TrimSpace(workflowName)) + "\n" + strings.ToLower(strings.TrimSpace(jobName))
}

func appendDuration(values []int64, duration int64) []int64 {
	if duration <= 0 {
		return values
	}
	values = append(values, duration)
	if len(values) > maxDurations {
		values = values[len(values)-maxDurations:]
	}
	return values
}

func median(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	copyValues := append([]int64(nil), values...)
	sort.Slice(copyValues, func(i, j int) bool { return copyValues[i] < copyValues[j] })
	return copyValues[len(copyValues)/2]
}

func clamp(value, low, high float64) float64 {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func max64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
