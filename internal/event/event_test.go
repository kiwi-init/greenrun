package event

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseGitHubRun(t *testing.T) {
	id, err := ParseGitHubRun("https://github.com/kiwi-init/greenrun/actions/runs/12345/job/1")
	require.NoError(t, err)
	require.Equal(t, int64(12345), id)

	id, err = ParseGitHubRun("latest-failed")
	require.NoError(t, err)
	require.Zero(t, id)

	_, err = ParseGitHubRun("nope")
	require.Error(t, err)
}
