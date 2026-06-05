package githubrun

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kiwi-init/greenrun/internal/event"
	"github.com/kiwi-init/greenrun/internal/executil"
	"github.com/kiwi-init/greenrun/internal/mask"
	"github.com/kiwi-init/greenrun/internal/model"
	"github.com/kiwi-init/greenrun/internal/output"
	"github.com/kiwi-init/greenrun/internal/store"
)

type Options struct {
	Reference string
	Watch     bool
	Artifacts bool
	Secrets   map[string]string
}

type runView struct {
	Attempt      int       `json:"attempt"`
	Conclusion   string    `json:"conclusion"`
	CreatedAt    time.Time `json:"createdAt"`
	DatabaseID   int64     `json:"databaseId"`
	DisplayTitle string    `json:"displayTitle"`
	Event        string    `json:"event"`
	HeadBranch   string    `json:"headBranch"`
	HeadSHA      string    `json:"headSha"`
	Jobs         []jobView `json:"jobs"`
	Name         string    `json:"name"`
	Number       int       `json:"number"`
	StartedAt    time.Time `json:"startedAt"`
	Status       string    `json:"status"`
	UpdatedAt    time.Time `json:"updatedAt"`
	URL          string    `json:"url"`
	WorkflowName string    `json:"workflowName"`
}

type jobView struct {
	CompletedAt time.Time  `json:"completedAt"`
	Conclusion  string     `json:"conclusion"`
	DatabaseID  int64      `json:"databaseId"`
	Name        string     `json:"name"`
	StartedAt   time.Time  `json:"startedAt"`
	Status      string     `json:"status"`
	Steps       []stepView `json:"steps"`
}

type stepView struct {
	CompletedAt time.Time `json:"completedAt"`
	Conclusion  string    `json:"conclusion"`
	Name        string    `json:"name"`
	Number      int       `json:"number"`
	StartedAt   time.Time `json:"startedAt"`
	Status      string    `json:"status"`
}

type artifactsResponse struct {
	Artifacts []struct {
		ID                 int64     `json:"id"`
		Name               string    `json:"name"`
		SizeInBytes        int64     `json:"size_in_bytes"`
		ArchiveDownloadURL string    `json:"archive_download_url"`
		Expired            bool      `json:"expired"`
		CreatedAt          time.Time `json:"created_at"`
		ExpiresAt          time.Time `json:"expires_at"`
	} `json:"artifacts"`
}

type annotationView struct {
	Path            string `json:"path"`
	StartLine       int    `json:"start_line"`
	StartColumn     int    `json:"start_column"`
	AnnotationLevel string `json:"annotation_level"`
	Title           string `json:"title"`
	Message         string `json:"message"`
	RawDetails      string `json:"raw_details"`
}

func Import(ctx context.Context, repository model.Repository, run *store.Run, options Options) (model.Result, error) {
	if !executil.Exists("gh") {
		return model.Result{}, fmt.Errorf("GitHub CLI is required; install gh and authenticate with `gh auth login`")
	}
	id, err := resolveRunID(ctx, repository, options.Reference)
	if err != nil {
		return model.Result{}, err
	}
	if options.Watch {
		_ = executil.Run(ctx, repository.Root, "gh", "run", "watch", strconv.FormatInt(id, 10), "--exit-status")
	}

	fields := "attempt,conclusion,createdAt,databaseId,displayTitle,event,headBranch,headSha,jobs,name,number,startedAt,status,updatedAt,url,workflowName"
	raw, err := executil.Output(ctx, repository.Root, "gh", "run", "view", strconv.FormatInt(id, 10), "--json", fields)
	if err != nil {
		return model.Result{}, err
	}
	var remote runView
	if err := json.Unmarshal([]byte(raw), &remote); err != nil {
		return model.Result{}, err
	}

	values := make([]string, 0, len(options.Secrets))
	for _, value := range options.Secrets {
		values = append(values, value)
	}
	masker := mask.New(values...)
	failedLog, _ := executil.Output(ctx, repository.Root, "gh", "run", "view", strconv.FormatInt(id, 10), "--log-failed")
	masker.Observe(failedLog)
	failedLog = masker.Apply(failedLog)
	logPath := ""
	if failedLog != "" {
		logPath, err = run.WriteLog("github-failed", failedLog+"\n")
		if err != nil {
			return model.Result{}, err
		}
	}

	workflow := model.Workflow{
		ID: remote.WorkflowName, Name: remote.WorkflowName, Path: remote.Name,
		Triggered: true, Fidelity: model.FidelityExact,
	}
	for _, remoteJob := range remote.Jobs {
		jobStatus := status(remoteJob.Conclusion, remoteJob.Status)
		job := model.Job{
			ID: strconv.FormatInt(remoteJob.DatabaseID, 10), Name: remoteJob.Name,
			WorkflowID: workflow.ID, WorkflowName: workflow.Name,
			Status: jobStatus, Fidelity: model.FidelityExact,
		}
		if jobStatus == model.StatusFail {
			job.Log = logPath
		}
		if !remoteJob.StartedAt.IsZero() {
			value := remoteJob.StartedAt
			job.StartedAt = &value
		}
		if !remoteJob.CompletedAt.IsZero() {
			value := remoteJob.CompletedAt
			job.CompletedAt = &value
			if job.StartedAt != nil {
				job.DurationMS = value.Sub(*job.StartedAt).Milliseconds()
			}
		}
		for _, remoteStep := range remoteJob.Steps {
			step := model.Step{
				ID: strconv.Itoa(remoteStep.Number), Name: remoteStep.Name,
				Status: status(remoteStep.Conclusion, remoteStep.Status),
			}
			if !remoteStep.StartedAt.IsZero() {
				value := remoteStep.StartedAt
				step.StartedAt = &value
			}
			if !remoteStep.CompletedAt.IsZero() {
				value := remoteStep.CompletedAt
				step.CompletedAt = &value
				if step.StartedAt != nil {
					step.DurationMS = value.Sub(*step.StartedAt).Milliseconds()
				}
			}
			job.Steps = append(job.Steps, step)
		}
		if annotations, annotationErr := fetchAnnotations(ctx, repository, remoteJob.DatabaseID, masker); annotationErr == nil {
			job.Diagnostics = annotations
		}
		workflow.Jobs = append(workflow.Jobs, job)
	}

	artifacts, artifactErr := fetchArtifacts(ctx, repository, id)
	if artifactErr == nil {
		_ = run.WriteJSON(filepath.Join("artifacts", "metadata.json"), artifacts)
	}
	if options.Artifacts {
		if err := downloadArtifacts(ctx, repository, id, run.ArtifactsDir); err != nil {
			return model.Result{}, err
		}
		for index := range artifacts {
			artifacts[index].DownloadedTo = run.ArtifactsDir
		}
	}

	started := remote.StartedAt
	if started.IsZero() {
		started = remote.CreatedAt
	}
	completed := remote.UpdatedAt
	if completed.IsZero() {
		completed = time.Now().UTC()
	}
	resultStatus := status(remote.Conclusion, remote.Status)
	if resultStatus == model.StatusRunning {
		resultStatus = model.StatusPartial
	}
	result := model.Result{
		SchemaVersion: model.SchemaVersion,
		ID:            run.ID,
		Source:        model.SourceGitHub,
		Status:        resultStatus,
		Fidelity:      model.FidelityExact,
		StartedAt:     started,
		CompletedAt:   completed,
		DurationMS:    completed.Sub(started).Milliseconds(),
		Repository:    repository,
		Event: model.Event{
			Name: remote.Event, Fidelity: model.FidelityExact, HeadRef: remote.HeadBranch,
			HeadSHA: remote.HeadSHA, Reason: "imported from GitHub Actions",
		},
		Workflows:    []model.Workflow{workflow},
		Artifacts:    artifacts,
		Reproduce:    reproduce(workflow),
		RunDirectory: run.Directory,
		RemoteURL:    remote.URL,
	}
	if artifactErr != nil {
		result.Diagnostics = append(result.Diagnostics, model.Diagnostic{
			Level: "warning", Message: "artifact metadata unavailable: " + artifactErr.Error(),
		})
	}
	if err := run.Complete(result, output.Compact(result)); err != nil {
		return model.Result{}, err
	}
	return result, nil
}

func resolveRunID(ctx context.Context, repository model.Repository, reference string) (int64, error) {
	id, err := event.ParseGitHubRun(reference)
	if err != nil {
		return 0, err
	}
	if id > 0 {
		return id, nil
	}
	raw, err := executil.Output(ctx, repository.Root, "gh", "run", "list", "--status", "failure", "--limit", "1", "--json", "databaseId")
	if err != nil {
		return 0, err
	}
	var values []struct {
		DatabaseID int64 `json:"databaseId"`
	}
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return 0, err
	}
	if len(values) == 0 {
		return 0, fmt.Errorf("no failed GitHub Actions run found")
	}
	return values[0].DatabaseID, nil
}

func fetchArtifacts(ctx context.Context, repository model.Repository, id int64) ([]model.Artifact, error) {
	raw, err := executil.Output(ctx, repository.Root, "gh", "api",
		fmt.Sprintf("repos/%s/actions/runs/%d/artifacts", repository.Slug, id), "--paginate", "--slurp")
	if err != nil {
		return nil, err
	}
	var pages []artifactsResponse
	if err := json.Unmarshal([]byte(raw), &pages); err != nil {
		return nil, err
	}
	var values []model.Artifact
	for _, response := range pages {
		for _, artifact := range response.Artifacts {
			values = append(values, model.Artifact{
				ID: artifact.ID, Name: artifact.Name, SizeBytes: artifact.SizeInBytes,
				ArchiveURL: artifact.ArchiveDownloadURL, Expired: artifact.Expired,
				CreatedAt: artifact.CreatedAt, ExpiresAt: artifact.ExpiresAt,
			})
		}
	}
	return values, nil
}

func downloadArtifacts(ctx context.Context, repository model.Repository, id int64, directory string) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	return executil.Run(ctx, repository.Root, "gh", "run", "download", strconv.FormatInt(id, 10), "--dir", directory)
}

func fetchAnnotations(ctx context.Context, repository model.Repository, jobID int64, masker *mask.Masker) ([]model.Diagnostic, error) {
	raw, err := executil.Output(
		ctx,
		repository.Root,
		"gh",
		"api",
		fmt.Sprintf("repos/%s/check-runs/%d/annotations", repository.Slug, jobID),
		"--paginate",
		"--slurp",
	)
	if err != nil {
		return nil, err
	}
	return parseAnnotations([]byte(raw), masker)
}

func parseAnnotations(raw []byte, masker *mask.Masker) ([]model.Diagnostic, error) {
	var pages [][]annotationView
	if err := json.Unmarshal(raw, &pages); err != nil {
		return nil, err
	}
	var diagnostics []model.Diagnostic
	for _, page := range pages {
		for _, annotation := range page {
			message := strings.TrimSpace(strings.TrimSpace(annotation.Title + " " + annotation.Message))
			if annotation.RawDetails != "" {
				message = strings.TrimSpace(message + "\n" + annotation.RawDetails)
			}
			masker.Observe(message)
			diagnostics = append(diagnostics, model.Diagnostic{
				Level:   annotation.AnnotationLevel,
				Message: masker.Apply(message),
				File:    annotation.Path,
				Line:    annotation.StartLine,
				Column:  annotation.StartColumn,
			})
		}
	}
	return diagnostics, nil
}

func status(conclusion, current string) model.Status {
	value := strings.ToLower(conclusion)
	if value == "" {
		value = strings.ToLower(current)
	}
	switch value {
	case "success":
		return model.StatusPass
	case "failure", "timed_out", "startup_failure":
		return model.StatusFail
	case "cancelled":
		return model.StatusCancelled
	case "skipped", "neutral":
		return model.StatusSkip
	case "in_progress", "queued", "pending", "waiting", "requested":
		return model.StatusRunning
	default:
		return model.StatusError
	}
}

func reproduce(workflow model.Workflow) string {
	for _, job := range workflow.Jobs {
		if job.Status == model.StatusFail {
			return "greenrun"
		}
	}
	return ""
}
