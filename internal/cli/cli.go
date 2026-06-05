package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/kiwi-init/greenrun/internal/engine"
	"github.com/kiwi-init/greenrun/internal/event"
	"github.com/kiwi-init/greenrun/internal/executil"
	"github.com/kiwi-init/greenrun/internal/githubrun"
	"github.com/kiwi-init/greenrun/internal/history"
	"github.com/kiwi-init/greenrun/internal/model"
	"github.com/kiwi-init/greenrun/internal/output"
	"github.com/kiwi-init/greenrun/internal/repo"
	"github.com/kiwi-init/greenrun/internal/snapshot"
	"github.com/kiwi-init/greenrun/internal/store"
	"github.com/kiwi-init/greenrun/internal/workflow"
	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
)

type app struct {
	ctx     context.Context
	version string
	out     *os.File
	errOut  *os.File
	store   *store.Store

	plain      bool
	quiet      bool
	eventName  string
	arch       string
	secretArgs []string
	secretFile string
}

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func Execute(ctx context.Context, version string, args []string) int {
	a := &app{
		ctx: ctx, version: version, out: os.Stdout, errOut: os.Stderr, store: store.New(),
	}
	command := a.rootCommand()
	command.SetArgs(args)
	err := command.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	var exit *exitError
	if errors.As(err, &exit) {
		if exit.err != nil && exit.code == 2 {
			output.New(a.out, a.errOut, a.plain).Error(exit.err)
		}
		return exit.code
	}
	output.New(a.out, a.errOut, a.plain).Error(err)
	return 2
}

func (a *app) rootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "greenrun",
		Short:         "Fail GitHub Actions as fast as possible",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       a.version,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runLocal(cmd, false)
		},
	}
	root.PersistentFlags().BoolVar(&a.plain, "plain", false, "disable color and interactive formatting")
	root.PersistentFlags().BoolVarP(&a.quiet, "quiet", "q", false, "hide live workflow logs")
	root.PersistentFlags().StringVar(&a.eventName, "event", "", "override the inferred GitHub event")
	root.PersistentFlags().StringVar(&a.arch, "arch", "native", "runner architecture: native or github")
	root.PersistentFlags().StringArrayVarP(&a.secretArgs, "secret", "s", nil, "provide NAME or NAME=value to workflow jobs")
	root.PersistentFlags().StringVar(&a.secretFile, "secret-file", "", "explicit dotenv-style secrets file")
	root.PersistentPreRunE = func(_ *cobra.Command, _ []string) error {
		if a.arch != "native" && a.arch != "github" {
			return fmt.Errorf("--arch must be native or github")
		}
		return nil
	}

	run := &cobra.Command{Use: "run", Short: "Run applicable GitHub Actions workflows"}
	var complete bool
	run.Flags().BoolVar(&complete, "complete", false, "collect all workflow failures")
	run.RunE = func(cmd *cobra.Command, _ []string) error { return a.runLocal(cmd, complete) }

	plan := &cobra.Command{Use: "plan", Short: "Show inferred workflows, jobs, and fidelity", RunE: a.plan}

	show := &cobra.Command{Use: "show [run]", Short: "Show a saved result", Args: cobra.MaximumNArgs(1)}
	var showJSON bool
	show.Flags().BoolVar(&showJSON, "json", false, "print canonical JSON")
	show.RunE = func(cmd *cobra.Command, args []string) error { return a.show(cmd, args, showJSON) }

	logs := &cobra.Command{Use: "logs [run]", Short: "Show saved workflow logs", Args: cobra.MaximumNArgs(1)}
	var failedOnly bool
	logs.Flags().BoolVar(&failedOnly, "failed", false, "show logs only for failed jobs")
	logs.RunE = func(cmd *cobra.Command, args []string) error { return a.logs(cmd, args, failedOnly) }

	path := &cobra.Command{Use: "path [run]", Short: "Print a saved run directory", Args: cobra.MaximumNArgs(1), RunE: a.path}

	github := &cobra.Command{
		Use: "github [URL|ID|latest-failed]", Short: "Import a GitHub Actions run", Args: cobra.MaximumNArgs(1),
	}
	var watch, artifacts bool
	github.Flags().BoolVar(&watch, "watch", false, "wait for the remote run to finish")
	github.Flags().BoolVar(&artifacts, "artifacts", false, "download artifact contents")
	github.RunE = func(cmd *cobra.Command, args []string) error {
		reference := argument(args, "latest-failed")
		return a.importGitHub(cmd, reference, watch, artifacts)
	}

	root.AddCommand(
		run, plan, show, logs, path, github,
		&cobra.Command{Use: "warm", Short: "Pre-pull runner images", RunE: a.warm},
		&cobra.Command{Use: "doctor", Short: "Check Greenrun prerequisites", RunE: a.doctor},
		&cobra.Command{Use: "update", Short: "Install the latest verified release", RunE: a.update},
		&cobra.Command{Use: "version", Short: "Print the Greenrun version", Run: func(_ *cobra.Command, _ []string) {
			fmt.Fprintln(a.out, a.version)
		}},
	)
	return root
}

func (a *app) runLocal(cmd *cobra.Command, complete bool) error {
	started := time.Now().UTC()
	printer := output.New(a.out, a.errOut, a.plain)
	repository, err := repo.Discover(cmd.Context(), ".")
	if err != nil {
		return &exitError{code: 2, err: err}
	}
	secrets, err := a.secrets()
	if err != nil {
		return &exitError{code: 2, err: err}
	}
	snap, err := snapshot.Create(cmd.Context(), repository)
	if err != nil {
		return &exitError{code: 2, err: err}
	}
	defer snap.Close()
	run, err := a.store.Start(repository)
	if err != nil {
		return &exitError{code: 2, err: err}
	}
	inferred := event.Infer(cmd.Context(), repository, snap.HeadSHA, a.eventName)
	eventPath, err := event.Write(run.Directory, inferred)
	if err != nil {
		return &exitError{code: 2, err: err}
	}
	plan, planErr := workflow.BuildPlan(cmd.Context(), repository, inferred, snap.Root, workflow.Options{Secrets: secrets, Arch: a.arch})
	history.Apply(&plan, history.Load(a.store, repository))
	if planErr != nil {
		plan.Diagnostics = append(plan.Diagnostics, model.Diagnostic{Level: "error", Message: planErr.Error()})
	}
	if err := run.WriteJSON("plan.json", plan); err != nil {
		return &exitError{code: 2, err: err}
	}
	_ = run.AppendEvent(model.EventRecord{
		Time: time.Now().UTC(), Type: "run_started", Status: model.StatusRunning,
		Data: map[string]any{"event": inferred.Name, "source": model.SourceLocal},
	})
	printer.Header(repository, inferred)

	workflows := plan.Workflows
	var executionErr error
	if planErr == nil {
		workflows, executionErr = engine.New(a.store).Execute(cmd.Context(), plan, snap.Root, eventPath, run, engine.Options{
			Complete: complete, Quiet: a.quiet, Arch: a.arch, Secrets: secrets, Out: a.out,
		})
	}
	cancelled := errors.Is(cmd.Context().Err(), context.Canceled)
	status, fidelity := engine.ResultStatus(workflows, plan.Diagnostics, firstError(planErr, executionErr), cancelled)
	fidelity = engine.CombineFidelity(fidelity, inferred.Fidelity)
	completed := time.Now().UTC()
	result := model.Result{
		SchemaVersion: model.SchemaVersion, ID: run.ID, Source: model.SourceLocal,
		Status: status, Fidelity: fidelity, StartedAt: started, CompletedAt: completed,
		DurationMS: completed.Sub(started).Milliseconds(), Repository: repository, Event: inferred,
		Workflows: workflows, Diagnostics: plan.Diagnostics, Reproduce: "greenrun", RunDirectory: run.Directory,
		Artifacts: engine.CollectArtifacts(run.ArtifactsDir),
	}
	if executionErr != nil && !cancelled {
		result.Diagnostics = append(result.Diagnostics, model.Diagnostic{Level: "error", Message: executionErr.Error()})
	}
	if err := run.Complete(result, output.Compact(result)); err != nil {
		return &exitError{code: 2, err: err}
	}
	_ = history.Update(a.store, result)
	printer.Result(result)
	if status == model.StatusPass {
		return nil
	}
	return &exitError{code: model.ExitCode(status), err: fmt.Errorf("run completed with status %s", status)}
}

func (a *app) plan(cmd *cobra.Command, _ []string) error {
	repository, err := repo.Discover(cmd.Context(), ".")
	if err != nil {
		return err
	}
	secrets, err := a.secrets()
	if err != nil {
		return err
	}
	snap, err := snapshot.Create(cmd.Context(), repository)
	if err != nil {
		return err
	}
	defer snap.Close()
	inferred := event.Infer(cmd.Context(), repository, snap.HeadSHA, a.eventName)
	plan, buildErr := workflow.BuildPlan(cmd.Context(), repository, inferred, snap.Root, workflow.Options{Secrets: secrets, Arch: a.arch})
	history.Apply(&plan, history.Load(a.store, repository))
	output.New(a.out, a.errOut, a.plain).Plan(plan)
	if buildErr != nil {
		return buildErr
	}
	for _, diagnostic := range plan.Diagnostics {
		if diagnostic.Level == "error" {
			return &exitError{code: 1, err: fmt.Errorf("workflow validation failed")}
		}
	}
	return nil
}

func (a *app) show(cmd *cobra.Command, args []string, asJSON bool) error {
	repository, err := repo.Discover(cmd.Context(), ".")
	if err != nil {
		return err
	}
	directory, err := a.store.Resolve(repository, argument(args, "latest"))
	if err != nil {
		return err
	}
	name := "result.gr"
	if asJSON {
		name = "result.json"
	}
	data, err := os.ReadFile(filepath.Join(directory, name))
	if err != nil {
		return err
	}
	_, err = a.out.Write(data)
	return err
}

func (a *app) logs(cmd *cobra.Command, args []string, failedOnly bool) error {
	repository, err := repo.Discover(cmd.Context(), ".")
	if err != nil {
		return err
	}
	result, err := a.store.ReadResult(repository, argument(args, "latest"))
	if err != nil {
		return err
	}
	printed := map[string]bool{}
	for _, workflowValue := range result.Workflows {
		for _, job := range workflowValue.Jobs {
			if job.Log == "" || (failedOnly && job.Status != model.StatusFail) || printed[job.Log] {
				continue
			}
			data, readErr := os.ReadFile(filepath.Join(result.RunDirectory, filepath.FromSlash(job.Log)))
			if readErr != nil {
				return readErr
			}
			fmt.Fprintf(a.out, "==> %s / %s <==\n", workflowValue.Name, job.Name)
			_, _ = a.out.Write(data)
			printed[job.Log] = true
		}
	}
	if len(printed) == 0 {
		return fmt.Errorf("no matching logs found")
	}
	return nil
}

func (a *app) path(cmd *cobra.Command, args []string) error {
	repository, err := repo.Discover(cmd.Context(), ".")
	if err != nil {
		return err
	}
	directory, err := a.store.Resolve(repository, argument(args, "latest"))
	if err != nil {
		return err
	}
	fmt.Fprintln(a.out, directory)
	return nil
}

func (a *app) importGitHub(cmd *cobra.Command, reference string, watch, artifacts bool) error {
	repository, err := repo.Discover(cmd.Context(), ".")
	if err != nil {
		return err
	}
	secrets, err := a.secrets()
	if err != nil {
		return err
	}
	run, err := a.store.Start(repository)
	if err != nil {
		return err
	}
	result, err := githubrun.Import(cmd.Context(), repository, run, githubrun.Options{
		Reference: reference, Watch: watch, Artifacts: artifacts, Secrets: secrets,
	})
	if err != nil {
		return err
	}
	output.New(a.out, a.errOut, a.plain).Result(result)
	if result.Status == model.StatusPass {
		return nil
	}
	return &exitError{code: model.ExitCode(result.Status), err: fmt.Errorf("remote run is %s", result.Status)}
}

func (a *app) warm(cmd *cobra.Command, _ []string) error {
	if !executil.Exists("docker") {
		return fmt.Errorf("Docker is required")
	}
	images := engine.Images()
	sort.Strings(images)
	for _, image := range images {
		fmt.Fprintf(a.out, "pulling %s\n", image)
		args := []string{"pull"}
		if a.arch == "github" {
			args = append(args, "--platform", "linux/amd64")
		}
		args = append(args, image)
		command := exec.CommandContext(cmd.Context(), "docker", args...)
		command.Stdout, command.Stderr = a.out, a.errOut
		if err := command.Run(); err != nil {
			return err
		}
	}
	return nil
}

func (a *app) doctor(cmd *cobra.Command, _ []string) error {
	type check struct {
		name     string
		required bool
		run      func() error
	}
	checks := []check{
		{"git", true, func() error { return commandVersion(cmd.Context(), "git", "--version") }},
		{"docker", true, func() error { return executil.Run(cmd.Context(), ".", "docker", "info") }},
		{"disk", true, func() error { return checkDisk(a.store.Home, 5<<30) }},
		{"gh", false, func() error { return executil.Run(cmd.Context(), ".", "gh", "auth", "status") }},
		{"repository", true, func() error { _, err := repo.Discover(cmd.Context(), "."); return err }},
	}
	failed := false
	for _, current := range checks {
		if err := current.run(); err != nil {
			label := "WARN"
			if current.required {
				label = "FAIL"
				failed = true
			}
			fmt.Fprintf(a.out, "%-4s %-12s %s\n", label, current.name, err)
		} else {
			fmt.Fprintf(a.out, "PASS %-12s\n", current.name)
		}
	}
	fmt.Fprintf(a.out, "INFO host         %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(a.out, "INFO store        %s\n", a.store.Home)
	if failed {
		return &exitError{code: 2, err: fmt.Errorf("required checks failed")}
	}
	return nil
}

func (a *app) update(cmd *cobra.Command, _ []string) error {
	url := os.Getenv("GREENRUN_INSTALL_URL")
	if url == "" {
		url = "https://greenrun.sh/install.sh"
	}
	temp, err := os.CreateTemp("", "greenrun-install-*.sh")
	if err != nil {
		return err
	}
	name := temp.Name()
	temp.Close()
	defer os.Remove(name)
	if !executil.Exists("curl") {
		return fmt.Errorf("curl is required to update Greenrun")
	}
	if err := executil.Run(cmd.Context(), ".", "curl", "-fsSL", url, "-o", name); err != nil {
		return err
	}
	command := exec.CommandContext(cmd.Context(), "sh", name)
	command.Stdout, command.Stderr = a.out, a.errOut
	return command.Run()
}

func (a *app) secrets() (map[string]string, error) {
	values := map[string]string{}
	if a.secretFile != "" {
		file, err := os.Open(a.secretFile)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			name, value, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
			if !ok {
				return nil, fmt.Errorf("%s: expected NAME=value", a.secretFile)
			}
			values[strings.ToUpper(strings.TrimSpace(name))] = strings.Trim(strings.TrimSpace(value), `"'`)
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
	}
	for _, item := range a.secretArgs {
		name, value, ok := strings.Cut(item, "=")
		name = strings.ToUpper(strings.TrimSpace(name))
		if name == "" {
			return nil, fmt.Errorf("empty secret name")
		}
		if !ok {
			value, ok = os.LookupEnv(name)
			if !ok {
				return nil, fmt.Errorf("secret %s is not set in the environment", name)
			}
		}
		values[name] = value
	}
	return values, nil
}

func commandVersion(ctx context.Context, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Run()
}

func checkDisk(path string, minimum uint64) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return err
	}
	available := uint64(stat.Bavail) * uint64(stat.Bsize)
	if available < minimum {
		return fmt.Errorf("%.1f GiB available; at least %.1f GiB required",
			float64(available)/(1<<30), float64(minimum)/(1<<30))
	}
	return nil
}

func argument(args []string, fallback string) string {
	if len(args) > 0 {
		return args[0]
	}
	return fallback
}

func firstError(values ...error) error {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}
