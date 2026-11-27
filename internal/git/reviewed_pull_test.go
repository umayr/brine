package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewedPullUsesExactSourceAndRejectsDirtyWrongBranch(t *testing.T) {
	root := t.TempDir()
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = captureEnvironment(os.Environ())
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("git %v: %v %s", args, e, out)
		}
		return strings.TrimSpace(string(out))
	}
	origin := filepath.Join(root, "approved")
	os.Mkdir(origin, 0700)
	run(origin, "init", "-b", "main")
	run(origin, "config", "user.email", "fixture@example.invalid")
	run(origin, "config", "user.name", "Fixture")
	os.WriteFile(filepath.Join(origin, "compose.pkl"), []byte("first"), 0600)
	run(origin, "add", ".")
	run(origin, "commit", "-m", "first")
	clone := filepath.Join(root, "clone")
	run(root, "clone", origin, clone)
	run(clone, "remote", "set-url", "origin", filepath.Join(root, "not-approved"))
	os.WriteFile(filepath.Join(origin, "compose.pkl"), []byte("second"), 0600)
	run(origin, "add", ".")
	run(origin, "commit", "-m", "second")
	want := run(origin, "rev-parse", "HEAD")
	t.Setenv("GIT_DIR", filepath.Join(root, "hostile.git"))
	t.Setenv("GIT_WORK_TREE", origin)
	t.Setenv("GIT_COMMON_DIR", filepath.Join(root, "hostile-common"))
	changed, e := New().PullBranch(context.Background(), clone, origin, "main")
	if e != nil || !changed {
		t.Fatal("approved explicit pull failed", changed, e)
	}
	if got := run(clone, "rev-parse", "HEAD"); got != want {
		t.Fatal("wrong source applied", got)
	}
	changed, e = New().PullBranch(context.Background(), clone, origin, "main")
	if e != nil || changed {
		t.Fatal("unchanged pull misreported", changed, e)
	}
	short, e := New().ShortRefReviewed(context.Background(), clone)
	if e != nil || !strings.HasPrefix(want, short) || short == "" {
		t.Fatal("recording escaped reviewed repository/environment", short, e)
	}
	run(clone, "config", "url."+filepath.Join(root, "unapproved")+".insteadOf", origin)
	if _, e = New().PullBranch(context.Background(), clone, origin, "main"); e == nil {
		t.Fatal("checkout URL rewrite accepted")
	}
	run(clone, "config", "--remove-section", "url."+filepath.Join(root, "unapproved"))
	parent := filepath.Join(root, "enclosing")
	os.Mkdir(parent, 0700)
	run(parent, "init", "-b", "main")
	run(parent, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "parent")
	nested := filepath.Join(parent, "workspace")
	os.Mkdir(nested, 0700)
	if _, e = New().PullBranch(context.Background(), nested, origin, "main"); e == nil {
		t.Fatal("missing metadata fell through to enclosing repository")
	}
	os.WriteFile(filepath.Join(clone, "compose.pkl"), []byte("dirty"), 0600)
	if _, e = New().PullBranch(context.Background(), clone, origin, "main"); e == nil {
		t.Fatal("dirty source accepted")
	}
	run(clone, "reset", "--hard")
	run(clone, "checkout", "-b", "other")
	if _, e = New().PullBranch(context.Background(), clone, origin, "main"); e == nil {
		t.Fatal("wrong branch accepted")
	}
}
