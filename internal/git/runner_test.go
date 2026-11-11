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
	for _, mode := range []string{"unstaged", "staged", "untracked"} {
		name := "file"
		if mode == "untracked" {
			name = "untracked"
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("local"), 0600); err != nil {
			t.Fatal(err)
		}
		if mode == "staged" {
			gitCmd(t, dir, "add", "file")
		}
		head := gitCmd(t, dir, "rev-parse", "HEAD")
		index := gitCmd(t, dir, "ls-files", "--stage")
		status := gitCmd(t, dir, "status", "--porcelain")
		if _, err := r.Pull(ctx, dir); err == nil || !strings.Contains(err.Error(), "uncommitted local changes") {
			t.Fatalf("%s: got %v", mode, err)
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != "local" {
			t.Fatalf("%s: content %q, %v", mode, data, err)
		}
		if gitCmd(t, dir, "rev-parse", "HEAD") != head || gitCmd(t, dir, "ls-files", "--stage") != index || gitCmd(t, dir, "status", "--porcelain") != status {
			t.Fatalf("%s: pull changed repository", mode)
		}
		if name == "file" {
			gitCmd(t, dir, "reset", "--hard", "HEAD")
		} else if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
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
	os.WriteFile(filepath.Join(dir, "file"), []byte("unstaged"), 0600)
	os.WriteFile(filepath.Join(dir, "other"), []byte("untracked"), 0600)
	var output bytes.Buffer
	if err := New().Push(context.Background(), dir, "change", &output); err != nil {
		t.Fatal(err)
	}
	if got := gitCmd(t, remote, "show", "HEAD:file"); got != "staged" {
		t.Fatalf("remote content = %q", got)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "file")); err != nil || string(data) != "unstaged" {
		t.Fatalf("working content = %q, %v", data, err)
	}
	if got := gitCmd(t, dir, "status", "--porcelain"); !strings.Contains(got, " M file") || !strings.Contains(got, "?? other") {
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

func TestRunnerDisablesConfiguredHooks(t *testing.T) {
	dir := repo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitCmd(t, dir, "init", "--bare", remote)
	gitCmd(t, dir, "remote", "add", "origin", remote)
	gitCmd(t, dir, "push", "-u", "origin", "HEAD")
	hooks := t.TempDir()
	marker := filepath.Join(t.TempDir(), "hook-ran")
	for _, name := range []string{"pre-commit", "commit-msg", "post-commit", "pre-push"} {
		if err := os.WriteFile(filepath.Join(hooks, name), []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 1\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	gitCmd(t, dir, "config", "core.hooksPath", hooks)
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "file")
	var output bytes.Buffer
	if err := New().Push(context.Background(), dir, "change", &output); err != nil {
		t.Fatalf("push: %v; output: %s", err, output.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("hook ran or marker stat failed: %v", err)
	}
	if got := gitCmd(t, remote, "show", "HEAD:file"); got != "changed" {
		t.Fatalf("remote content = %q", got)
	}
}
