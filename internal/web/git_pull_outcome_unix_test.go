//go:build unix

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

func TestGitPullCompletedWithoutRevision(t *testing.T) {
	for _, mode := range []string{"changed", "unchanged"} {
		t.Run(mode, func(t *testing.T) {
			st, err := store.NewFSStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			dir, err := st.RepoDir("synthetic")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "compose.pkl"), []byte("x"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := st.CreateFromGit("synthetic", &store.GitSource{URL: "example"}, ""); err != nil {
				t.Fatal(err)
			}
			binary := filepath.Join(t.TempDir(), "git-fixture")
			t.Setenv("BRINE_TEST_PULL_MODE", mode)
			script := `#!/bin/sh
[ "$1" = -c ] && [ "$2" = core.hooksPath=/dev/null ] || exit 2
shift 2
case "$*" in
  'status --porcelain') exit 0;;
  'rev-parse HEAD')
    if [ -f pulled ] && [ "$BRINE_TEST_PULL_MODE" = changed ]; then printf 'after\n'; else printf 'before\n'; fi;;
  'pull --ff-only --quiet') [ ! -f pulled ] && : > pulled;;
  '--git-dir=.git rev-parse --short HEAD') printf 'private revision diagnostic\n' >&2; exit 1;;
  *) exit 2;;
esac
`
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/web/stacks/synthetic/git/pull", nil)
			route := chi.NewRouteContext()
			route.URLParams.Add("name", "synthetic")
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
			w := httptest.NewRecorder()
			(&Handler{store: st, git: &git.Runner{Binary: binary}, deployTimeout: 5 * time.Second}).stackGitPull(w, req)
			body := w.Body.String()
			if w.Code != http.StatusOK || !strings.Contains(body, "ui-notice-warning") || !strings.Contains(body, "Git pull completed, but the current revision is unavailable.") ||
				strings.Contains(body, "private revision diagnostic") || strings.Contains(body, "Pulled — now at") || strings.Contains(body, "Already up to date") || strings.Contains(body, "retry") {
				t.Fatalf("HTTP %d body %q", w.Code, body)
			}
		})
	}
}
