package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/projectsnapshot"
)

func TestCaptureBranchUsesExactObjectBytes(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(captureEnvironment(os.Environ()), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", args[0], err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, path), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-b", "deploy")
	run("config", "user.name", "Test")
	run("config", "user.email", "test@example.invalid")
	write("compose.pkl", "committed\n")
	write(".gitattributes", "compose.pkl export-ignore\n")
	run("add", ".")
	run("commit", "-m", "first")
	want := run("rev-parse", "HEAD")
	write("compose.pkl", "dirty worktree\n")
	write("untracked", "not reviewed\n")
	limits := projectsnapshot.Limits{MaxFiles: 10, MaxEntries: 20, MaxDepth: 3, MaxFileBytes: 256, MaxTotalBytes: 512}
	t.Setenv("GIT_DIR", filepath.Join(dir, ".git"))
	t.Setenv("GIT_WORK_TREE", dir)
	commit, snap, err := New().CaptureBranch(ctx, dir, "deploy", "compose.pkl", limits)
	if err != nil || commit != want || len(snap.Files) != 2 || snap.Files[1].Path != "compose.pkl" || string(snap.Files[1].Content) != "committed\n" {
		t.Fatalf("unexpected capture: commit=%s files=%d err=%v", commit, len(snap.Files), err)
	}
	if got := run("rev-parse", "HEAD"); got != want {
		t.Fatal("source ref changed")
	}
	limits.MaxFileBytes = 1
	if id, snap, err := New().CaptureBranch(ctx, dir, "deploy", "compose.pkl", limits); err == nil || id != "" || len(snap.Files) != 0 {
		t.Fatal("oversized blob returned")
	}
	limits.MaxFileBytes = 256
	if err := os.Symlink("compose.pkl", filepath.Join(dir, "symlink")); err != nil {
		t.Fatal(err)
	}
	run("add", "symlink")
	run("commit", "-m", "unsupported")
	if id, snap, err := New().CaptureBranch(ctx, dir, "deploy", "compose.pkl", limits); err == nil || id != "" || len(snap.Files) != 0 {
		t.Fatal("symlink commit returned")
	}
	if _, _, err := New().CaptureBranch(ctx, dir, "deploy:other", "compose.pkl", limits); err == nil {
		t.Fatal("invalid branch accepted")
	}
}
