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
	"strconv"
	"strings"
	"sync"
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
	// Jobs caps concurrently running job containers across all workflows.
	// Zero auto-sizes from the host CPU count.
	Jobs int
}

type Engine struct {
	Store *store.Store
}

func New(store *store.Store) *Engine {
	return &Engine{Store: store}
}

// DefaultConcurrency sizes the global job pool: enough parallelism to
// overlap slow jobs, capped so Docker containers do not thrash the host.
func DefaultConcurrency(cores int) int {
	value := cores / 2
	if value < 2 {
		value = 2
	}
	if value > 8 {
		value = 8
	}
	return value
}

// workflowState is the per-workflow execution context shared by the jobs
// the scheduler dispatches from that workflow.
type workflowState struct {
	plan      *model.Workflow
	parsed    *actmodel.Workflow
	collector *logCollector
	selected  map[string]bool

	startOnce sync.Once
	startedAt time.Time
}

// failureState records the first job failure of the run. Later failures
// observed while the run is being torn down are cancellations, not
// independent signal.
type failureState struct {
	mu         sync.Mutex
	recorded   bool
	workflowID string
	jobID      string
}

func (f *failureState) record(workflowID, jobID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recorded {
		return false
	}
	f.recorded = true
	f.workflowID = workflowID
	f.jobID = jobID
	return true
}

func (f *failureState) first() (string, string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.workflowID, f.jobID, f.recorded
}

// Execute runs every triggered workflow's pending jobs through one global
// fail-as-fast-as-possible queue: jobs across all workflows compete for
// the same weighted slots in rank order, and the first failure cancels
// everything unless options.Complete asks for the full failure set.
func (e *Engine) Execute(ctx context.Context, plan model.Plan, snapshotRoot, eventPath string, run *store.Run, options Options) ([]model.Workflow, error) {
	if options.Out == nil {
		options.Out = os.Stdout
	}
	workflows := append([]model.Workflow(nil), plan.Workflows...)
	if hasErrors(plan.Diagnostics) {
		return workflows, fmt.Errorf("workflow validation failed")
	}
	markDependencyBlocks(workflows)

	slots := options.Jobs
	if slots <= 0 {
		slots = DefaultConcurrency(runtime.NumCPU())
	}

	execCtx, cancelExec := context.WithCancel(ctx)
	defer cancelExec()
	failures := &failureState{}

	values := make([]string, 0, len(options.Secrets))
	for _, value := range options.Secrets {
		values = append(values, value)
	}
	masker := mask.New(values...)

	states, nodes, err := buildSchedule(snapshotRoot, run, workflows, slots, masker, options, failures, cancelExec)
	if err != nil {
		return workflows, err
	}
	if len(nodes) == 0 {
		return workflows, nil
	}

	runner, stopServices, err := e.startServices(execCtx, snapshotRoot, eventPath, plan, run, slots, options)
	if err != nil {
		return workflows, err
	}
	defer stopServices()

	logger := rootLogger()
	cleanupDone := make(chan struct{})
	if !options.Complete {
		go func() {
			select {
			case <-execCtx.Done():
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

	results := runQueue(execCtx, nodes, slots, !options.Complete, func(index int) (bool, error) {
		return e.executeNode(execCtx, runner, logger, run, states, nodes[index], options, failures, cancelExec)
	})
	close(cleanupDone)

	// Evidence first: logs, timings, and step records must be on the model
	// before outcome mapping rewrites the statuses of torn-down steps.
	var evidenceErr error
	for _, state := range states {
		if err := state.collector.Apply(state.plan); err != nil && evidenceErr == nil {
			evidenceErr = err
		}
		reconcilePlannedSteps(state.plan, state.parsed, state.selected, state.collector)
	}
	userCancelled := errors.Is(ctx.Err(), context.Canceled)
	executionErr := applyOutcomes(states, nodes, results, options, failures, userCancelled)
	if executionErr == nil {
		executionErr = evidenceErr
	}
	for _, state := range states {
		if err := completeWorkflow(run, state); err != nil && executionErr == nil {
			executionErr = err
		}
	}
	return workflows, executionErr
}

// buildSchedule parses each runnable workflow once and flattens its
// pending jobs into scheduler nodes wired to per-workflow collectors.
func buildSchedule(snapshotRoot string, run *store.Run, workflows []model.Workflow, slots int, masker *mask.Masker, options Options, failures *failureState, cancelExec context.CancelFunc) ([]*workflowState, []*node, error) {
	var states []*workflowState
	var nodes []*node
	for index := range workflows {
		workflowPlan := &workflows[index]
		if !workflowPlan.Triggered || !hasRunnableJobs(*workflowPlan) {
			continue
		}
		file, err := os.Open(filepath.Join(snapshotRoot, workflowPlan.Path))
		if err != nil {
			return nil, nil, err
		}
		parsed, err := actmodel.ReadWorkflow(file, false)
		file.Close()
		if err != nil {
			return nil, nil, err
		}
		parsed.File = filepath.Base(workflowPlan.Path)
		parsed.Name = "greenrun-" + run.ID + "-" + workflowPlan.Name

		selected := map[string]bool{}
		for _, job := range workflowPlan.Jobs {
			if job.Status == model.StatusPending {
				selected[job.ID] = true
			}
		}
		workflowID := workflowPlan.ID
		onFailure := func(jobID string) {
			if failures.record(workflowID, jobID) && !options.Complete {
				cancelExec()
			}
		}
		state := &workflowState{
			plan:     workflowPlan,
			parsed:   parsed,
			selected: selected,
			collector: newLogCollector(
				masker,
				run,
				options.Out,
				options.Quiet,
				workflowPlan.ID,
				onFailure,
				reusableJobAliases(snapshotRoot, parsed, selected),
				selected,
				reusableFallback(parsed, selected),
			),
		}
		stateIndex := len(states)
		states = append(states, state)

		nodeIndex := map[string]int{}
		for jobIndex := range workflowPlan.Jobs {
			job := &workflowPlan.Jobs[jobIndex]
			if !selected[job.ID] {
				continue
			}
			nodeIndex[job.ID] = len(nodes)
			nodes = append(nodes, &node{
				workflowIndex: stateIndex,
				jobID:         job.ID,
				rank:          job.Rank,
				weight:        jobWeight(parsed.GetJob(job.ID), slots),
			})
		}
		for jobIndex := range workflowPlan.Jobs {
			job := &workflowPlan.Jobs[jobIndex]
			if !selected[job.ID] {
				continue
			}
			current := nodes[nodeIndex[job.ID]]
			for _, need := range job.Needs {
				if dependency, ok := nodeIndex[need]; ok {
					current.needs = append(current.needs, dependency)
				}
			}
		}
	}
	return states, nodes, nil
}

// startServices brings up the run-wide Docker connection, artifact
// endpoints, and actions cache, and returns the shared act runner.
func (e *Engine) startServices(ctx context.Context, snapshotRoot, eventPath string, plan model.Plan, run *store.Run, slots int, options Options) (actrunner.Runner, func(), error) {
	// The embedded servers log through the context logger; without one they
	// fall back to the global info-level logger and announce themselves on
	// every run. Keep them at warnings and above.
	ctx = common.WithLogger(ctx, logrus.NewEntry(rootLogger()))
	socket, err := container.GetSocketAndHost("")
	if err != nil {
		return nil, nil, fmt.Errorf("connect to Docker: %w", err)
	}
	if socket.Host != "" {
		_ = os.Setenv("DOCKER_HOST", socket.Host)
	}

	endpoints, err := containerEndpoints()
	if err != nil {
		return nil, nil, err
	}
	artifactBackendPort, err := freePort()
	if err != nil {
		return nil, nil, err
	}
	artifactPort, err := freePort()
	if err != nil {
		return nil, nil, err
	}
	stopArtifacts := artifacts.Serve(ctx, run.ArtifactsDir, "127.0.0.1", fmt.Sprint(artifactBackendPort))
	stopArtifactProxy, err := startArtifactProxy(ctx, endpoints.BindHost, artifactPort, artifactBackendPort)
	if err != nil {
		stopArtifacts()
		return nil, nil, fmt.Errorf("start artifact compatibility server: %w", err)
	}

	cachePort, err := freePort()
	if err != nil {
		stopArtifactProxy()
		stopArtifacts()
		return nil, nil, err
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
		rootLogger(),
	)
	if err != nil {
		stopArtifactProxy()
		stopArtifacts()
		return nil, nil, fmt.Errorf("start actions cache: %w", err)
	}

	config := &actrunner.Config{
		Actor:                 "greenrun",
		Workdir:               snapshotRoot,
		ActionCacheDir:        filepath.Join(e.Store.Home, "cache", "actions"),
		EventName:             plan.Event.Name,
		EventPath:             eventPath,
		DefaultBranch:         plan.Repository.DefaultBranch,
		LogOutput:             true,
		Env:                   map[string]string{"ACTIONS_CACHE_URL": cache.ExternalURL() + "/"},
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
		ConcurrentJobs:        slots,
	}
	runner, err := actrunner.New(config)
	if err != nil {
		cache.Close()
		stopArtifactProxy()
		stopArtifacts()
		return nil, nil, err
	}
	stop := func() {
		cache.Close()
		stopArtifactProxy()
		stopArtifacts()
	}
	return runner, stop, nil
}

// executeNode runs one job as a single-job act plan against the shared
// parsed workflow model, so dependency results and outputs recorded by
// earlier nodes are visible exactly as they would be on GitHub.
func (e *Engine) executeNode(ctx context.Context, runner actrunner.Runner, logger *logrus.Logger, run *store.Run, states []*workflowState, current *node, options Options, failures *failureState, cancelExec context.CancelFunc) (bool, error) {
	state := states[current.workflowIndex]
	state.startOnce.Do(func() {
		state.startedAt = time.Now().UTC()
		_ = run.AppendEvent(model.EventRecord{
			Time: state.startedAt, Type: "workflow_started", Workflow: state.plan.ID, Status: model.StatusRunning,
		})
	})

	jobCtx := common.WithLogger(ctx, logrus.NewEntry(logger))
	jobCtx = actrunner.WithJobLoggerFactory(jobCtx, state.collector)
	jobCtx = common.WithDryrun(jobCtx, false)

	executionErr := runner.NewPlanExecutor(singleJobPlan(state.parsed, current.jobID, current.weight))(jobCtx)
	job := state.parsed.GetJob(current.jobID)
	failed := job != nil && job.Result == "failure"
	if failed {
		// The act plan reports its own failure as an error; the result on
		// the model is the signal, not the error value.
		executionErr = nil
		if failures.record(state.plan.ID, current.jobID) && !options.Complete {
			cancelExec()
		}
	}
	if errors.Is(executionErr, context.Canceled) {
		executionErr = nil
	}
	return failed, executionErr
}

// applyOutcomes maps every scheduled node back onto the result model with
// honest statuses, and returns the first genuine engine error.
func applyOutcomes(states []*workflowState, nodes []*node, results []nodeResult, options Options, failures *failureState, userCancelled bool) error {
	failedWorkflow, failedJob, hasFailure := failures.first()
	cancelReason := "run cancelled"
	if hasFailure {
		cancelReason = "cancelled after " + failedJob + " failed"
	}
	var executionErr error
	for index, current := range nodes {
		state := states[current.workflowIndex]
		job := findJob(state.plan, current.jobID)
		if job == nil {
			continue
		}
		outcome := results[index]
		if !outcome.dispatched {
			job.Status = model.StatusCancelled
			job.Reason = cancelReason
			continue
		}
		parsedJob := state.parsed.GetJob(current.jobID)
		result := ""
		if parsedJob != nil {
			result = parsedJob.Result
		}
		isFirstFailure := hasFailure && failedWorkflow == state.plan.ID && failedJob == current.jobID
		switch result {
		case "failure":
			if options.Complete || isFirstFailure || !hasFailure {
				job.Status = model.StatusFail
			} else {
				// A job torn down by the first failure's cancellation also
				// reports "failure"; that is teardown, not evidence.
				markCancelled(job, "cancelled after "+failedJob+" failed")
			}
		case "success":
			job.Status = model.StatusPass
		case "skipped":
			job.Status = model.StatusSkip
			job.Reason = skipReason(state.parsed, parsedJob)
		default:
			switch {
			case isFirstFailure:
				// The collector saw this job fail, but the cancellation it
				// triggered tore the plan down before act recorded the
				// result. The failure is the evidence; report it.
				job.Status = model.StatusFail
			case userCancelled || (hasFailure && !options.Complete):
				markCancelled(job, cancelReason)
			case outcome.err != nil:
				job.Status = model.StatusError
				job.ErrorMessage = outcome.err.Error()
			case !state.collector.HasJob(current.jobID):
				job.Status = model.StatusSkip
				job.Reason = "job condition did not run"
			default:
				job.Status = model.StatusPass
			}
		}
		if outcome.err != nil && executionErr == nil {
			executionErr = outcome.err
		}
	}
	return executionErr
}

// completeWorkflow emits the completion event for a workflow that started.
func completeWorkflow(run *store.Run, state *workflowState) error {
	if state.startedAt.IsZero() {
		return nil
	}
	status := model.StatusPass
	for _, job := range state.plan.Jobs {
		if job.Status == model.StatusFail {
			status = model.StatusFail
			break
		}
	}
	return run.AppendEvent(model.EventRecord{
		Time: time.Now().UTC(), Type: "workflow_completed", Workflow: state.plan.ID,
		Status: status, Data: map[string]any{"duration_ms": time.Since(state.startedAt).Milliseconds()},
	})
}

func findJob(workflow *model.Workflow, jobID string) *model.Job {
	for index := range workflow.Jobs {
		if workflow.Jobs[index].ID == jobID {
			return &workflow.Jobs[index]
		}
	}
	return nil
}

func markCancelled(job *model.Job, reason string) {
	job.Status = model.StatusCancelled
	job.Reason = reason
	for stepIndex := range job.Steps {
		if job.Steps[stepIndex].Status == model.StatusFail || job.Steps[stepIndex].Status == model.StatusRunning {
			job.Steps[stepIndex].Status = model.StatusCancelled
		}
	}
}

// skipReason distinguishes a job skipped by its own condition from one
// skipped because a dependency did not succeed. The parsed model's
// recorded results are the authority: they are what act evaluated.
func skipReason(parsed *actmodel.Workflow, parsedJob *actmodel.Job) string {
	if parsedJob != nil {
		for _, need := range parsedJob.Needs() {
			dependency := parsed.GetJob(need)
			if dependency != nil && dependency.Result != "" && dependency.Result != "success" {
				return "dependency " + need + " did not succeed"
			}
		}
	}
	return "job condition did not run"
}

// singleJobPlan wraps one job of the shared workflow model in an act plan.
// The matrix parallelism is pinned to the slot weight the scheduler
// reserved for the job.
func singleJobPlan(parsed *actmodel.Workflow, jobID string, weight int) *actmodel.Plan {
	if job := parsed.GetJob(jobID); job != nil && job.Strategy != nil {
		job.Strategy.MaxParallel = weight
		job.Strategy.MaxParallelString = strconv.Itoa(weight)
	}
	return &actmodel.Plan{Stages: []*actmodel.Stage{{Runs: []*actmodel.Run{{Workflow: parsed, JobID: jobID}}}}}
}

// jobWeight is the number of containers a job occupies while running: its
// resolvable matrix breadth, capped by the workflow's declared
// max-parallel and the global slot count.
func jobWeight(job *actmodel.Job, slots int) int {
	if job == nil || job.Uses != "" || job.Strategy == nil {
		return 1
	}
	matrixes, err := job.GetMatrixes()
	if err != nil || len(matrixes) <= 1 {
		return 1
	}
	weight := len(matrixes)
	if declared := job.Strategy.GetMaxParallel(); declared > 0 && declared < weight {
		weight = declared
	}
	if weight > slots {
		weight = slots
	}
	if weight < 1 {
		weight = 1
	}
	return weight
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
