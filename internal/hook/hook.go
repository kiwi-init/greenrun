// Package hook manages the local git pre-push hook. The hook lives in the
// repository's git directory, never in the checkout, so installing it does
// not add repository configuration and never reaches a remote.
package hook

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kiwi-init/greenrun/internal/executil"
)

// Marker identifies a hook file Greenrun owns. Install and Uninstall
// refuse to touch a pre-push hook without it.
const Marker = "# greenrun pre-push hook"

// The hook blocks the push only on a real local CI failure (exit 1) or a
// cancelled verification (130). A pass (0) and an honest partial (3) let
// the push proceed, and a Greenrun runtime error (2) fails open with a
// warning: a broken Docker daemon must not lock pushes.
const script = `#!/bin/sh
` + Marker + ` v1
# Installed by 'greenrun hook install'; remove with 'greenrun hook uninstall'.
status=0
greenrun --plain || status=$?
case "$status" in
0|3)
	exit 0
	;;
2)
	echo "greenrun: could not verify this push (runtime error); allowing it. Run 'greenrun doctor'." >&2
	exit 0
	;;
*)
	echo "greenrun: blocking push (exit $status). Bypass once with 'git push --no-verify'." >&2
	exit "$status"
	;;
esac
`

type State string

const (
	StateInstalled State = "installed"
	StateMissing   State = "not installed"
	StateForeign   State = "foreign"
)

// Path resolves the pre-push hook location, honoring core.hooksPath.
func Path(ctx context.Context, root string) (string, error) {
	hooksDir, err := executil.Output(ctx, root, "git", "rev-parse", "--git-path", "hooks")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(hooksDir) {
		hooksDir = filepath.Join(root, hooksDir)
	}
	return filepath.Join(hooksDir, "pre-push"), nil
}

// Install writes the Greenrun pre-push hook. A pre-existing hook Greenrun
// does not own is left untouched and reported as an error.
func Install(ctx context.Context, root string) (string, error) {
	path, err := Path(ctx, root)
	if err != nil {
		return "", err
	}
	state, err := inspect(path)
	if err != nil {
		return "", err
	}
	if state == StateForeign {
		return "", fmt.Errorf("a pre-push hook already exists at %s; remove it or invoke greenrun from it manually", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		return "", err
	}
	return path, nil
}

// Uninstall removes the hook only when Greenrun owns it.
func Uninstall(ctx context.Context, root string) (string, error) {
	path, err := Path(ctx, root)
	if err != nil {
		return "", err
	}
	state, err := inspect(path)
	if err != nil {
		return "", err
	}
	switch state {
	case StateMissing:
		return path, nil
	case StateForeign:
		return "", fmt.Errorf("the pre-push hook at %s was not installed by greenrun; leaving it in place", path)
	}
	return path, os.Remove(path)
}

// Status reports whether the pre-push hook is Greenrun's, absent, or
// owned by something else.
func Status(ctx context.Context, root string) (State, string, error) {
	path, err := Path(ctx, root)
	if err != nil {
		return "", "", err
	}
	state, err := inspect(path)
	return state, path, err
}

func inspect(path string) (State, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return StateMissing, nil
	}
	if err != nil {
		return "", err
	}
	if strings.Contains(string(data), Marker) {
		return StateInstalled, nil
	}
	return StateForeign, nil
}
