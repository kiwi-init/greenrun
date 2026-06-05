package store

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kiwi-init/greenrun/internal/model"
	"github.com/kiwi-init/greenrun/internal/repo"
)

type Store struct {
	Home string
}

type Run struct {
	Store        *Store
	Repository   model.Repository
	ID           string
	Directory    string
	LogsDir      string
	ArtifactsDir string

	mu sync.Mutex
}

func New() *Store {
	return &Store{Home: repo.HomeDir()}
}

func (s *Store) RepoDir(repository model.Repository) string {
	return filepath.Join(s.Home, "repos", repo.SlugForPath(repository))
}

func (s *Store) Start(repository model.Repository) (*Run, error) {
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		return nil, err
	}
	id := time.Now().UTC().Format("20060102T150405Z") + "-" + strings.ToUpper(hex.EncodeToString(suffix))
	directory := filepath.Join(s.RepoDir(repository), "runs", id)
	logs := filepath.Join(directory, "logs")
	artifacts := filepath.Join(directory, "artifacts")
	for _, path := range []string{logs, artifacts, filepath.Join(s.Home, "cache", "actions"), filepath.Join(s.Home, "cache", "artifacts")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return nil, err
		}
	}
	return &Run{
		Store:        s,
		Repository:   repository,
		ID:           id,
		Directory:    directory,
		LogsDir:      logs,
		ArtifactsDir: artifacts,
	}, nil
}

func (r *Run) WriteJSON(name string, value any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWrite(filepath.Join(r.Directory, name), data, 0o600)
}

func (r *Run) AppendEvent(event model.EventRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	file, err := os.OpenFile(filepath.Join(r.Directory, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewEncoder(file).Encode(event)
}

func (r *Run) WriteLog(name, content string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name = safeName(name) + ".log"
	path := filepath.Join(r.LogsDir, name)
	if err := atomicWrite(path, []byte(content), 0o600); err != nil {
		return "", err
	}
	return filepath.ToSlash(filepath.Join("logs", name)), nil
}

func (r *Run) Complete(result model.Result, compact string) error {
	result.RunDirectory = r.Directory
	if err := r.WriteJSON("result.json", result); err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(r.Directory, "result.gr"), []byte(compact), 0o600); err != nil {
		return err
	}
	repoDir := r.Store.RepoDir(r.Repository)
	if err := atomicWrite(filepath.Join(repoDir, "latest"), []byte(r.ID+"\n"), 0o600); err != nil {
		return err
	}
	index, err := os.OpenFile(filepath.Join(repoDir, "index.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer index.Close()
	entry := map[string]any{
		"id": result.ID, "status": result.Status, "source": result.Source,
		"started_at": result.StartedAt, "duration_ms": result.DurationMS,
	}
	return json.NewEncoder(index).Encode(entry)
}

func (s *Store) Resolve(repository model.Repository, reference string) (string, error) {
	repoDir := s.RepoDir(repository)
	id := reference
	if reference == "" || reference == "latest" {
		data, err := os.ReadFile(filepath.Join(repoDir, "latest"))
		if err != nil {
			return "", fmt.Errorf("no Greenrun result found for %s", repository.Slug)
		}
		id = strings.TrimSpace(string(data))
	}
	directory := filepath.Join(repoDir, "runs", id)
	if info, err := os.Stat(directory); err != nil || !info.IsDir() {
		return "", fmt.Errorf("run %q not found for %s", id, repository.Slug)
	}
	return directory, nil
}

func (s *Store) ReadResult(repository model.Repository, reference string) (model.Result, error) {
	directory, err := s.Resolve(repository, reference)
	if err != nil {
		return model.Result{}, err
	}
	data, err := os.ReadFile(filepath.Join(directory, "result.json"))
	if err != nil {
		return model.Result{}, err
	}
	var result model.Result
	if err := json.Unmarshal(data, &result); err != nil {
		return model.Result{}, err
	}
	result.RunDirectory = directory
	return result, nil
}

func ReadLines(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var lines []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".greenrun-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func safeName(value string) string {
	var builder strings.Builder
	for _, char := range strings.ToLower(value) {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9':
			builder.WriteRune(char)
		default:
			if builder.Len() > 0 && !strings.HasSuffix(builder.String(), "-") {
				builder.WriteByte('-')
			}
		}
	}
	return strings.Trim(builder.String(), "-")
}
