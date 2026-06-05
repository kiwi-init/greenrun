package snapshot

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/kiwi-init/greenrun/internal/model"
	"github.com/stretchr/testify/require"
)

func TestCreateIncludesWorkingTreeWithoutMutatingSource(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.name", "Test")
	runGit(t, root, "config", "user.email", "test@example.com")
	require.NoError(t, os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("before\n"), 0o644))
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-qm", "initial")
	head := gitOutput(t, root, "rev-parse", "HEAD")

	require.NoError(t, os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("after\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("new\n"), 0o644))
	snapshot, err := Create(context.Background(), model.Repository{Root: root, HeadSHA: head})
	require.NoError(t, err)
	defer snapshot.Close()

	tracked, err := os.ReadFile(filepath.Join(snapshot.Root, "tracked.txt"))
	require.NoError(t, err)
	require.Equal(t, "after\n", string(tracked))
	untracked, err := os.ReadFile(filepath.Join(snapshot.Root, "untracked.txt"))
	require.NoError(t, err)
	require.Equal(t, "new\n", string(untracked))

	source, err := os.ReadFile(filepath.Join(root, "tracked.txt"))
	require.NoError(t, err)
	require.Equal(t, "after\n", string(source))
	require.NotEmpty(t, snapshot.HeadSHA)
}

func TestCreateInitialRepositoryExcludesIgnoredFiles(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.name", "Test")
	runGit(t, root, "config", "user.email", "test@example.com")
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.bin\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("tracked\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "ignored.bin"), []byte("ignored\n"), 0o644))
	runGit(t, root, "add", ".gitignore", "tracked.txt")

	snapshot, err := Create(context.Background(), model.Repository{Root: root})
	require.NoError(t, err)
	defer snapshot.Close()
	_, err = os.Stat(filepath.Join(snapshot.Root, "tracked.txt"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(snapshot.Root, "ignored.bin"))
	require.True(t, os.IsNotExist(err))
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.Output()
	require.NoError(t, err)
	return string(output[:len(output)-1])
}
