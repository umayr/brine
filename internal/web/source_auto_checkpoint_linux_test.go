//go:build linux

package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/snapshothistory"
	"github.com/umayr/brine/internal/store"
)

func TestSourceAutoCheckpointSavesWithVisibleWarningWhenArchiveIsFull(t *testing.T) {
	home := os.Getenv("HOME")
	if !strings.HasPrefix(home, "/brine-go-test-home.") {
		if os.Getenv("CI") != "" {
			t.Fatal("CI must provision trusted synthetic HOME")
		}
		t.Skip("run through scripts/ci-test-go.sh for private Linux archive coverage")
	}
	root, err := os.MkdirTemp(home, "brine-auto-test-")
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	data, archive, keys := filepath.Join(root, "data"), filepath.Join(root, "archive"), filepath.Join(root, "keys")
	for _, dir := range []string{data, archive, keys} {
		check(os.Mkdir(dir, 0700))
	}
	key := filepath.Join(keys, "source-key")
	check(os.WriteFile(key, make([]byte, 32), 0600))
	st, err := store.NewFSStore(data)
	check(err)
	_, err = st.Create("demo", "value = 1\n")
	check(err)
	check(snapshothistory.InitializeSourceArchive(archive, key, data))
	vault, err := snapshothistory.OpenSourceArchive(archive, key, data, snapshothistory.SourceArchiveLimits{Objects: 2})
	check(err)
	defer vault.Close()
	a, err := auth.New(auth.Config{User: "operator", Password: "synthetic-password", APIToken: "synthetic-token"})
	check(err)
	run := dill.New("/must-not-execute")
	run.Socket = "/synthetic.sock"
	run.BeforeLegacyAction = func(context.Context, string) error { return nil }
	run.CheckSourceProtection = func(string) error { return nil }
	run.Coordinator.CheckAdmission = func(string) error { return nil }
	h := &Handler{store: st, auth: a, agent: agent.NewLocalAgent(nil, run)}
	check(h.EnableSourceHistory(vault))
	router := chi.NewRouter()
	router.Use(a.Middleware)
	router.Post("/login", a.HandleLogin)
	router.Get("/token", func(w http.ResponseWriter, r *http.Request) { w.Header().Set("X-Test-CSRF", a.CSRFFor(r)) })
	Mount(router, h, httpx.Timeouts{})
	req := func(method, path string, f url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(f.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	login := req("POST", "/login", url.Values{"user": {"operator"}, "password": {"synthetic-password"}}, nil)
	if login.Code != 303 || len(login.Result().Cookies()) != 1 {
		t.Fatalf("login: %d", login.Code)
	}
	cookie := login.Result().Cookies()[0]
	csrf := req("GET", "/token", nil, cookie).Header().Get("X-Test-CSRF")
	const history = "/web/stacks/demo/history"
	toggle := func(value string) {
		t.Helper()
		w := req("POST", history+"/auto-checkpoint", url.Values{"enabled": {value}, "_csrf": {csrf}}, cookie)
		if w.Code != 303 {
			t.Fatalf("toggle %s: %d %s", value, w.Code, w.Body.String())
		}
	}
	revisions := func(want int) {
		t.Helper()
		v, e := st.ReviewSource(context.Background(), "demo")
		check(e)
		if len(v.Revisions) != want {
			t.Fatalf("got %d revisions, want %d", len(v.Revisions), want)
		}
	}
	save := func(content string, missed bool) {
		t.Helper()
		w := req("PUT", "/web/stacks/demo", url.Values{"pkl_content": {content}, "active_file": {"compose.pkl"}, "_csrf": {csrf}}, cookie)
		if w.Code != 200 || (w.Header().Get(store.SourceCheckpointHeader) == "not-retained") != missed || strings.Contains(w.Header().Get("HX-Redirect"), "checkpoint=not-retained") != missed {
			t.Fatalf("save status=%d headers=%v", w.Code, w.Header())
		}
		got, e := st.ReadPkl("demo")
		check(e)
		if got != content {
			t.Fatalf("saved source=%q want %q", got, content)
		}
	}
	global := func(value string) {
		t.Helper()
		w := req("POST", "/settings/source-history", url.Values{"enabled": {value}, "_csrf": {csrf}}, cookie)
		if w.Code != 303 {
			t.Fatalf("global default %s: %d %s", value, w.Code, w.Body.String())
		}
		w = req("GET", "/settings/source-history", nil, cookie)
		label := "Enable Auto-Checkpoint"
		if value == "yes" {
			label = "Disable Auto-Checkpoint"
		}
		if w.Code != 200 || !strings.Contains(w.Body.String(), label) {
			t.Fatalf("settings state %s: %d", value, w.Code)
		}
	}
	revisions(0)
	global("yes")
	revisions(0) // No global backfill.
	toggle("no")
	save("value = 0\n", false)
	revisions(0) // Explicit off wins.
	toggle("inherit")
	revisions(0)
	save("value = 1\n", false)
	revisions(1)
	global("no")
	save("value = 2\n", false)
	revisions(1)
	toggle("yes")
	revisions(2) // Explicit enable retains the current baseline.
	save("value = 2\n", false)
	revisions(2)
	save("value = 3\n", true) // Explicit on wins; quota failure does not lose the saved edit.
	revisions(2)
	v, err := st.ReviewSource(context.Background(), "demo")
	check(err)
	w := req("POST", history+"/review", url.Values{"kind": {"diff"}, "revision": {v.Revisions[1]}, "_csrf": {csrf}}, cookie)
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, "Comparing r1 → r2") || !strings.Contains(body, "value = 2") || strings.Contains(body, "/history/confirm") {
		t.Fatalf("unexpected read-only comparison: %d %s", w.Code, body)
	}
	toggle("inherit")
	save("value = 4\n", false)
	revisions(2)
}
