package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	greenmask "github.com/kiwi-init/greenrun/internal/mask"
	"github.com/kiwi-init/greenrun/internal/model"
	"github.com/kiwi-init/greenrun/internal/store"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestParseAnnotation(t *testing.T) {
	diagnostic, ok := parseAnnotation("::error file=src/main.go,line=12,col=4::broken value")
	require.True(t, ok)
	require.Equal(t, "src/main.go", diagnostic.File)
	require.Equal(t, 12, diagnostic.Line)
	require.Equal(t, 4, diagnostic.Column)
	require.Equal(t, "broken value", diagnostic.Message)
}

func TestCollectorNeverPersistsSecrets(t *testing.T) {
	storage := &store.Store{Home: t.TempDir()}
	run, err := storage.Start(model.Repository{Slug: "owner/repo", Identity: "id"})
	require.NoError(t, err)
	collector := newLogCollector(greenmask.New("seed-secret"), run, os.Stdout, true, false, nil, nil, nil, "")

	entry := &logrus.Entry{
		Logger: logrus.New(), Context: context.Background(),
		Data: logrus.Fields{"jobID": "test"}, Message: "::add-mask::dynamic-secret",
	}
	require.NoError(t, collector.Fire(entry))
	entry.Message = "values seed-secret dynamic-secret"
	require.NoError(t, collector.Fire(entry))
	workflow := model.Workflow{ID: "ci", Jobs: []model.Job{{ID: "test"}}}
	require.NoError(t, collector.Apply(&workflow))

	data, err := os.ReadFile(filepath.Join(run.Directory, filepath.FromSlash(workflow.Jobs[0].Log)))
	require.NoError(t, err)
	require.NotContains(t, string(data), "seed-secret")
	require.NotContains(t, string(data), "dynamic-secret")
	require.Contains(t, string(data), "***")
}

func TestCollectorMapsReusableJobEvidenceToCaller(t *testing.T) {
	storage := &store.Store{Home: t.TempDir()}
	run, err := storage.Start(model.Repository{Slug: "owner/repo", Identity: "id"})
	require.NoError(t, err)
	collector := newLogCollector(
		greenmask.New(),
		run,
		os.Stdout,
		true,
		false,
		nil,
		map[string]string{"child": "call"},
		map[string]bool{"call": true},
		"",
	)
	entry := &logrus.Entry{
		Logger:  logrus.New(),
		Context: context.Background(),
		Data:    logrus.Fields{"jobID": "child", "step": "verify", "stepResult": "success"},
		Message: "child workflow passed",
	}
	require.NoError(t, collector.Fire(entry))
	workflow := model.Workflow{ID: "ci", Jobs: []model.Job{{ID: "call"}}}
	require.NoError(t, collector.Apply(&workflow))

	require.NotEmpty(t, workflow.Jobs[0].Log)
	require.Len(t, workflow.Jobs[0].Steps, 1)
	require.Equal(t, "verify", workflow.Jobs[0].Steps[0].Name)
	require.True(t, collector.HasJob("call"))
	require.False(t, collector.HasJob("other"))
}

func TestCollectorMapsUnknownReusableChildToSoleCaller(t *testing.T) {
	storage := &store.Store{Home: t.TempDir()}
	run, err := storage.Start(model.Repository{Slug: "owner/repo", Identity: "id"})
	require.NoError(t, err)
	collector := newLogCollector(
		greenmask.New(),
		run,
		os.Stdout,
		true,
		false,
		nil,
		nil,
		map[string]bool{"call": true, "lint": true},
		"call",
	)
	entry := &logrus.Entry{
		Logger:  logrus.New(),
		Context: context.Background(),
		Data:    logrus.Fields{"jobID": "remote-child"},
		Message: "remote reusable output",
	}
	require.NoError(t, collector.Fire(entry))
	require.True(t, collector.HasJob("call"))
	require.False(t, collector.HasJob("remote-child"))
}
