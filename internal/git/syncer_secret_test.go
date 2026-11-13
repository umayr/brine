//go:build unix

package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/secretbindings"
	"github.com/umayr/brine/internal/store"
)

func TestSyncRejectsOldDillInStrictMode(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(home, ".brine-git-strict-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	workspace := filepath.Join(dir, "workspace")
	private := filepath.Join(dir, "state")
	if err = os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	st, err := store.NewFSStore(workspace)
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
	stack, err := st.CreateFromGit("demo", &store.GitSource{URL: "synthetic"}, "")
	if err != nil {
		t.Fatal(err)
	}
	state, err := secretbindings.Open(private, workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	gitBin := filepath.Join(dir, "git")
	counter := filepath.Join(dir, "counter")
	gitScript := "#!/bin/sh\nwhile [ \"$1\" = -c ]; do shift 2; done\ncase \"$1 $2\" in 'status --porcelain') exit 0;; 'pull --ff-only') exit 0;; 'rev-parse HEAD') if [ -e '" + counter + "' ]; then echo new; else echo old; touch '" + counter + "'; fi;; 'rev-parse --short') echo new;; esac\n"
	if err = os.WriteFile(gitBin, []byte(gitScript), 0700); err != nil {
		t.Fatal(err)
	}
	dillBin := filepath.Join(dir, "dill")
	up := filepath.Join(dir, "up")
	discovery := filepath.Join(dir, "discovery")
	script := "#!/bin/sh\ncase \" $* \" in *\" secret-capabilities \"*) touch '" + discovery + "';; *\" up \"*) touch '" + up + "';; esac\nexit 1\n"
	if err = os.WriteFile(dillBin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	runner := dill.New(dillBin)
	secret := &dill.SecretRunner{Runner: runner, State: state, Coordinator: &runner.Coordinator, Engine: "docker", Socket: "/tmp/synthetic.sock", Policy: "/tmp/synthetic-policy", Target: "lab-a"}
	runner.StrictActions = &dill.StrictController{Secret: secret, Resolve: func(path string) (string, error) {
		if path != stack.PklPath {
			t.Fatal("wrong path")
		}
		return "demo", nil
	}, LegacyGate: func(context.Context, string) error { return nil }}
	NewSyncer(st, &Runner{Binary: gitBin}, runner, 10*time.Second).sync(context.Background(), "demo")
	if _, err = os.Stat(counter); err != nil {
		t.Fatal("fake Git never read changed HEAD:", err)
	}
	updated, err := st.Get("demo")
	if err != nil || updated.Git.LastCommit != "new" {
		t.Fatalf("changed pull not recorded: %+v %v", updated, err)
	}
	if _, err = os.Stat(discovery); err != nil {
		t.Fatal("capability rejection not exercised:", err)
	}
	if _, err = os.Stat(up); !os.IsNotExist(err) {
		t.Fatal("auto-sync used old binary to deploy")
	}
}
