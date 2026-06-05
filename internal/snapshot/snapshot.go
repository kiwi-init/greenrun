package snapshot

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kiwi-init/greenrun/internal/executil"
	"github.com/kiwi-init/greenrun/internal/model"
)

type Snapshot struct {
	Root    string
	HeadSHA string
	cleanup func()
}

func Create(ctx context.Context, repository model.Repository) (*Snapshot, error) {
	parent, err := os.MkdirTemp("", "greenrun-snapshot-*")
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = os.RemoveAll(parent) }
	root := filepath.Join(parent, "repo")

	if repository.HeadSHA == "" {
		if err := os.MkdirAll(root, 0o700); err != nil {
			cleanup()
			return nil, err
		}
		if err := executil.Run(ctx, root, "git", "init", "-q"); err != nil {
			cleanup()
			return nil, err
		}
		if err := copyInitial(ctx, repository.Root, root); err != nil {
			cleanup()
			return nil, err
		}
	} else {
		cmd := exec.CommandContext(ctx, "git", "clone", "--quiet", "--no-hardlinks", repository.Root, root)
		if out, err := cmd.CombinedOutput(); err != nil {
			cleanup()
			return nil, fmt.Errorf("create isolated clone: %s", strings.TrimSpace(string(out)))
		}
		if err := applyTrackedChanges(ctx, repository.Root, root); err != nil {
			cleanup()
			return nil, err
		}
		if err := copyUntracked(ctx, repository.Root, root); err != nil {
			cleanup()
			return nil, err
		}
	}

	if repository.RemoteURL != "" {
		if err := configureOrigin(ctx, root, repository.RemoteURL); err != nil {
			cleanup()
			return nil, err
		}
	}
	if err := executil.Run(ctx, root, "git", "config", "user.name", "Greenrun"); err != nil {
		cleanup()
		return nil, err
	}
	if err := executil.Run(ctx, root, "git", "config", "user.email", "greenrun@localhost"); err != nil {
		cleanup()
		return nil, err
	}
	if err := executil.Run(ctx, root, "git", "add", "-A"); err != nil {
		cleanup()
		return nil, err
	}
	_ = executil.Run(ctx, root, "git", "commit", "--allow-empty", "--no-gpg-sign", "-q", "-m", "greenrun synthetic commit")
	head, err := executil.Output(ctx, root, "git", "rev-parse", "HEAD")
	if err != nil {
		cleanup()
		return nil, err
	}

	return &Snapshot{Root: root, HeadSHA: head, cleanup: cleanup}, nil
}

func configureOrigin(ctx context.Context, root, remoteURL string) error {
	if executil.Run(ctx, root, "git", "remote", "get-url", "origin") == nil {
		if err := executil.Run(ctx, root, "git", "remote", "set-url", "origin", remoteURL); err != nil {
			return fmt.Errorf("set snapshot origin: %w", err)
		}
		return nil
	}
	if err := executil.Run(ctx, root, "git", "remote", "add", "origin", remoteURL); err != nil {
		return fmt.Errorf("add snapshot origin: %w", err)
	}
	return nil
}

func copyInitial(ctx context.Context, source, target string) error {
	output, err := executil.Output(ctx, source, "git", "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	for _, relative := range splitNUL(output) {
		if relative == "" {
			continue
		}
		if err := copyPath(filepath.Join(source, relative), filepath.Join(target, relative)); err != nil {
			return fmt.Errorf("copy initial file %s: %w", relative, err)
		}
	}
	return nil
}

func (s *Snapshot) Close() {
	if s != nil && s.cleanup != nil {
		s.cleanup()
	}
}

func applyTrackedChanges(ctx context.Context, source, target string) error {
	patch, err := os.CreateTemp("", "greenrun-*.patch")
	if err != nil {
		return err
	}
	defer os.Remove(patch.Name())
	defer patch.Close()

	cmd := exec.CommandContext(ctx, "git", "diff", "--binary", "HEAD")
	cmd.Dir = source
	cmd.Stdout = patch
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("capture working tree: %s", strings.TrimSpace(stderr.String()))
	}
	if info, err := patch.Stat(); err != nil || info.Size() == 0 {
		return err
	}
	if err := patch.Close(); err != nil {
		return err
	}
	cmd = exec.CommandContext(ctx, "git", "apply", "--whitespace=nowarn", patch.Name())
	cmd.Dir = target
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("apply working tree to snapshot: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func copyUntracked(ctx context.Context, source, target string) error {
	output, err := executil.Output(ctx, source, "git", "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	for _, relative := range splitNUL(output) {
		if relative == "" {
			continue
		}
		if err := copyPath(filepath.Join(source, relative), filepath.Join(target, relative)); err != nil {
			return fmt.Errorf("copy untracked %s: %w", relative, err)
		}
	}
	return nil
}

func copyPath(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return os.MkdirAll(target, info.Mode().Perm())
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		value, err := os.Readlink(source)
		if err != nil {
			return err
		}
		_ = os.Remove(target)
		return os.Symlink(value, target)
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return err
	}
	return output.Close()
}

func splitNUL(value string) []string {
	value = strings.TrimSuffix(value, "\x00")
	if value == "" {
		return nil
	}
	return strings.Split(value, "\x00")
}
