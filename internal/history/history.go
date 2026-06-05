package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/kiwi-init/greenrun/internal/model"
	"github.com/kiwi-init/greenrun/internal/store"
)

const maxDurations = 31

type JobStats struct {
	Runs       int     `json:"runs"`
	Failures   int     `json:"failures"`
	Durations  []int64 `json:"durations_ms,omitempty"`
	LastStatus string  `json:"last_status,omitempty"`
	UpdatedAt  string  `json:"updated_at,omitempty"`
}

type Stats struct {
	SchemaVersion int                 `json:"schema_version"`
	Jobs          map[string]JobStats `json:"jobs"`
}

func Load(storage *store.Store, repository model.Repository) Stats {
	stats := Stats{SchemaVersion: 1, Jobs: map[string]JobStats{}}
	data, err := os.ReadFile(filepath.Join(storage.RepoDir(repository), "stats.json"))
	if err == nil {
		_ = json.Unmarshal(data, &stats)
	}
	if stats.Jobs == nil {
		stats.Jobs = map[string]JobStats{}
	}
	return stats
}

func Apply(plan *model.Plan, stats Stats) {
	for workflowIndex := range plan.Workflows {
		for jobIndex := range plan.Workflows[workflowIndex].Jobs {
			job := &plan.Workflows[workflowIndex].Jobs[jobIndex]
			value, ok := stats.Jobs[key(job.WorkflowID, job.ID)]
			if !ok || value.Runs == 0 {
				continue
			}
			failureRate := float64(value.Failures) / float64(value.Runs)
			medianSeconds := float64(median(value.Durations)) / 1000
			job.Rank += failureRate*200 - medianSeconds/10
		}
	}
}

func Update(storage *store.Store, result model.Result) error {
	stats := Load(storage, result.Repository)
	for _, workflow := range result.Workflows {
		for _, job := range workflow.Jobs {
			if job.Status != model.StatusPass && job.Status != model.StatusFail {
				continue
			}
			id := key(workflow.ID, job.ID)
			value := stats.Jobs[id]
			value.Runs++
			if job.Status == model.StatusFail {
				value.Failures++
			}
			if job.DurationMS > 0 {
				value.Durations = append(value.Durations, job.DurationMS)
				if len(value.Durations) > maxDurations {
					value.Durations = value.Durations[len(value.Durations)-maxDurations:]
				}
			}
			value.LastStatus = string(job.Status)
			value.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			stats.Jobs[id] = value
		}
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

func key(workflow, job string) string {
	return workflow + "/" + job
}

func median(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	copyValues := append([]int64(nil), values...)
	sort.Slice(copyValues, func(i, j int) bool { return copyValues[i] < copyValues[j] })
	return copyValues[len(copyValues)/2]
}
