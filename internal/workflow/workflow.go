package workflow

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/kiwi-init/greenrun/internal/model"
	actmodel "github.com/nektos/act/pkg/model"
	"github.com/rhysd/actionlint"
	"gopkg.in/yaml.v3"
)

var secretPattern = regexp.MustCompile(`(?i)secrets\.([A-Z_][A-Z0-9_]*)`)

type Options struct {
	Secrets map[string]string
	Arch    string
}

type rawWorkflow struct {
	Permissions yaml.Node         `yaml:"permissions"`
	Jobs        map[string]rawJob `yaml:"jobs"`
}

type rawJob struct {
	Environment yaml.Node `yaml:"environment"`
	Permissions yaml.Node `yaml:"permissions"`
	Source      yaml.Node `yaml:"-"`
}

func (r *rawJob) UnmarshalYAML(node *yaml.Node) error {
	var fields struct {
		Environment yaml.Node `yaml:"environment"`
		Permissions yaml.Node `yaml:"permissions"`
	}
	if err := node.Decode(&fields); err != nil {
		return err
	}
	r.Environment = fields.Environment
	r.Permissions = fields.Permissions
	r.Source = *node
	return nil
}

func BuildPlan(ctx context.Context, repository model.Repository, event model.Event, root string, options Options) (model.Plan, error) {
	plan := model.Plan{
		SchemaVersion: model.SchemaVersion,
		Repository:    repository,
		Event:         event,
	}
	plan.GeneratedAt = now()

	workflowsDir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(workflowsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return plan, fmt.Errorf("no .github/workflows directory found")
		}
		return plan, err
	}

	linter, err := actionlint.NewLinter(io.Discard, &actionlint.LinterOptions{
		Color:      actionlint.ColorOptionKindNever,
		WorkingDir: root,
	})
	if err != nil {
		return plan, err
	}
	lintErrors, lintErr := linter.LintRepository(root)
	if lintErr != nil {
		plan.Diagnostics = append(plan.Diagnostics, model.Diagnostic{Level: "error", Message: lintErr.Error()})
	}
	for _, lintError := range lintErrors {
		plan.Diagnostics = append(plan.Diagnostics, model.Diagnostic{
			Level: "error", Message: lintError.Message, File: relative(root, lintError.Filepath),
			Line: lintError.Line, Column: lintError.Column,
		})
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || (filepath.Ext(entry.Name()) != ".yml" && filepath.Ext(entry.Name()) != ".yaml") {
			continue
		}
		path := filepath.Join(workflowsDir, entry.Name())
		workflowPlan, readErr := readWorkflow(root, path, repository, event, options)
		if readErr != nil {
			plan.Diagnostics = append(plan.Diagnostics, model.Diagnostic{
				Level: "error", Message: readErr.Error(), File: relative(root, path),
			})
			continue
		}
		plan.Workflows = append(plan.Workflows, workflowPlan)
	}

	if len(plan.Workflows) == 0 {
		return plan, fmt.Errorf("no GitHub Actions workflow files found")
	}
	return plan, nil
}

func readWorkflow(root, path string, repository model.Repository, event model.Event, options Options) (model.Workflow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return model.Workflow{}, err
	}
	parsed, err := actmodel.ReadWorkflow(strings.NewReader(string(data)), false)
	if err != nil {
		return model.Workflow{}, err
	}
	parsed.File = filepath.Base(path)
	name := parsed.Name
	if name == "" {
		name = parsed.File
	}
	triggered, reason, filterFidelity := matchesEvent(parsed, event, repository.ChangedFiles)
	workflowPlan := model.Workflow{
		ID:        strings.TrimSuffix(parsed.File, filepath.Ext(parsed.File)),
		Name:      name,
		Path:      relative(root, path),
		Triggered: triggered,
		Fidelity:  filterFidelity,
		Reason:    reason,
	}

	var raw rawWorkflow
	_ = yaml.Unmarshal(data, &raw)
	workflowOIDC := requestsOIDC(raw.Permissions)

	jobIDs := parsed.GetJobIDs()
	sort.Strings(jobIDs)
	for _, id := range jobIDs {
		job := parsed.GetJob(id)
		fidelity, state, jobReason := classifyJob(job, workflowOIDC || requestsOIDC(raw.Jobs[id].Permissions), raw.Jobs[id].Environment, options.Arch)
		if !triggered {
			state = model.StatusSkip
			jobReason = reason
		}
		jobData, _ := yaml.Marshal(raw.Jobs[id].Source)
		missing := missingSecrets(string(jobData), options.Secrets)
		if state == model.StatusPending && len(missing) > 0 {
			state = model.StatusBlocked
			jobReason = "missing secrets: " + strings.Join(missing, ", ")
		}
		workflowPlan.Jobs = append(workflowPlan.Jobs, model.Job{
			ID:           id,
			Name:         job.Name,
			WorkflowID:   workflowPlan.ID,
			WorkflowName: workflowPlan.Name,
			Runner:       job.RunsOn(),
			Needs:        job.Needs(),
			Uses:         job.Uses,
			Status:       state,
			Fidelity:     fidelity,
			Reason:       jobReason,
			Rank:         rank(id + " " + job.Name),
		})
	}
	return workflowPlan, nil
}

func classifyJob(job *actmodel.Job, oidc bool, environment yaml.Node, arch string) (model.Fidelity, model.Status, string) {
	if oidc {
		return model.FidelityRemoteOnly, model.StatusRemote, "requires GitHub OIDC"
	}
	if environment.Kind != 0 {
		return model.FidelityRemoteOnly, model.StatusRemote, "uses a GitHub environment that may require approval"
	}
	runsOn := job.RunsOn()
	if len(runsOn) == 0 {
		return model.FidelityApproximate, model.StatusPending, "runner label is expression-based"
	}
	for _, label := range runsOn {
		lower := strings.ToLower(label)
		switch {
		case strings.Contains(lower, "${{"):
			return classifyMatrixRunner(job, runsOn, arch)
		case strings.Contains(lower, "macos"), strings.Contains(lower, "windows"), lower == "self-hosted":
			return model.FidelityRemoteOnly, model.StatusRemote, "unsupported runner: " + label
		}
	}
	if !supportedRunner(runsOn) {
		return model.FidelityRemoteOnly, model.StatusRemote, "unsupported runner labels: " + strings.Join(runsOn, ", ")
	}
	if arch != "github" && runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		return model.FidelityCompatible, model.StatusPending, "native arm64 runner image"
	}
	return model.FidelityExact, model.StatusPending, ""
}

func classifyMatrixRunner(job *actmodel.Job, labels []string, arch string) (model.Fidelity, model.Status, string) {
	matrixes, err := job.GetMatrixes()
	if err != nil || len(matrixes) == 0 {
		return model.FidelityRemoteOnly, model.StatusRemote, "runner expression cannot be resolved safely"
	}
	for _, matrix := range matrixes {
		resolved := make([]string, 0, len(labels))
		for _, label := range labels {
			for key, value := range matrix {
				label = strings.ReplaceAll(label, "${{ matrix."+key+" }}", fmt.Sprint(value))
				label = strings.ReplaceAll(label, "${{matrix."+key+"}}", fmt.Sprint(value))
			}
			if strings.Contains(label, "${{") {
				return model.FidelityRemoteOnly, model.StatusRemote, "runner expression cannot be resolved safely"
			}
			resolved = append(resolved, label)
		}
		if !supportedRunner(resolved) {
			return model.FidelityRemoteOnly, model.StatusRemote, "matrix includes unsupported runner: " + strings.Join(resolved, ", ")
		}
	}
	if arch != "github" && runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		return model.FidelityCompatible, model.StatusPending, "Ubuntu-only matrix on native arm64 images"
	}
	return model.FidelityApproximate, model.StatusPending, "Ubuntu-only runner matrix"
}

func supportedRunner(labels []string) bool {
	found := false
	for _, label := range labels {
		switch strings.ToLower(label) {
		case "ubuntu-latest", "ubuntu-24.04", "ubuntu-22.04":
			found = true
		default:
			return false
		}
	}
	return found
}

func matchesEvent(workflow *actmodel.Workflow, event model.Event, changedFiles []string) (bool, string, model.Fidelity) {
	found := false
	for _, candidate := range workflow.On() {
		if candidate == event.Name {
			found = true
			break
		}
	}
	if !found {
		return false, "workflow does not handle " + event.Name, model.FidelityExact
	}
	config := workflow.OnEvent(event.Name)
	mapping, ok := config.(map[string]any)
	if !ok || mapping == nil {
		return true, "", event.Fidelity
	}
	if event.Name == "push" {
		hasTagFilters := len(stringSlice(mapping["tags"])) > 0 || len(stringSlice(mapping["tags-ignore"])) > 0
		hasBranchFilters := len(stringSlice(mapping["branches"])) > 0 || len(stringSlice(mapping["branches-ignore"])) > 0
		if hasTagFilters && !hasBranchFilters {
			return false, "workflow only handles tag pushes", model.FidelityExact
		}
	}
	branch := event.HeadRef
	if event.Name == "pull_request" || event.Name == "pull_request_target" {
		branch = event.BaseRef
	}
	if !matchFilter(mapping, "branches", "branches-ignore", branch) {
		return false, "branch filters do not match " + branch, model.FidelityExact
	}
	if !matchPaths(mapping, changedFiles) {
		return false, "path filters do not match changed files", model.FidelityExact
	}
	return true, "", event.Fidelity
}

func matchFilter(mapping map[string]any, includeKey, ignoreKey, value string) bool {
	if patterns := stringSlice(mapping[includeKey]); len(patterns) > 0 {
		return orderedMatch(patterns, value, false)
	}
	if patterns := stringSlice(mapping[ignoreKey]); len(patterns) > 0 {
		return !orderedMatch(patterns, value, false)
	}
	return true
}

func matchPaths(mapping map[string]any, files []string) bool {
	includes := stringSlice(mapping["paths"])
	ignores := stringSlice(mapping["paths-ignore"])
	if len(includes) == 0 && len(ignores) == 0 {
		return true
	}
	for _, file := range files {
		if len(includes) > 0 && orderedMatch(includes, filepath.ToSlash(file), false) {
			return true
		}
		if len(ignores) > 0 && !orderedMatch(ignores, filepath.ToSlash(file), false) {
			return true
		}
	}
	return false
}

func orderedMatch(patterns []string, value string, initial bool) bool {
	matched := initial
	for _, pattern := range patterns {
		negative := strings.HasPrefix(pattern, "!")
		pattern = strings.TrimPrefix(pattern, "!")
		ok, err := doublestar.Match(pattern, value)
		if err != nil || !ok {
			continue
		}
		matched = !negative
	}
	return matched
}

func stringSlice(value any) []string {
	switch current := value.(type) {
	case string:
		return []string{current}
	case []string:
		return current
	case []any:
		values := make([]string, 0, len(current))
		for _, item := range current {
			if text, ok := item.(string); ok {
				values = append(values, text)
			}
		}
		return values
	default:
		return nil
	}
}

func requestsOIDC(node yaml.Node) bool {
	if node.Kind == 0 {
		return false
	}
	var value any
	if err := node.Decode(&value); err != nil {
		return false
	}
	return containsOIDC(value)
}

func containsOIDC(value any) bool {
	switch current := value.(type) {
	case map[string]any:
		for key, nested := range current {
			if strings.EqualFold(key, "id-token") && strings.EqualFold(fmt.Sprint(nested), "write") {
				return true
			}
			if containsOIDC(nested) {
				return true
			}
		}
	case map[any]any:
		for key, nested := range current {
			if strings.EqualFold(fmt.Sprint(key), "id-token") && strings.EqualFold(fmt.Sprint(nested), "write") {
				return true
			}
			if containsOIDC(nested) {
				return true
			}
		}
	}
	return false
}

func missingSecrets(content string, supplied map[string]string) []string {
	matches := secretPattern.FindAllStringSubmatch(content, -1)
	set := map[string]struct{}{}
	for _, match := range matches {
		name := strings.ToUpper(match[1])
		if name == "GITHUB_TOKEN" {
			continue
		}
		if _, ok := supplied[name]; !ok {
			set[name] = struct{}{}
		}
	}
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func rank(value string) float64 {
	value = strings.ToLower(value)
	switch {
	case strings.Contains(value, "lint"), strings.Contains(value, "type"), strings.Contains(value, "check"):
		return 100
	case strings.Contains(value, "unit"):
		return 90
	case strings.Contains(value, "test"):
		return 80
	case strings.Contains(value, "build"):
		return 60
	case strings.Contains(value, "integration"):
		return 50
	case strings.Contains(value, "e2e"), strings.Contains(value, "end-to-end"):
		return 30
	case strings.Contains(value, "deploy"), strings.Contains(value, "release"):
		return 10
	default:
		return 70
	}
}

func relative(root, path string) string {
	value, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(value)
}

var now = func() time.Time { return time.Now().UTC() }
