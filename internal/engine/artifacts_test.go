package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCollectArtifactsIndexesActArchives(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "1", "test-results", "test-results.zip")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("archive"), 0o600))

	artifacts := CollectArtifacts(root)

	require.Len(t, artifacts, 1)
	require.Equal(t, int64(1), artifacts[0].ID)
	require.Equal(t, "test-results", artifacts[0].Name)
	require.Equal(t, int64(7), artifacts[0].SizeBytes)
	require.Equal(t, path, artifacts[0].DownloadedTo)
}
