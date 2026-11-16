package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/store"
)

func TestProjectPreviewSafe(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewFSStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.Create("app", "secret-content"); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "stacks", "app")
	if err = os.WriteFile(filepath.Join(root, "<img src=x>.txt"), []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, ".git", "private"), []byte("hidden"), 0600); err != nil {
		t.Fatal(err)
	}
	h := New(nil, st, nil, nil, "docker", false, nil, 0)
	r := chi.NewRouter()
	Mount(r, h, httpx.Timeouts{})
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}
	w := get("/web/stacks/app/project-preview")
	body := w.Body.String()
	for _, s := range []string{"compose.pkl", "&lt;img src=x&gt;.txt", "3 bytes", "meta.json", "Transfer unsupported", "versioned provisional contract", "revalidated by the server"} {
		if !strings.Contains(body, s) {
			t.Fatalf("missing %q: %s", s, body)
		}
	}
	for _, s := range []string{"secret-content", "<img src=x>", ".git/private"} {
		if strings.Contains(body, s) {
			t.Fatalf("unexpected %q", s)
		}
	}
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d", w.Code)
	}
	if err = os.Remove(filepath.Join(root, "<img src=x>.txt")); err != nil {
		t.Fatal(err)
	}
	w = get("/web/stacks/app/project-preview")
	if w.Code != 200 || strings.Contains(w.Body.String(), "Transfer unsupported") || !strings.Contains(w.Body.String(), "versioned provisional contract") {
		t.Fatalf("compatible preview: %d %s", w.Code, w.Body.String())
	}
	if err = os.Symlink("/etc/passwd", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	w = get("/web/stacks/app/project-preview")
	if w.Code != 422 || strings.Contains(w.Body.String(), root) || strings.Contains(w.Body.String(), "link") {
		t.Fatalf("unsafe error: %d %s", w.Code, w.Body.String())
	}
	if err = os.Remove(filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "large"), make([]byte, 4<<20+1), 0600); err != nil {
		t.Fatal(err)
	}
	w = get("/web/stacks/app/project-preview")
	if w.Code != 422 || strings.Contains(w.Body.String(), "large") {
		t.Fatalf("oversize: %d %s", w.Code, w.Body.String())
	}
	w = get("/web/stacks/..%2Fapp/project-preview")
	if w.Code == 200 {
		t.Fatal("traversal accepted")
	}
}
