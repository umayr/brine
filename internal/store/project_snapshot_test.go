//go:build linux || darwin

package store

import (
	"github.com/umayr/brine/internal/projectsnapshot"
	"os"
	"path/filepath"
	"testing"
)

func TestProjectSnapshotGitFolderAnchorsAndScope(t *testing.T) {
	data := t.TempDir()
	st, err := NewFSStore(data)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := st.RepoDir("git-project")
	if err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(repo, "apps", "db")
	if err = os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(project, "compose.pkl"), []byte("project"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(repo, "excluded.txt"), []byte("not part of project"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = st.CreateFromGit("git-project", &GitSource{URL: "https://example.invalid/repo.git", Folder: "apps/db"}, "compose.pkl"); err != nil {
		t.Fatal(err)
	}
	limits := projectsnapshot.Limits{MaxFiles: 10, MaxEntries: 20, MaxDepth: 5, MaxFileBytes: 1024, MaxTotalBytes: 4096}
	stack, snap, err := st.ProjectSnapshot("git-project", limits)
	if err != nil || stack.Git.Folder != "apps/db" || len(snap.Files) != 1 || string(snap.Files[0].Content) != "project" {
		t.Fatal("wrong workspace captured", err)
	}
	// Replace an intermediate component with an external symlink. No bytes from
	// outside the trusted workspace may be captured, even with the same entrypoint.
	outside := t.TempDir()
	if err = os.Mkdir(filepath.Join(outside, "db"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(outside, "db", "compose.pkl"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	apps := filepath.Join(repo, "apps")
	if err = os.Rename(apps, apps+".saved"); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, apps); err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.ProjectSnapshot("git-project", limits); err == nil {
		t.Fatal("intermediate folder symlink followed")
	}
}
