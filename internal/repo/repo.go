package repo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/kiwi-init/greenrun/internal/executil"
	"github.com/kiwi-init/greenrun/internal/model"
)

var scpRemote = regexp.MustCompile(`^[^@]+@[^:]+:(.+)$`)

func Discover(ctx context.Context, start string) (model.Repository, error) {
	root, err := executil.Output(ctx, start, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return model.Repository{}, fmt.Errorf("not a git repository: %w", err)
	}

	branch, _ := executil.Output(ctx, root, "git", "branch", "--show-current")
	if branch == "" {
		branch, _ = executil.Output(ctx, root, "git", "rev-parse", "--short", "HEAD")
	}
	head, _ := executil.Output(ctx, root, "git", "rev-parse", "HEAD")
	remoteURL, _ := executil.Output(ctx, root, "git", "remote", "get-url", "origin")
	defaultBranch := discoverDefaultBranch(ctx, root)
	baseSHA := discoverBase(ctx, root, defaultBranch)
	changed, err := changedFiles(ctx, root, baseSHA)
	if err != nil {
		return model.Repository{}, err
	}

	slug := remoteSlug(remoteURL)
	if slug == "" {
		slug = filepath.Base(root)
	}
	identitySource := remoteURL
	if identitySource == "" {
		identitySource = root
	}
	sum := sha256.Sum256([]byte(identitySource))

	return model.Repository{
		Root:          root,
		Slug:          slug,
		RemoteURL:     remoteURL,
		Identity:      hex.EncodeToString(sum[:8]),
		Branch:        branch,
		DefaultBranch: defaultBranch,
		HeadSHA:       head,
		BaseSHA:       baseSHA,
		Dirty:         len(changed) > 0,
		ChangedFiles:  changed,
	}, nil
}

func discoverDefaultBranch(ctx context.Context, root string) string {
	ref, err := executil.Output(ctx, root, "git", "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if err == nil {
		if _, branch, ok := strings.Cut(ref, "/"); ok && branch != "" {
			return branch
		}
	}
	for _, candidate := range []string{"main", "master"} {
		if executil.Run(ctx, root, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+candidate) == nil ||
			executil.Run(ctx, root, "git", "show-ref", "--verify", "--quiet", "refs/remotes/origin/"+candidate) == nil {
			return candidate
		}
	}
	return "main"
}

func discoverBase(ctx context.Context, root, defaultBranch string) string {
	for _, ref := range []string{"origin/" + defaultBranch, defaultBranch} {
		if sha, err := executil.Output(ctx, root, "git", "merge-base", "HEAD", ref); err == nil {
			return sha
		}
	}
	return ""
}

func changedFiles(ctx context.Context, root, baseSHA string) ([]string, error) {
	tracked, err := executil.Output(ctx, root, "git", "diff", "--name-only", "-z", "HEAD")
	if err != nil {
		tracked, _ = executil.Output(ctx, root, "git", "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	}
	committed := ""
	if baseSHA != "" {
		committed, _ = executil.Output(ctx, root, "git", "diff", "--name-only", "-z", baseSHA+"..HEAD")
	}
	untracked, _ := executil.Output(ctx, root, "git", "ls-files", "--others", "--exclude-standard", "-z")
	set := map[string]struct{}{}
	values := append(splitNUL(committed), splitNUL(tracked)...)
	values = append(values, splitNUL(untracked)...)
	for _, value := range values {
		if value != "" {
			set[value] = struct{}{}
		}
	}
	files := make([]string, 0, len(set))
	for value := range set {
		files = append(files, value)
	}
	sort.Strings(files)
	return files, nil
}

func splitNUL(value string) []string {
	value = strings.TrimSuffix(value, "\x00")
	if value == "" {
		return nil
	}
	return strings.Split(value, "\x00")
}

func remoteSlug(remote string) string {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return ""
	}
	path := ""
	if match := scpRemote.FindStringSubmatch(remote); len(match) == 2 {
		path = match[1]
	} else if parsed, err := url.Parse(remote); err == nil {
		path = parsed.Path
	}
	path = strings.Trim(strings.TrimSuffix(path, ".git"), "/")
	parts := strings.Split(path, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}
	return path
}

func SlugForPath(repo model.Repository) string {
	slug := strings.NewReplacer("/", "-", "\\", "-", " ", "-").Replace(repo.Slug)
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "repository"
	}
	return slug + "-" + repo.Identity
}

func HomeDir() string {
	if value := os.Getenv("GREENRUN_HOME"); value != "" {
		return value
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".greenrun")
}
