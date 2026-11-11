package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/store"
)

func TestGitPullFailurePreservesLastCommit(t *testing.T) {
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
	if err := st.UpdateGitStatus("test", "previous", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/", nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("name", "test")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
	w := httptest.NewRecorder()
	(&Handler{store: st, git: git.New(), deployTimeout: time.Second}).stackGitPull(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	got, err := st.Get("test")
	if err != nil {
		t.Fatal(err)
	}
	if got.Git.LastCommit != "previous" || !strings.Contains(got.Git.SyncError, "check working tree") {
		t.Fatalf("status = %+v", got.Git)
	}
}
