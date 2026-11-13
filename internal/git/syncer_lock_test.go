package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/store"
)

func TestSyncHoldsDeploymentLockAcrossWorkspacePull(t *testing.T) {
	st, err := store.NewFSStore(t.TempDir())
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
	if _, err = st.CreateFromGit("demo", &store.GitSource{URL: "synthetic"}, ""); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "git")
	marker := filepath.Join(dir, "called")
	if err = os.WriteFile(bin, []byte("#!/bin/sh\necho called > '"+marker+"'\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	r := dill.New("unused")
	// Budget process startup under full-suite race instrumentation separately
	// from the short observation proving Git cannot run while the lock is held.
	s := NewSyncer(st, &Runner{Binary: bin}, r, 5*time.Second)
	unlock := r.LockStack("demo")
	done := make(chan struct{})
	go func() { defer close(done); s.sync(context.Background(), "demo") }()
	select {
	case <-done:
		t.Fatal("sync bypassed lock")
	case <-time.After(30 * time.Millisecond):
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("git ran while locked")
	}
	unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("sync did not finish")
	}
	if _, err = os.Stat(marker); err != nil {
		t.Fatal("git never ran")
	}
}
