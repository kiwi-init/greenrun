package engine

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	greenmask "github.com/kiwi-init/greenrun/internal/mask"
	"github.com/kiwi-init/greenrun/internal/model"
	"github.com/kiwi-init/greenrun/internal/store"
	actrunner "github.com/nektos/act/pkg/runner"
	"github.com/sirupsen/logrus"
)

type logCollector struct {
	masker     *greenmask.Masker
	run        *store.Run
	out        io.Writer
	quiet      bool
	workflowID string
	onFailure  func(job string)
	aliases    map[string]string
	knownJobs  map[string]bool
	fallback   string

	mu          sync.Mutex
	buffers     map[string]*bytes.Buffer
	started     map[string]time.Time
	finished    map[string]time.Time
	steps       map[string]map[string]*model.Step
	diagnostics map[string][]model.Diagnostic
}

func newLogCollector(
	masker *greenmask.Masker,
	run *store.Run,
	out io.Writer,
	quiet bool,
	workflowID string,
	onFailure func(job string),
	aliases map[string]string,
	knownJobs map[string]bool,
	fallback string,
) *logCollector {
	return &logCollector{
		masker: masker, run: run, out: out, quiet: quiet, workflowID: workflowID, onFailure: onFailure,
		buffers: map[string]*bytes.Buffer{}, started: map[string]time.Time{},
		finished: map[string]time.Time{}, steps: map[string]map[string]*model.Step{},
		diagnostics: map[string][]model.Diagnostic{}, aliases: aliases, knownJobs: knownJobs, fallback: fallback,
	}
}

func (c *logCollector) WithJobLogger() *logrus.Logger {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	logger.SetLevel(logrus.InfoLevel)
	logger.AddHook(c)
	return logger
}

func (c *logCollector) Levels() []logrus.Level {
	return logrus.AllLevels
}

func (c *logCollector) Fire(entry *logrus.Entry) error {
	now := time.Now().UTC()
	job := fieldString(entry.Data["jobID"])
	if job == "" {
		job = fieldString(entry.Data["job"])
	}
	if alias := c.aliases[job]; alias != "" {
		job = alias
	} else if job != "" && c.fallback != "" && !c.knownJobs[job] {
		job = c.fallback
	}
	step := fieldString(entry.Data["step"])
	message := entry.Message
	c.masker.Observe(message)
	if entry.Context != nil {
		for _, value := range *actrunner.Masks(entry.Context) {
			c.masker.Add(value)
		}
	}
	message = c.masker.Apply(message)

	c.mu.Lock()
	defer c.mu.Unlock()
	if job == "" {
		job = "greenrun"
	}
	if _, ok := c.started[job]; !ok {
		c.started[job] = now
	}
	c.finished[job] = now
	buffer := c.buffers[job]
	if buffer == nil {
		buffer = &bytes.Buffer{}
		c.buffers[job] = buffer
	}
	fmt.Fprintln(buffer, message)
	if diagnostic, ok := parseAnnotation(message); ok {
		c.diagnostics[job] = append(c.diagnostics[job], diagnostic)
	}
	if !c.quiet {
		if entry.Data["raw_output"] == true {
			fmt.Fprintf(c.out, "[%s] | %s\n", job, message)
		} else {
			fmt.Fprintf(c.out, "[%s] %s\n", job, message)
		}
	}

	if step != "" {
		if c.steps[job] == nil {
			c.steps[job] = map[string]*model.Step{}
		}
		current := c.steps[job][step]
		if current == nil {
			started := now
			current = &model.Step{Name: step, Status: model.StatusRunning, StartedAt: &started}
			c.steps[job][step] = current
		}
		if status := fieldString(entry.Data["stepResult"]); status != "" {
			completed := now
			current.CompletedAt = &completed
			current.DurationMS = completed.Sub(*current.StartedAt).Milliseconds()
			switch strings.ToLower(status) {
			case "success":
				current.Status = model.StatusPass
			case "failure":
				current.Status = model.StatusFail
			default:
				current.Status = model.Status(status)
			}
		}
	}

	failure := strings.EqualFold(fieldString(entry.Data["stepResult"]), "failure") ||
		(entry.Level <= logrus.ErrorLevel && job != "greenrun")
	if failure && c.onFailure != nil {
		c.onFailure(job)
	}
	_ = c.run.AppendEvent(model.EventRecord{
		Time: now, Type: "log", Workflow: c.workflowID, Job: job, Step: step, Message: message,
	})
	return nil
}

func (c *logCollector) HasJob(job string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.started[job]
	return ok
}

func (c *logCollector) Apply(workflow *model.Workflow) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for index := range workflow.Jobs {
		job := &workflow.Jobs[index]
		buffer := c.buffers[job.ID]
		if buffer != nil {
			path, err := c.run.WriteLog(workflow.ID+"-"+job.ID, buffer.String())
			if err != nil {
				return err
			}
			job.Log = filepath.ToSlash(path)
		}
		if started, ok := c.started[job.ID]; ok {
			value := started
			job.StartedAt = &value
		}
		if finished, ok := c.finished[job.ID]; ok {
			value := finished
			job.CompletedAt = &value
			if job.StartedAt != nil {
				job.DurationMS = value.Sub(*job.StartedAt).Milliseconds()
			}
		}
		if steps := c.steps[job.ID]; len(steps) > 0 {
			job.Steps = make([]model.Step, 0, len(steps))
			for _, step := range steps {
				job.Steps = append(job.Steps, *step)
			}
			sort.SliceStable(job.Steps, func(i, j int) bool {
				left, right := job.Steps[i], job.Steps[j]
				if left.StartedAt == nil {
					return false
				}
				if right.StartedAt == nil {
					return true
				}
				if left.StartedAt.Equal(*right.StartedAt) {
					return left.Name < right.Name
				}
				return left.StartedAt.Before(*right.StartedAt)
			})
		}
		job.Diagnostics = append(job.Diagnostics, c.diagnostics[job.ID]...)
	}
	return nil
}

func parseAnnotation(message string) (model.Diagnostic, bool) {
	start := strings.Index(message, "::error")
	if start < 0 {
		return model.Diagnostic{}, false
	}
	command := message[start+len("::error"):]
	meta, body, ok := strings.Cut(command, "::")
	if !ok {
		return model.Diagnostic{}, false
	}
	diagnostic := model.Diagnostic{Level: "error", Message: strings.TrimSpace(body)}
	meta = strings.TrimPrefix(meta, " ")
	for _, part := range strings.Split(meta, ",") {
		key, value, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found {
			continue
		}
		switch key {
		case "file":
			diagnostic.File = value
		case "line":
			_, _ = fmt.Sscan(value, &diagnostic.Line)
		case "col", "column":
			_, _ = fmt.Sscan(value, &diagnostic.Column)
		}
	}
	return diagnostic, diagnostic.Message != ""
}

func fieldString(value any) string {
	switch current := value.(type) {
	case string:
		return strings.TrimSpace(current)
	case []string:
		return strings.Join(current, "/")
	case fmt.Stringer:
		return current.String()
	default:
		if value == nil {
			return ""
		}
		return fmt.Sprint(value)
	}
}

func rootLogger() *logrus.Logger {
	logger := logrus.New()
	logger.SetOutput(os.Stderr)
	logger.SetLevel(logrus.WarnLevel)
	logger.SetFormatter(&logrus.TextFormatter{DisableTimestamp: true, DisableColors: true})
	return logger
}
