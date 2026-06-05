package event

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kiwi-init/greenrun/internal/executil"
	"github.com/kiwi-init/greenrun/internal/model"
)

type prView struct {
	Number      int    `json:"number"`
	BaseRefName string `json:"baseRefName"`
	HeadRefName string `json:"headRefName"`
	BaseRefOID  string `json:"baseRefOid"`
	HeadRefOID  string `json:"headRefOid"`
	URL         string `json:"url"`
}

func Infer(ctx context.Context, repository model.Repository, snapshotHead, explicit string) model.Event {
	if explicit != "" {
		return synthesize(repository, snapshotHead, explicit, "event selected by command line")
	}
	if repository.Branch != repository.DefaultBranch {
		if pull, ok := currentPullRequest(ctx, repository.Root); ok {
			payload := pullRequestPayload(repository, snapshotHead, pull.BaseRefName, pull.BaseRefOID, pull.Number)
			return model.Event{
				Name:       "pull_request",
				Inferred:   true,
				Fidelity:   model.FidelityExact,
				BaseRef:    pull.BaseRefName,
				HeadRef:    pull.HeadRefName,
				BaseSHA:    pull.BaseRefOID,
				HeadSHA:    snapshotHead,
				Payload:    payload,
				Reason:     "using pull request metadata from gh",
				PullNumber: pull.Number,
			}
		}
		return synthesize(repository, snapshotHead, "pull_request", "feature branch without accessible pull request metadata")
	}
	return synthesize(repository, snapshotHead, "push", "current branch is the default branch")
}

func Write(runDir string, event model.Event) (string, error) {
	path := filepath.Join(runDir, "event.json")
	data, err := json.MarshalIndent(event.Payload, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func synthesize(repository model.Repository, snapshotHead, name, reason string) model.Event {
	switch name {
	case "pull_request", "pull_request_target":
		payload := pullRequestPayload(repository, snapshotHead, repository.DefaultBranch, repository.BaseSHA, 0)
		return model.Event{
			Name:     name,
			Inferred: true,
			Fidelity: model.FidelityApproximate,
			BaseRef:  repository.DefaultBranch,
			HeadRef:  repository.Branch,
			BaseSHA:  repository.BaseSHA,
			HeadSHA:  snapshotHead,
			Payload:  payload,
			Reason:   reason,
		}
	default:
		payload := map[string]any{
			"ref":    "refs/heads/" + repository.Branch,
			"before": repository.HeadSHA,
			"after":  snapshotHead,
			"repository": map[string]any{
				"full_name":      repository.Slug,
				"default_branch": repository.DefaultBranch,
			},
			"head_commit": map[string]any{"id": snapshotHead},
		}
		return model.Event{
			Name:     name,
			Inferred: true,
			Fidelity: model.FidelityApproximate,
			HeadRef:  repository.Branch,
			BaseSHA:  repository.HeadSHA,
			HeadSHA:  snapshotHead,
			Payload:  payload,
			Reason:   reason,
		}
	}
}

func pullRequestPayload(repository model.Repository, headSHA, baseRef, baseSHA string, number int) map[string]any {
	return map[string]any{
		"action": "synchronize",
		"number": number,
		"pull_request": map[string]any{
			"number": number,
			"base": map[string]any{
				"ref": baseRef,
				"sha": baseSHA,
				"repo": map[string]any{
					"full_name":      repository.Slug,
					"default_branch": repository.DefaultBranch,
				},
			},
			"head": map[string]any{
				"ref":  repository.Branch,
				"sha":  headSHA,
				"repo": map[string]any{"full_name": repository.Slug},
			},
		},
		"repository": map[string]any{
			"full_name":      repository.Slug,
			"default_branch": repository.DefaultBranch,
		},
		"sender": map[string]any{"login": "greenrun"},
	}
}

func currentPullRequest(ctx context.Context, root string) (prView, bool) {
	if !executil.Exists("gh") {
		return prView{}, false
	}
	output, err := executil.Output(ctx, root, "gh", "pr", "view", "--json",
		"number,baseRefName,headRefName,baseRefOid,headRefOid,url")
	if err != nil {
		return prView{}, false
	}
	var pull prView
	if err := json.Unmarshal([]byte(output), &pull); err != nil {
		return prView{}, false
	}
	return pull, pull.Number > 0
}

func ParseGitHubRun(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "latest-failed" {
		return 0, nil
	}
	if index := strings.LastIndex(value, "/runs/"); index >= 0 {
		value = strings.Trim(strings.Split(value[index+len("/runs/"):], "/")[0], " ")
	}
	var id int64
	if _, err := fmt.Sscan(value, &id); err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid GitHub run %q", value)
	}
	return id, nil
}
