package git

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/store"
)

// Git diagnostics are trusted-operator data, not a redacted secret channel.
// Unlike Dill, Git intentionally inherits service environment/config/helpers.
// Drive the marker from an actual fake Git stderr producer to BOTH durable
// meta.json and service logging, then prove successful sync clears the outcome.
func TestDiagnosticSentinelGitFailurePersistedAndLogged(t *testing.T) {
	const marker = "synthetic-deployment-git-diagnostic"
	t.Setenv("BRINE_SYNTHETIC_GIT_DIAGNOSTIC", marker)
	dir := t.TempDir()
	root := filepath.Join(dir, "state")
	st, err := store.NewFSStore(root)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := st.RepoDir("demo")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(repo, "compose.pkl"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = st.CreateFromGit("demo", &store.GitSource{URL: "synthetic-never-fetched"}, ""); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "git")
	fail := filepath.Join(dir, "fail")
	calls := filepath.Join(dir, "calls")
	script := fmt.Sprintf(`#!/bin/sh
while [ "$1" = '-c' ]; do shift 2; done
printf '%%s\n' "$1" >> %q
case "$1" in
 status) exit 0;;
 rev-parse) echo synthetic-commit; exit 0;;
 pull) if [ -e %q ]; then printf '%%s\n' "$BRINE_SYNTHETIC_GIT_DIAGNOSTIC" >&2; exit 2; fi; exit 0;;
esac
exit 9
`, calls, fail)
	if err = os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(fail, []byte("fail"), 0600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	syncer := NewSyncer(st, &Runner{Binary: bin}, dill.New("/synthetic-must-not-run"), 10*time.Second)
	syncer.sync(context.Background(), "demo")
	got, err := st.Get("demo")
	if err != nil || !strings.Contains(got.Git.SyncError, marker) || !strings.Contains(logs.String(), marker) {
		t.Fatal("Git stderr did not reach stored outcome and service log")
	}
	metaFound := false
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "meta.json" {
			return nil
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		if bytes.Contains(b, []byte(marker)) {
			metaFound = true
		}
		return nil
	})
	if err != nil || !metaFound {
		t.Fatal("diagnostic missing from durable meta.json")
	}
	b, err := os.ReadFile(calls)
	if err != nil || !strings.Contains(string(b), "pull") {
		t.Fatal("diagnostic producer not exercised")
	}
	if err = os.Remove(fail); err != nil {
		t.Fatal(err)
	}
	syncer.sync(context.Background(), "demo")
	reopened, err := store.NewFSStore(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err = reopened.Get("demo")
	if err != nil || got.Git.SyncError != "" || got.Git.LastCommit != "synthetic-commit" {
		t.Fatal("successful sync did not durably clear failure")
	}
}
