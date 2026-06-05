package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kiwi-init/greenrun/internal/model"
	"github.com/stretchr/testify/require"
)

func TestRunLifecycle(t *testing.T) {
	storage := &Store{Home: t.TempDir()}
	repository := model.Repository{Slug: "owner/repo", Identity: "abc"}
	run, err := storage.Start(repository)
	require.NoError(t, err)
	require.NoError(t, run.AppendEvent(model.EventRecord{Time: time.Now(), Type: "started"}))

	result := model.Result{
		ID: run.ID, Status: model.StatusPass, Source: model.SourceLocal,
		Repository: repository, RunDirectory: run.Directory,
	}
	require.NoError(t, run.Complete(result, "greenrun 1\n"))
	resolved, err := storage.Resolve(repository, "latest")
	require.NoError(t, err)
	require.Equal(t, run.Directory, resolved)
	_, err = os.Stat(filepath.Join(resolved, "result.json"))
	require.NoError(t, err)
}
