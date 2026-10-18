package seed

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// FetchRepo checks out url at ref (branch or commit SHA) into workDir/name
// with a shallow fetch and returns the directory and commit SHA.
func FetchRepo(ctx context.Context, workDir, name, url, ref string) (string, string, error) {
	dir := filepath.Join(workDir, name)
	if _, err := os.Stat(filepath.Join(dir, ".git")); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(workDir, 0o755); err != nil {
			return "", "", fmt.Errorf("git %s: %w", name, err)
		}
		if _, err := git(ctx, "init", "-q", dir); err != nil {
			return "", "", err
		}
		if _, err := git(ctx, "-C", dir, "remote", "add", "origin", url); err != nil {
			return "", "", err
		}
	} else if err != nil {
		return "", "", fmt.Errorf("git %s: %w", name, err)
	} else {
		// Keep the remote in sync with sources.yaml.
		if _, err := git(ctx, "-C", dir, "remote", "set-url", "origin", url); err != nil {
			return "", "", err
		}
	}
	if _, err := git(ctx, "-C", dir, "fetch", "-q", "--depth", "1", "origin", ref); err != nil {
		return "", "", err
	}
	if _, err := git(ctx, "-C", dir, "checkout", "-q", "--force", "FETCH_HEAD"); err != nil {
		return "", "", err
	}
	sha, err := git(ctx, "-C", dir, "rev-parse", "HEAD")
	if err != nil {
		return "", "", err
	}
	return dir, sha, nil
}

func git(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// BlobURL is a permalink to path at sha in a GitHub repository.
func BlobURL(repoURL, sha, path string) string {
	return strings.TrimRight(repoURL, "/") + "/blob/" + sha + "/" + path
}
