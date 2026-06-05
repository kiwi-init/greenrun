package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/kiwi-init/greenrun/internal/mask"
	"github.com/kiwi-init/greenrun/internal/model"
	"github.com/kiwi-init/greenrun/internal/store"
	"github.com/nektos/act/pkg/artifactcache"
	"github.com/nektos/act/pkg/artifacts"
	"github.com/nektos/act/pkg/common"
	"github.com/nektos/act/pkg/container"
	actmodel "github.com/nektos/act/pkg/model"
	actrunner "github.com/nektos/act/pkg/runner"
	"github.com/sirupsen/logrus"
)

const (
	defaultImage22 = "ghcr.io/kiwi-init/greenrun-ubuntu:22.04-latest"
	defaultImage24 = "ghcr.io/kiwi-init/greenrun-ubuntu:24.04-latest"
)

type Options struct {
	Complete bool
	Quiet    bool
	Arch     string
	Secrets  map[string]string
	Out      *os.File
}

type Engine struct {
	Store *store.Store
}

func New(store *store.Store) *Engine {
	return &Engine{Store: store}
}

func (e *Engine) Execute(ctx context.Context, plan model.Plan, snapshotRoot, eventPath string, run *store.Run, options Options) ([]model.Workflow, error) {
	if options.Out == nil {
		options.Out = os.Stdout
	}
	workflows := append([]model.Workflow(nil), plan.Workflows...)
	if hasErrors(plan.Diagnostics) {
		return workflows, fmt.Errorf("workflow validation failed")
	}

	markDependencyBlocks(workflows)
	sort.SliceStable(workflows, func(i, j int) bool {
		return workflowRank(workflows[i]) > workflowRank(workflows[j])
	})

	for workflowIndex := range workflows {
		workflowPlan := &workflows[workflowIndex]
		if !workflowPlan.Triggered || !hasRunnableJobs(*workflowPlan) {
			continue
		}
		if err := run.AppendEvent(model.EventRecord{
			Time: time.Now().UTC(), Type: "workflow_started", Workflow: workflowPlan.ID, Status: model.StatusRunning,
		}); err != nil {
			return workflows, err
		}
		failed, err := e.executeWorkflow(ctx, plan, snapshotRoot, eventPath, run, workflowPlan, options)
		if err != nil && !failed {
			return workflows, err
		}
		if failed && !options.Complete {
			cancelRemaining(workflows[workflowIndex+1:])
			break
		}
	}
	return workflows, nil
}

func (e *Engine) executeWorkflow(parent context.Context, wholePlan model.Plan, snapshotRoot, eventPath string, run *store.Run, workflowPlan *model.Workflow, options Options) (bool, error) {
	file, err := os.Open(filepath.Join(snapshotRoot, workflowPlan.Path))
	if err != nil {
		return false, err
	}
	defer file.Close()
	parsed, err := actmodel.ReadWorkflow(file, false)
	if err != nil {
		return false, err
	}
	parsed.File = filepath.Base(workflowPlan.Path)
	parsed.Name = "greenrun-" + run.ID + "-" + workflowPlan.Name
	actPlan, selected, err := makeActPlan(parsed, workflowPlan.Jobs)
	if err != nil {
		return false, err
	}
	if len(selected) == 0 {
		return false, nil
	}

	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	values := make([]string, 0, len(options.Secrets))
	for _, value := range options.Secrets {
		values = append(values, value)
	}
	masker := mask.New(values...)
	aliases := reusableJobAliases(snapshotRoot, parsed, selected)
	collector := newLogCollector(
		masker,
		run,
		options.Out,
		options.Quiet,
		!options.Complete,
		cancel,
		aliases,
		selected,
		reusableFallback(parsed, selected),
	)
	logger := rootLogger()
	ctx = common.WithLogger(ctx, logrus.NewEntry(logger))
	ctx = actrunner.WithJobLoggerFactory(ctx, collector)
	ctx = common.WithDryrun(ctx, false)

	socket, err := container.GetSocketAndHost("")
	if err != nil {
		return false, fmt.Errorf("connect to Docker: %w", err)
	}
	if socket.Host != "" {
		_ = os.Setenv("DOCKER_HOST", socket.Host)
	}

	endpoints, err := containerEndpoints()
	if err != nil {
		return false, err
	}
	artifactBackendPort, err := freePort()
	if err != nil {
		return false, err
	}
	artifactPort, err := freePort()
	if err != nil {
		return false, err
	}
	stopArtifacts := artifacts.Serve(ctx, run.ArtifactsDir, "127.0.0.1", fmt.Sprint(artifactBackendPort))
	defer stopArtifacts()
	stopArtifactProxy, err := startArtifactProxy(
		ctx,
		endpoints.BindHost,
		artifactPort,
		artifactBackendPort,
	)
	if err != nil {
		return false, fmt.Errorf("start artifact compatibility server: %w", err)
	}
	defer stopArtifactProxy()

	cachePort, err := freePort()
	if err != nil {
		return false, err
	}
	cacheExternalURL := ""
	if endpoints.ExternalHost != endpoints.BindHost {
		cacheExternalURL = fmt.Sprintf("http://%s:%d", endpoints.ExternalHost, cachePort)
	}
	cache, err := artifactcache.StartHandler(
		filepath.Join(e.Store.Home, "cache", "artifacts"),
		cacheExternalURL,
		endpoints.BindHost,
		uint16(cachePort),
		logger,
	)
	if err != nil {
		return false, fmt.Errorf("start actions cache: %w", err)
	}
	defer cache.Close()

	env := map[string]string{"ACTIONS_CACHE_URL": cache.ExternalURL() + "/"}
	config := &actrunner.Config{
		Actor:                 "greenrun",
		Workdir:               snapshotRoot,
		ActionCacheDir:        filepath.Join(e.Store.Home, "cache", "actions"),
		EventName:             wholePlan.Event.Name,
		EventPath:             eventPath,
		DefaultBranch:         wholePlan.Repository.DefaultBranch,
		LogOutput:             true,
		Env:                   env,
		Secrets:               options.Secrets,
		Token:                 options.Secrets["GITHUB_TOKEN"],
		Platforms:             platformImages(),
		ContainerArchitecture: architecture(options.Arch),
		ContainerDaemonSocket: socket.Socket,
		UseGitIgnore:          true,
		AutoRemove:            true,
		GitHubInstance:        "github.com",
		RemoteName:            "origin",
		ArtifactServerPath:    run.ArtifactsDir,
		ArtifactServerAddr:    endpoints.ExternalHost,
		ArtifactServerPort:    fmt.Sprint(artifactPort),
		ConcurrentJobs:        2,
	}
	runner, err := actrunner.New(config)
	if err != nil {
		return false, err
	}

	started := time.Now().UTC()
	cleanupDone := make(chan struct{})
	if !options.Complete {
		go func() {
			select {
			case <-ctx.Done():
				timer := time.NewTimer(5 * time.Second)
				defer timer.Stop()
				select {
				case <-timer.C:
					forceCleanup(run.ID)
				case <-cleanupDone:
				}
			case <-cleanupDone:
			}
		}()
	}
	executionErr := runner.NewPlanExecutor(actPlan)(ctx)
	close(cleanupDone)
	failed := collector.Failed()
	if errors.Is(executionErr, context.Canceled) && failed {
		executionErr = nil
	}
	if err := collector.Apply(workflowPlan); err != nil {
		return failed, err
	}
	reconcilePlannedSteps(workflowPlan, parsed, selected, collector)
	firstFailedJob := collector.FailedJob()
	for index := range workflowPlan.Jobs {
		job := &workflowPlan.Jobs[index]
		if !selected[job.ID] {
			continue
		}
		result := parsed.GetJob(job.ID).Result
		if result == "success" {
			if collector.HasJob(job.ID) || parsed.GetJob(job.ID).Uses != "" {
				job.Status = model.StatusPass
				continue
			}
		}
		if failed && !options.Complete && firstFailedJob != "" && job.ID != firstFailedJob {
			job.Status = model.StatusCancelled
			job.Reason = "cancelled after " + firstFailedJob + " failed"
			for stepIndex := range job.Steps {
				if job.Steps[stepIndex].Status == model.StatusFail || job.Steps[stepIndex].Status == model.StatusRunning {
					job.Steps[stepIndex].Status = model.StatusCancelled
				}
			}
			continue
		}
		if !collector.HasJob(job.ID) {
			job.Status = model.StatusSkip
			job.Reason = "job condition did not run"
			continue
		}
		switch result {
		case "failure":
			job.Status = model.StatusFail
			failed = true
		default:
			if failed || errors.Is(parent.Err(), context.Canceled) {
				job.Status = model.StatusCancelled
				if job.Reason == "" {
					job.Reason = "cancelled after first failure"
				}
			} else if executionErr != nil {
				job.Status = model.StatusError
				job.ErrorMessage = executionErr.Error()
			} else {
				job.Status = model.StatusPass
			}
		}
	}
	status := model.StatusPass
	if failed {
		status = model.StatusFail
	}
	_ = run.AppendEvent(model.EventRecord{
		Time: time.Now().UTC(), Type: "workflow_completed", Workflow: workflowPlan.ID,
		Status: status, Data: map[string]any{"duration_ms": time.Since(started).Milliseconds()},
	})
	return failed, executionErr
}

func forceCleanup(runID string) {
	list := exec.Command("docker", "ps", "-aq", "--filter", "name=act-greenrun-"+runID)
	output, err := list.Output()
	if err != nil {
		return
	}
	ids := strings.Fields(string(output))
	if len(ids) == 0 {
		return
	}
	args := append([]string{"rm", "-f"}, ids...)
	_ = exec.Command("docker", args...).Run()
}

func reusableJobAliases(root string, workflow *actmodel.Workflow, selected map[string]bool) map[string]string {
	topLevel := map[string]bool{}
	for _, jobID := range workflow.GetJobIDs() {
		topLevel[jobID] = true
	}
	aliases := map[string]string{}
	for callerID := range selected {
		uses := workflow.GetJob(callerID).Uses
		if !strings.HasPrefix(uses, "./") || strings.Contains(uses, "@") {
			continue
		}
		path := filepath.Clean(filepath.Join(root, strings.TrimPrefix(uses, "./")))
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		child, readErr := actmodel.ReadWorkflow(file, false)
		_ = file.Close()
		if readErr != nil {
			continue
		}
		for _, childID := range child.GetJobIDs() {
			if topLevel[childID] {
				continue
			}
			if existing, exists := aliases[childID]; !exists || existing == callerID {
				aliases[childID] = callerID
			} else {
				delete(aliases, childID)
			}
		}
	}
	return aliases
}

func reusableFallback(workflow *actmodel.Workflow, selected map[string]bool) string {
	fallback := ""
	for jobID := range selected {
		if workflow.GetJob(jobID).Uses == "" {
			continue
		}
		if fallback != "" {
			return ""
		}
		fallback = jobID
	}
	return fallback
}

func reconcilePlannedSteps(workflow *model.Workflow, parsed *actmodel.Workflow, selected map[string]bool, collector *logCollector) {
	for jobIndex := range workflow.Jobs {
		job := &workflow.Jobs[jobIndex]
		if !selected[job.ID] || !collector.HasJob(job.ID) {
			continue
		}
		known := map[string]bool{}
		for _, step := range job.Steps {
			known[strings.TrimSpace(step.Name)] = true
		}
		for _, planned := range parsed.GetJob(job.ID).Steps {
			name := strings.TrimSpace(planned.String())
			if name == "" || strings.Contains(name, "${{") || known[name] {
				continue
			}
			job.Steps = append(job.Steps, model.Step{
				ID:     planned.ID,
				Name:   name,
				Status: model.StatusSkip,
			})
		}
	}
}

func makeActPlan(workflow *actmodel.Workflow, jobs []model.Job) (*actmodel.Plan, map[string]bool, error) {
	selected := map[string]bool{}
	ranks := map[string]float64{}
	for _, job := range jobs {
		if job.Status == model.StatusPending {
			selected[job.ID] = true
			ranks[job.ID] = job.Rank
		}
	}
	stages := []*actmodel.Stage{}
	remaining := map[string]bool{}
	for id := range selected {
		remaining[id] = true
	}
	completed := map[string]bool{}
	for len(remaining) > 0 {
		var ready []string
		for id := range remaining {
			ok := true
			for _, dependency := range workflow.GetJob(id).Needs() {
				if selected[dependency] && !completed[dependency] {
					ok = false
					break
				}
			}
			if ok {
				ready = append(ready, id)
			}
		}
		if len(ready) == 0 {
			return nil, nil, fmt.Errorf("workflow %s contains a dependency cycle", workflow.Name)
		}
		sort.SliceStable(ready, func(i, j int) bool { return ranks[ready[i]] > ranks[ready[j]] })
		stage := &actmodel.Stage{}
		for _, id := range ready {
			if strategy := workflow.GetJob(id).Strategy; strategy != nil {
				strategy.MaxParallel = 2
				strategy.MaxParallelString = "2"
			}
			stage.Runs = append(stage.Runs, &actmodel.Run{Workflow: workflow, JobID: id})
			delete(remaining, id)
			completed[id] = true
		}
		stages = append(stages, stage)
	}
	return &actmodel.Plan{Stages: stages}, selected, nil
}

func markDependencyBlocks(workflows []model.Workflow) {
	for workflowIndex := range workflows {
		jobs := map[string]*model.Job{}
		for jobIndex := range workflows[workflowIndex].Jobs {
			job := &workflows[workflowIndex].Jobs[jobIndex]
			jobs[job.ID] = job
		}
		changed := true
		for changed {
			changed = false
			for _, job := range jobs {
				if job.Status != model.StatusPending {
					continue
				}
				for _, dependency := range job.Needs {
					dep := jobs[dependency]
					if dep != nil && dep.Status != model.StatusPending {
						job.Status = model.StatusBlocked
						job.Fidelity = dep.Fidelity
						job.Reason = "dependency " + dependency + " is " + string(dep.Status)
						changed = true
						break
					}
				}
			}
		}
	}
}

func cancelRemaining(workflows []model.Workflow) {
	for workflowIndex := range workflows {
		for jobIndex := range workflows[workflowIndex].Jobs {
			job := &workflows[workflowIndex].Jobs[jobIndex]
			if job.Status == model.StatusPending {
				job.Status = model.StatusCancelled
				job.Reason = "first failure"
			}
		}
	}
}

func platformImages() map[string]string {
	image22 := os.Getenv("GREENRUN_IMAGE_22")
	if image22 == "" {
		image22 = defaultImage22
	}
	image24 := os.Getenv("GREENRUN_IMAGE_24")
	if image24 == "" {
		image24 = defaultImage24
	}
	return map[string]string{
		"ubuntu-latest": image24,
		"ubuntu-24.04":  image24,
		"ubuntu-22.04":  image22,
	}
}

func Images() []string {
	platforms := platformImages()
	return []string{platforms["ubuntu-22.04"], platforms["ubuntu-24.04"]}
}

func architecture(value string) string {
	if value == "github" {
		return "linux/amd64"
	}
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		return "linux/arm64"
	}
	return ""
}

func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func hasErrors(diagnostics []model.Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Level == "error" {
			return true
		}
	}
	return false
}

func hasRunnableJobs(workflow model.Workflow) bool {
	for _, job := range workflow.Jobs {
		if job.Status == model.StatusPending {
			return true
		}
	}
	return false
}

func workflowRank(workflow model.Workflow) float64 {
	var result float64
	for _, job := range workflow.Jobs {
		if job.Rank > result {
			result = job.Rank
		}
	}
	return result
}

func ResultStatus(workflows []model.Workflow, diagnostics []model.Diagnostic, executionErr error, cancelled bool) (model.Status, model.Fidelity) {
	if cancelled {
		return model.StatusCancelled, lowestFidelity(workflows)
	}
	if executionErr != nil {
		if hasErrors(diagnostics) {
			return model.StatusFail, lowestFidelity(workflows)
		}
		return model.StatusError, lowestFidelity(workflows)
	}
	partial := false
	for _, workflow := range workflows {
		for _, job := range workflow.Jobs {
			switch job.Status {
			case model.StatusFail:
				return model.StatusFail, lowestFidelity(workflows)
			case model.StatusRemote, model.StatusBlocked:
				partial = true
			}
		}
	}
	if partial {
		return model.StatusPartial, lowestFidelity(workflows)
	}
	return model.StatusPass, lowestFidelity(workflows)
}

func lowestFidelity(workflows []model.Workflow) model.Fidelity {
	result := model.FidelityExact
	for _, workflow := range workflows {
		if !workflow.Triggered {
			continue
		}
		result = CombineFidelity(result, workflow.Fidelity)
		for _, job := range workflow.Jobs {
			if job.Status == model.StatusSkip {
				continue
			}
			result = CombineFidelity(result, job.Fidelity)
		}
	}
	return result
}

func CombineFidelity(left, right model.Fidelity) model.Fidelity {
	weight := map[model.Fidelity]int{
		model.FidelityExact:       0,
		model.FidelityCompatible:  1,
		model.FidelityApproximate: 2,
		model.FidelityRemoteOnly:  3,
	}
	if weight[right] > weight[left] {
		return right
	}
	return left
}
