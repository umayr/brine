package store_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/umayr/brine/internal/store"
)

func TestGitStatusConcurrentSuccessAndFailure(t *testing.T) {
	st, err := store.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir, err := st.RepoDir("test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.pkl"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateFromGit("test", &store.GitSource{URL: "example"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateGitStatus("test", "a", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	captured := make(chan struct{})
	success := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		snapshot, err := st.Get("test")
		if err != nil {
			done <- err
			return
		}
		close(captured)
		<-success
		done <- st.UpdateGitStatus("test", snapshot.Git.LastCommit, "pull failed", time.Now())
	}()
	<-captured
	if err := st.UpdateGitStatus("test", "b", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	close(success)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got, err := st.Get("test")
	if err != nil {
		t.Fatal(err)
	}
	if got.Git.LastCommit != "b" || got.Git.SyncError != "pull failed" {
		t.Fatalf("status = %+v", got.Git)
	}
}
