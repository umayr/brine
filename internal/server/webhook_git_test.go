package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/webhooks"
)

func TestWebhookGitSyncUsesExistingFastForwardAndStatus(t *testing.T) {
	s, st, _, logPath := dispatcherServer(t)
	home := t.TempDir()
	origin := filepath.Join(t.TempDir(), "origin")
	runGit := func(args ...string) {
		t.Helper()
		base := []string{"-c", "core.hooksPath=/dev/null", "-c", "protocol.file.allow=always", "-c", "user.name=fixture", "-c", "user.email=fixture@example.test"}
		cmd := exec.Command("git", append(base, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "HOME="+home)
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("synthetic Git fixture: %v %s", e, out)
		}
	}
	runGit("init", "-b", "main", origin)
	os.WriteFile(filepath.Join(origin, "compose.pkl"), []byte("config { engine = \"docker\" }\n"), 0600)
	runGit("-C", origin, "add", ".")
	runGit("-C", origin, "commit", "-m", "fixture initial")
	if e := st.Delete("example"); e != nil {
		t.Fatal(e)
	}
	repo, e := st.RepoDir("example")
	if e != nil {
		t.Fatal(e)
	}
	os.MkdirAll(filepath.Dir(repo), 0700)
	runGit("clone", origin, repo)
	stack, e := st.CreateFromGit("example", &store.GitSource{URL: origin, Branch: "main"}, "compose.pkl")
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(origin, "compose.pkl"), []byte("config { engine = \"docker\" }\n// next fixture revision\n"), 0600)
	runGit("-C", origin, "add", ".")
	runGit("-C", origin, "commit", "-m", "fixture next")
	d := s.webhookDispatcher.(*webhookDispatcher)
	result := d.Execute(context.Background(), webhooks.Trigger{StackID: webhookStackID(stack), TargetID: d.localTargetID(), Action: "git-sync"}, webhooks.IncomingRequest{ID: "req_git_fixture"})
	if result.Status != "completed" {
		t.Fatal("sync not completed", result)
	}
	content, e := st.ReadPkl("example")
	if e != nil || !strings.Contains(content, "next fixture revision") {
		t.Fatal("configured source not synchronized", e)
	}
	current, _ := st.Get("example")
	if current.Git.LastCommit == "" || current.Git.LastSyncAt == nil {
		t.Fatal("existing sync status not recorded")
	}
	if _, e := os.Stat(logPath); !os.IsNotExist(e) {
		t.Fatal("source sync implicitly deployed")
	}
}
