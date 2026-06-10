package hook

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiwi-init/greenrun/internal/executil"
	"github.com/stretchr/testify/require"
)

func initRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, executil.Run(context.Background(), root, "git", "init", "--quiet"))
	return root
}

func TestInstallStatusUninstallLifecycle(t *testing.T) {
	root := initRepo(t)
	ctx := context.Background()

	state, _, err := Status(ctx, root)
	require.NoError(t, err)
	require.Equal(t, StateMissing, state)

	path, err := Install(ctx, root)
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), Marker)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&0o100, "hook must be executable")

	state, _, err = Status(ctx, root)
	require.NoError(t, err)
	require.Equal(t, StateInstalled, state)

	// Reinstalling over our own hook is idempotent.
	_, err = Install(ctx, root)
	require.NoError(t, err)

	_, err = Uninstall(ctx, root)
	require.NoError(t, err)
	_, err = os.Stat(path)
	require.True(t, os.IsNotExist(err))

	// Uninstalling when nothing is installed is not an error.
	_, err = Uninstall(ctx, root)
	require.NoError(t, err)
}

func TestForeignHookIsNeverTouched(t *testing.T) {
	root := initRepo(t)
	ctx := context.Background()
	path, err := Path(ctx, root)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	foreign := "#!/bin/sh\nexit 0\n"
	require.NoError(t, os.WriteFile(path, []byte(foreign), 0o755))

	_, err = Install(ctx, root)
	require.Error(t, err)
	_, err = Uninstall(ctx, root)
	require.Error(t, err)

	state, _, err := Status(ctx, root)
	require.NoError(t, err)
	require.Equal(t, StateForeign, state)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, foreign, string(data))
}

func TestHookScriptFailsClosedOnFailureOnly(t *testing.T) {
	// The script blocks on exit 1 (failure) and 130 (cancelled), and lets
	// 0 (pass), 3 (partial), and 2 (greenrun runtime error) through.
	require.True(t, strings.Contains(script, `0|3)`))
	require.True(t, strings.Contains(script, `2)`))
	require.True(t, strings.Contains(script, "greenrun --plain"))
}
