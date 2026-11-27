//go:build linux || darwin

package store

import (
	"github.com/umayr/brine/internal/projectsnapshot"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectSnapshotRemotePolicyDoesNotErasePending(t *testing.T) {
	st, err := NewFSStore(t.TempDir())
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
	if err = os.WriteFile(filepath.Join(repo, "compose.pkl"), []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	stack, err := st.CreateFromGit("demo", &GitSource{URL: "https://example.invalid/repo.git"}, "compose.pkl")
	if err != nil {
		t.Fatal(err)
	}
	limits := projectsnapshot.Limits{MaxFiles: 10, MaxEntries: 20, MaxDepth: 4, MaxFileBytes: 1024, MaxTotalBytes: 4096}
	p := RemoteGitPolicy{Enabled: false, Host: "host", Target: strings.Repeat("a", 64), Source: stack.RemoteGitSource(), Phase: "ready"}
	if err = st.SwapRemoteGit("demo", p.Source, nil, &p); err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.ProjectSnapshot("demo", limits); err != nil {
		t.Fatal("disabled resolved policy blocked reviewed checkout", err)
	}
	next := p
	next.Enabled = true
	if err = st.SwapRemoteGit("demo", p.Source, &p, &next); err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.ProjectSnapshot("demo", limits); err == nil {
		t.Fatal("enabled automation ignored")
	}
	old := next
	next.Enabled = false
	next.Commit = strings.Repeat("b", 40)
	next.Seen = next.Commit
	next.Request = strings.Repeat("c", 32)
	next.Journal = strings.Repeat("d", 32)
	next.Phase = "pending"
	next.Reason = "unknown"
	if err = st.SwapRemoteGit("demo", p.Source, &old, &next); err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.ProjectSnapshot("demo", limits); err == nil {
		t.Fatal("disabled automation erased unknown request")
	}
}
