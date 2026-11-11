package git

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}
func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitCmd(t, dir, "init")
	gitCmd(t, dir, "config", "user.email", "test@example.com")
	gitCmd(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("initial"), 0600); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "file")
	gitCmd(t, dir, "commit", "-m", "initial")
	return dir
}
func TestPullBlocksDirtyAndStatusErrors(t *testing.T) {
	dir := repo(t)
	r := New()
	ctx := context.Background()
	for _, name := range []string{"file", "untracked"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("local"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Pull(ctx, dir); err == nil || !strings.Contains(err.Error(), "uncommitted local changes") {
			t.Fatalf("%s: got %v", name, err)
		}
		if name == "file" {
			gitCmd(t, dir, "checkout", "--", "file")
		} else {
			os.Remove(filepath.Join(dir, name))
		}
	}
	if _, err := r.Pull(ctx, t.TempDir()); err == nil || !strings.Contains(err.Error(), "check working tree") {
		t.Fatalf("status error: %v", err)
	}
	if _, err := r.Pull(ctx, dir); err == nil {
		t.Fatal("expected pull error without upstream")
	}
}
func TestPushDoesNotStageUntrackedOrUnstaged(t *testing.T) {
	dir := repo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitCmd(t, dir, "init", "--bare", remote)
	gitCmd(t, dir, "remote", "add", "origin", remote)
	gitCmd(t, dir, "push", "-u", "origin", "HEAD")
	os.WriteFile(filepath.Join(dir, "file"), []byte("staged"), 0600)
	gitCmd(t, dir, "add", "file")
	os.WriteFile(filepath.Join(dir, "other"), []byte("untracked"), 0600)
	var output bytes.Buffer
	if err := New().Push(context.Background(), dir, "change", &output); err != nil {
		t.Fatal(err)
	}
	if got := gitCmd(t, dir, "status", "--porcelain"); !strings.Contains(got, "?? other") {
		t.Fatalf("untracked file was staged: %s", got)
	}
}

func TestPullFastForwardsCleanRepository(t *testing.T) {
	source := repo(t)
	clone := filepath.Join(t.TempDir(), "clone")
	gitCmd(t, "", "clone", source, clone)
	os.WriteFile(filepath.Join(source, "file"), []byte("upstream"), 0600)
	gitCmd(t, source, "commit", "-am", "upstream")
	changed, err := New().Pull(context.Background(), clone)
	if err != nil || !changed {
		t.Fatalf("pull: changed=%v err=%v", changed, err)
	}
	changed, err = New().Pull(context.Background(), clone)
	if err != nil || changed {
		t.Fatalf("second pull: changed=%v err=%v", changed, err)
	}
}
