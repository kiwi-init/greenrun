package model

import "time"

const SchemaVersion = 1

type Status string

const (
	StatusPass      Status = "pass"
	StatusFail      Status = "fail"
	StatusPartial   Status = "partial"
	StatusError     Status = "error"
	StatusCancelled Status = "cancelled"
	StatusSkip      Status = "skip"
	StatusBlocked   Status = "blocked"
	StatusRemote    Status = "remote_only"
	StatusRunning   Status = "running"
	StatusPending   Status = "pending"
)

type Fidelity string

const (
	FidelityExact       Fidelity = "exact"
	FidelityCompatible  Fidelity = "compatible"
	FidelityApproximate Fidelity = "approximate"
	FidelityRemoteOnly  Fidelity = "remote_only"
)

type Source string

const (
	SourceLocal  Source = "local"
	SourceGitHub Source = "github"
)

type Repository struct {
	Root          string   `json:"root"`
	Slug          string   `json:"slug"`
	RemoteURL     string   `json:"remote_url,omitempty"`
	Identity      string   `json:"identity"`
	Branch        string   `json:"branch"`
	DefaultBranch string   `json:"default_branch"`
	HeadSHA       string   `json:"head_sha,omitempty"`
	BaseSHA       string   `json:"base_sha,omitempty"`
	Dirty         bool     `json:"dirty"`
	ChangedFiles  []string `json:"changed_files,omitempty"`
}

type Event struct {
	Name       string         `json:"name"`
	Path       string         `json:"path,omitempty"`
	Inferred   bool           `json:"inferred"`
	Fidelity   Fidelity       `json:"fidelity"`
	BaseRef    string         `json:"base_ref,omitempty"`
	HeadRef    string         `json:"head_ref,omitempty"`
	BaseSHA    string         `json:"base_sha,omitempty"`
	HeadSHA    string         `json:"head_sha,omitempty"`
	Payload    map[string]any `json:"payload,omitempty"`
	Reason     string         `json:"reason,omitempty"`
	PullNumber int            `json:"pull_number,omitempty"`
}

type Diagnostic struct {
	Level   string `json:"level"`
	Message string `json:"message"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
}

type Step struct {
	ID           string       `json:"id,omitempty"`
	Name         string       `json:"name"`
	Status       Status       `json:"status"`
	StartedAt    *time.Time   `json:"started_at,omitempty"`
	CompletedAt  *time.Time   `json:"completed_at,omitempty"`
	DurationMS   int64        `json:"duration_ms,omitempty"`
	Log          string       `json:"log,omitempty"`
	Diagnostics  []Diagnostic `json:"diagnostics,omitempty"`
	ErrorMessage string       `json:"error,omitempty"`
}

type Job struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	WorkflowID   string       `json:"workflow_id"`
	WorkflowName string       `json:"workflow_name"`
	Runner       []string     `json:"runner,omitempty"`
	Needs        []string     `json:"needs,omitempty"`
	Uses         string       `json:"uses,omitempty"`
	Status       Status       `json:"status"`
	Fidelity     Fidelity     `json:"fidelity"`
	Reason       string       `json:"reason,omitempty"`
	Rank         float64      `json:"rank,omitempty"`
	StartedAt    *time.Time   `json:"started_at,omitempty"`
	CompletedAt  *time.Time   `json:"completed_at,omitempty"`
	DurationMS   int64        `json:"duration_ms,omitempty"`
	Log          string       `json:"log,omitempty"`
	Steps        []Step       `json:"steps,omitempty"`
	Diagnostics  []Diagnostic `json:"diagnostics,omitempty"`
	ErrorMessage string       `json:"error,omitempty"`
}

type Workflow struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Path        string       `json:"path"`
	Triggered   bool         `json:"triggered"`
	Fidelity    Fidelity     `json:"fidelity"`
	Reason      string       `json:"reason,omitempty"`
	Jobs        []Job        `json:"jobs,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

type Artifact struct {
	ID           int64     `json:"id,omitempty"`
	Name         string    `json:"name"`
	SizeBytes    int64     `json:"size_bytes,omitempty"`
	ArchiveURL   string    `json:"archive_url,omitempty"`
	Expired      bool      `json:"expired,omitempty"`
	CreatedAt    time.Time `json:"created_at,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	DownloadedTo string    `json:"downloaded_to,omitempty"`
}

type Plan struct {
	SchemaVersion int          `json:"schema_version"`
	GeneratedAt   time.Time    `json:"generated_at"`
	Repository    Repository   `json:"repository"`
	Event         Event        `json:"event"`
	Workflows     []Workflow   `json:"workflows"`
	Diagnostics   []Diagnostic `json:"diagnostics,omitempty"`
}

type Result struct {
	SchemaVersion int          `json:"schema_version"`
	ID            string       `json:"id"`
	Source        Source       `json:"source"`
	Status        Status       `json:"status"`
	Fidelity      Fidelity     `json:"fidelity"`
	StartedAt     time.Time    `json:"started_at"`
	CompletedAt   time.Time    `json:"completed_at"`
	DurationMS    int64        `json:"duration_ms"`
	Repository    Repository   `json:"repository"`
	Event         Event        `json:"event"`
	Workflows     []Workflow   `json:"workflows"`
	Diagnostics   []Diagnostic `json:"diagnostics,omitempty"`
	Artifacts     []Artifact   `json:"artifacts,omitempty"`
	Reproduce     string       `json:"reproduce,omitempty"`
	RunDirectory  string       `json:"run_directory"`
	RemoteURL     string       `json:"remote_url,omitempty"`
}

type EventRecord struct {
	Time     time.Time      `json:"time"`
	Type     string         `json:"type"`
	Workflow string         `json:"workflow,omitempty"`
	Job      string         `json:"job,omitempty"`
	Step     string         `json:"step,omitempty"`
	Status   Status         `json:"status,omitempty"`
	Message  string         `json:"message,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
}

func ExitCode(status Status) int {
	switch status {
	case StatusPass:
		return 0
	case StatusFail:
		return 1
	case StatusPartial:
		return 3
	case StatusCancelled:
		return 130
	default:
		return 2
	}
}
