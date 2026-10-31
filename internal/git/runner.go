package git

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Runner shells out to the system git binary.
type Runner struct {
	Binary string // path to git, default "git"
}

// New returns a Runner using the system git binary.
func New() *Runner {
	return &Runner{Binary: "git"}
}

// Clone performs a shallow clone of url at branch into destDir.
func (r *Runner) Clone(ctx context.Context, url, branch, destDir string) error {
	if strings.HasPrefix(url, "-") {
		return fmt.Errorf("invalid git URL: must not start with '-'")
	}
	if branch == "" {
		branch = "main"
	}
	if strings.HasPrefix(branch, "-") {
		return fmt.Errorf("invalid git branch %q", branch)
	}
	// "--" terminates option parsing so a crafted URL cannot be interpreted
	// as a git clone flag (e.g. --upload-pack).
	args := []string{"clone", "--branch", branch, "--depth", "1", "--", url, destDir}
	return r.run(ctx, "", args...)
}

// Pull fetches from origin and fast-forwards the current branch.
// Returns true if the local HEAD changed (i.e. there were upstream commits).
func (r *Runner) Pull(ctx context.Context, repoDir string) (bool, error) {
	before, err := r.HeadRef(ctx, repoDir)
	if err != nil {
		return false, fmt.Errorf("head ref before pull: %w", err)
	}
	if err := r.run(ctx, repoDir, "pull", "--ff-only", "--quiet"); err != nil {
		return false, err
	}
	after, err := r.HeadRef(ctx, repoDir)
	if err != nil {
		return false, fmt.Errorf("head ref after pull: %w", err)
	}
	return before != after, nil
}

// Push stages all changes, commits with message, and pushes to origin.
// Output is written to w.
func (r *Runner) Push(ctx context.Context, repoDir, message string, w io.Writer) error {
	if err := r.runWriter(ctx, repoDir, w, "add", "-A"); err != nil {
		return fmt.Errorf("git add: %w", err)
	}
	if err := r.runWriter(ctx, repoDir, w, "commit", "-m", message); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	if err := r.runWriter(ctx, repoDir, w, "push"); err != nil {
		return fmt.Errorf("git push: %w", err)
	}
	return nil
}

// HasChanges returns true if the working tree has uncommitted changes.
func (r *Runner) HasChanges(ctx context.Context, repoDir string) (bool, error) {
	out, err := r.output(ctx, repoDir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// HeadRef returns the full SHA of the current HEAD commit.
func (r *Runner) HeadRef(ctx context.Context, repoDir string) (string, error) {
	out, err := r.output(ctx, repoDir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// ShortRef returns the short SHA of the current HEAD commit.
func (r *Runner) ShortRef(ctx context.Context, repoDir string) (string, error) {
	out, err := r.output(ctx, repoDir, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CommitMessage returns the subject line of the current HEAD commit.
func (r *Runner) CommitMessage(ctx context.Context, repoDir string) (string, error) {
	out, err := r.output(ctx, repoDir, "log", "-1", "--pretty=%s")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// run executes a git command in workDir, capturing stderr on failure.
func (r *Runner) run(ctx context.Context, workDir string, args ...string) error {
	return r.runWriter(ctx, workDir, io.Discard, args...)
}

// runWriter executes a git command, writing stdout+stderr to w.
func (r *Runner) runWriter(ctx context.Context, workDir string, w io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, r.Binary, args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	var errBuf bytes.Buffer
	cmd.Stdout = w
	cmd.Stderr = io.MultiWriter(w, &errBuf)
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errBuf.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("git %s: %s", args[0], msg)
	}
	return nil
}

// output runs a git command and returns stdout as a string.
func (r *Runner) output(ctx context.Context, workDir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, r.Binary, args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errBuf.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return out.String(), nil
}
