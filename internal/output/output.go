package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kiwi-init/greenrun/internal/model"
	"golang.org/x/term"
)

type Printer struct {
	Out   io.Writer
	Err   io.Writer
	Color bool
	Plain bool
}

func New(out, errOut io.Writer, plain bool) *Printer {
	color := false
	if file, ok := out.(*os.File); ok {
		color = term.IsTerminal(int(file.Fd())) && os.Getenv("NO_COLOR") == ""
	}
	return &Printer{Out: out, Err: errOut, Color: color && !plain, Plain: plain}
}

func (p *Printer) Header(repository model.Repository, event model.Event) {
	fmt.Fprintf(p.Out, "%s %s  event=%s  branch=%s\n",
		p.bold("greenrun"), repository.Slug, event.Name, repository.Branch)
}

func (p *Printer) Info(format string, args ...any) {
	fmt.Fprintf(p.Out, format+"\n", args...)
}

func (p *Printer) Warn(format string, args ...any) {
	fmt.Fprintf(p.Err, "%s %s\n", p.yellow("warning:"), fmt.Sprintf(format, args...))
}

func (p *Printer) Error(err error) {
	fmt.Fprintf(p.Err, "%s %s\n", p.red("greenrun:"), err)
}

func (p *Printer) Result(result model.Result) {
	label := strings.ToUpper(string(result.Status))
	switch result.Status {
	case model.StatusPass:
		label = p.green(label)
	case model.StatusFail, model.StatusError:
		label = p.red(label)
	case model.StatusPartial, model.StatusCancelled:
		label = p.yellow(label)
	}
	fmt.Fprintf(p.Out, "\n%s in %s\n", label, duration(result.DurationMS))
	if result.Reproduce != "" {
		fmt.Fprintf(p.Out, "Reproduce: %s\n", result.Reproduce)
	}
	fmt.Fprintf(p.Out, "Result: %s\n", filepath.Join(result.RunDirectory, "result.gr"))
}

func (p *Printer) Plan(plan model.Plan) {
	p.Header(plan.Repository, plan.Event)
	for _, diagnostic := range plan.Diagnostics {
		location := ""
		if diagnostic.File != "" {
			location = fmt.Sprintf(" %s:%d:%d", diagnostic.File, diagnostic.Line, diagnostic.Column)
		}
		fmt.Fprintf(p.Out, "%s%s %s\n", strings.ToUpper(diagnostic.Level), location, diagnostic.Message)
	}
	for _, workflow := range plan.Workflows {
		state := "run"
		if !workflow.Triggered {
			state = "skip"
		}
		fmt.Fprintf(p.Out, "\n%s  %s  [%s]\n", workflow.Name, workflow.Path, state)
		for _, job := range workflow.Jobs {
			fmt.Fprintf(p.Out, "  %-12s %-28s %s", strings.ToUpper(string(job.Status)), job.ID, job.Fidelity)
			if job.Reason != "" {
				fmt.Fprintf(p.Out, "  %s", job.Reason)
			}
			fmt.Fprintln(p.Out)
		}
	}
}

func Compact(result model.Result) string {
	var builder strings.Builder
	fmt.Fprintln(&builder, "greenrun 1")
	fmt.Fprintf(&builder, "run %s %s %d %s\n", result.Status, result.Source, result.DurationMS, result.ID)
	fmt.Fprintf(&builder, "repo %s %s %s\n", result.Repository.Slug, result.Event.Name, result.Fidelity)
	if result.RemoteURL != "" {
		fmt.Fprintf(&builder, "url %s\n", quote(result.RemoteURL))
	}
	for _, workflow := range result.Workflows {
		for _, job := range workflow.Jobs {
			fmt.Fprintf(&builder, "%s %s/%s %d", compactStatus(job.Status), workflow.ID, job.ID, job.DurationMS)
			if job.Reason != "" {
				fmt.Fprintf(&builder, " reason=%s", quote(job.Reason))
			}
			fmt.Fprintln(&builder)
			for _, diagnostic := range job.Diagnostics {
				if diagnostic.File != "" {
					fmt.Fprintf(&builder, "at %s:%d:%d\n", diagnostic.File, diagnostic.Line, diagnostic.Column)
				}
				fmt.Fprintf(&builder, "msg %s\n", quote(diagnostic.Message))
			}
			if job.ErrorMessage != "" {
				fmt.Fprintf(&builder, "msg %s\n", quote(job.ErrorMessage))
			}
			if job.Log != "" {
				fmt.Fprintf(&builder, "log %s\n", job.Log)
			}
			for _, step := range job.Steps {
				fmt.Fprintf(&builder, "step %s %s/%s/%s %d\n",
					compactStatus(step.Status), workflow.ID, job.ID, compactID(step.Name), step.DurationMS)
				for _, diagnostic := range step.Diagnostics {
					if diagnostic.File != "" {
						fmt.Fprintf(&builder, "at %s:%d:%d\n", diagnostic.File, diagnostic.Line, diagnostic.Column)
					}
					fmt.Fprintf(&builder, "msg %s\n", quote(diagnostic.Message))
				}
			}
		}
	}
	for _, artifact := range result.Artifacts {
		fmt.Fprintf(&builder, "artifact %s %d", quote(artifact.Name), artifact.SizeBytes)
		if artifact.DownloadedTo != "" {
			fmt.Fprintf(&builder, " path=%s", quote(artifact.DownloadedTo))
		}
		fmt.Fprintln(&builder)
	}
	for _, diagnostic := range result.Diagnostics {
		fmt.Fprintf(&builder, "diag %s %s\n", diagnostic.Level, quote(diagnostic.Message))
	}
	return builder.String()
}

func JSON(value any) ([]byte, error) {
	return json.MarshalIndent(value, "", "  ")
}

func duration(ms int64) string {
	if ms < 1000 {
		return strconv.FormatInt(ms, 10) + "ms"
	}
	seconds := float64(ms) / 1000
	if seconds < 60 {
		return fmt.Sprintf("%.1fs", seconds)
	}
	return fmt.Sprintf("%dm%.1fs", int(seconds)/60, seconds-float64(int(seconds)/60*60))
}

func compactStatus(status model.Status) string {
	switch status {
	case model.StatusPass:
		return "PASS"
	case model.StatusFail:
		return "FAIL"
	case model.StatusSkip:
		return "SKIP"
	case model.StatusBlocked:
		return "BLOCK"
	case model.StatusRemote:
		return "REMOTE"
	case model.StatusCancelled:
		return "CANCEL"
	default:
		return strings.ToUpper(string(status))
	}
}

func quote(value string) string {
	return strconv.Quote(strings.TrimSpace(value))
}

func compactID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer(" ", "-", "/", "-", "\\", "-").Replace(value)
	return strings.Trim(value, "-")
}

func (p *Printer) bold(value string) string   { return p.style("1", value) }
func (p *Printer) red(value string) string    { return p.style("31", value) }
func (p *Printer) green(value string) string  { return p.style("32", value) }
func (p *Printer) yellow(value string) string { return p.style("33", value) }

func (p *Printer) style(code, value string) string {
	if !p.Color {
		return value
	}
	return "\x1b[" + code + "m" + value + "\x1b[0m"
}
