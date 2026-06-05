package executil

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

func Output(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), message)
	}
	return strings.TrimSpace(string(out)), nil
}

func Run(ctx context.Context, dir, name string, args ...string) error {
	_, err := Output(ctx, dir, name, args...)
	return err
}

func Exists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
