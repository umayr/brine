//go:build unix

package api

import (
	"bytes"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	gitpkg "github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/store"
)

type failingRepoStore struct{ store.StackStore }

func (f failingRepoStore) RepoDir(string) (string, error) { return "", errors.New("unsafe repository") }

func TestGitActionsRejectUnavailableRepository(t *testing.T) {
	base := t.TempDir()
	st, err := store.NewFSStore(filepath.Join(base, "data"))
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
	if err = os.WriteFile(filepath.Join(repo, "compose.pkl"), []byte("config {}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = st.CreateFromGit("demo", &store.GitSource{URL: "synthetic"}, ""); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(base, "git-called")
	git := gitpkg.New()
	git.Binary = filepath.Join(base, "git")
	if err = os.WriteFile(git.Binary, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	Mount(r, nil, failingRepoStore{st}, nil, git, "docker", httpx.Timeouts{}, time.Second)
	for _, action := range []string{"pull", "push"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", "/api/stacks/demo/git/"+action, bytes.NewBufferString(`{}`)))
		if w.Code != 400 {
			t.Fatalf("%s: %d", action, w.Code)
		}
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("git ran with an unsafe directory")
	}
}

func TestGitWorkspaceSymlinksCannotExposeOrOverwriteExternalFiles(t *testing.T) {
	base := t.TempDir()
	st, err := store.NewFSStore(filepath.Join(base, "data"))
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
	compose := filepath.Join(repo, "compose.pkl")
	if err = os.WriteFile(compose, []byte("config {}"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ name, content string }{{"preview.json", "synthetic-private-proof"}, {"proof-key", "synthetic-private-key"}, {"policy.json", "synthetic-private-policy"}} {
		target := filepath.Join(base, item.name)
		if err = os.WriteFile(target, []byte(item.content), 0600); err != nil {
			t.Fatal(err)
		}
		if err = os.Symlink(target, filepath.Join(repo, item.name+".pkl")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = st.CreateFromGit("demo", &store.GitSource{URL: "synthetic"}, ""); err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	Mount(r, nil, st, nil, nil, "docker", httpx.Timeouts{}, time.Second)
	for _, item := range []struct{ name, content string }{{"preview.json", "synthetic-private-proof"}, {"proof-key", "synthetic-private-key"}, {"policy.json", "synthetic-private-policy"}} {
		filename := item.name + ".pkl"
		target := filepath.Join(base, item.name)
		url := "/api/stacks/demo/files/" + filename
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
		if w.Code == 200 || bytes.Contains(w.Body.Bytes(), []byte(item.content)) {
			t.Fatalf("symlink read: %d", w.Code)
		}
		w = httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("PUT", url, bytes.NewBufferString(`{"content":"overwrite"}`)))
		if w.Code == 200 || w.Code == 204 {
			t.Fatal("symlink write accepted")
		}
		data, err := os.ReadFile(target)
		if err != nil || string(data) != item.content {
			t.Fatal("external file modified")
		}
		if err = st.SetEntrypoint("demo", filename); err == nil {
			t.Fatal("symlink entrypoint accepted")
		}
	}
	outside := filepath.Join(base, "external")
	if err = os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(outside, "compose.pkl"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(repo, "linked")); err != nil {
		t.Fatal(err)
	}
	if err = st.UpdateGitSource("demo", &store.GitSource{URL: "synthetic", Folder: "linked"}); err == nil {
		t.Fatal("symlink folder accepted")
	}
	if err = os.Remove(compose); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(base, "proof-key"), compose); err != nil {
		t.Fatal(err)
	}
	if _, err = st.ReadPkl("demo"); err == nil {
		t.Fatal("symlink entrypoint read")
	}
	if _, err = st.Update("demo", "overwrite"); err == nil {
		t.Fatal("symlink entrypoint write")
	}
}

func TestGitPullCanRepairMissingOrSymlinkedEntrypoint(t *testing.T) {
	for _, kind := range []string{"missing", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			st, err := store.NewFSStore(filepath.Join(base, "data"))
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
			entry := filepath.Join(repo, "compose.pkl")
			if err = os.WriteFile(entry, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = st.CreateFromGit("demo", &store.GitSource{URL: "synthetic"}, ""); err != nil {
				t.Fatal(err)
			}
			if err = os.Remove(entry); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(base, "outside")
			if err = os.WriteFile(outside, []byte("protected"), 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "symlink" {
				if err = os.Symlink(outside, entry); err != nil {
					t.Fatal(err)
				}
			}
			git := gitpkg.New()
			git.Binary = filepath.Join(base, "git")
			script := "#!/bin/sh\ncase \" $* \" in *\" pull --ff-only \"*) rm -f '" + entry + "'; printf repaired > '" + entry + "';; *\" rev-parse \"*) echo synthetic;; esac\nexit 0\n"
			if err = os.WriteFile(git.Binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			r := chi.NewRouter()
			// This fixture verifies safe source repair, not process-start
			// latency. Concurrent normal/race suites exhausted the old 1s
			// budget (git status killed at 1.01s). Keep every safety assertion.
			Mount(r, nil, st, nil, git, "docker", httpx.Timeouts{}, 10*time.Second)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("POST", "/api/stacks/demo/git/pull", nil))
			if w.Code != 200 {
				t.Fatalf("repair pull: %d %s", w.Code, w.Body.String())
			}
			content, err := st.ReadPkl("demo")
			if err != nil || content != "repaired" {
				t.Fatal("pull did not restore safe content")
			}
			target, err := os.ReadFile(outside)
			if err != nil || string(target) != "protected" {
				t.Fatal("unsafe target modified")
			}
		})
	}
}
