package repo

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRemoteSlug(t *testing.T) {
	require.Equal(t, "kiwi-init/greenrun", remoteSlug("git@github.com:kiwi-init/greenrun.git"))
	require.Equal(t, "kiwi-init/greenrun", remoteSlug("https://github.com/kiwi-init/greenrun.git"))
	require.Empty(t, remoteSlug(""))
}
