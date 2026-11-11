package web

import (
	"bytes"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/httpx"
)

func TestDevReloadsTemplatesAndStaticFiles(t *testing.T) {
	root := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(root, "internal", "web", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("templates/layout.html", `layout:{{block "body" .}}{{end}}`)
	write("templates/example.html", `{{define "body"}}first{{end}}`)
	write("templates/nested/example.html", `{{define "body"}}nested{{end}}`)
	write("templates/partials/example.html", "first partial")
	write("static/example.txt", "first static")
	t.Chdir(root)
	h := New(nil, nil, nil, nil, "docker", true, nil, 0)
	router := chi.NewRouter()
	Mount(router, h, httpx.Timeouts{})

	check := func(page, partial, static string) {
		t.Helper()
		ts, err := h.templates()
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name string
			want string
		}{
			{"example.html", page},
			{"nested/example.html", strings.SplitN(page, ":", 2)[0] + ":nested"},
		} {
			var out bytes.Buffer
			if ts.pages[tc.name] == nil {
				t.Fatalf("missing page %s", tc.name)
			}
			if err := ts.pages[tc.name].Execute(&out, nil); err != nil {
				t.Fatal(err)
			}
			if out.String() != tc.want {
				t.Fatalf("%s: got %q, want %q", tc.name, out.String(), tc.want)
			}
		}
		var out bytes.Buffer
		if err := ts.partials["example"].Execute(&out, nil); err != nil {
			t.Fatal(err)
		}
		if out.String() != partial {
			t.Fatalf("partial: got %q, want %q", out.String(), partial)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/static/example.txt", nil))
		if w.Code != http.StatusOK || w.Body.String() != static {
			t.Fatalf("static: status %d, body %q, want %q", w.Code, w.Body.String(), static)
		}
	}
	check("layout:first", "first partial", "first static")
	write("templates/layout.html", `updated:{{block "body" .}}{{end}}`)
	write("templates/example.html", `{{define "body"}}second{{end}}`)
	write("templates/partials/example.html", "second partial")
	write("static/example.txt", "second static")
	check("updated:second", "second partial", "second static")

	// A bad edit must fail instead of returning the last good template set.
	write("templates/example.html", `{{define`)
	if _, err := h.templates(); err == nil {
		t.Fatal("invalid edit unexpectedly parsed")
	}
	write("templates/example.html", `{{define "body"}}recovered{{end}}`)
	check("updated:recovered", "second partial", "second static")
}

func TestProductionUsesEmbeddedAssets(t *testing.T) {
	h := &Handler{devFS: fstest.MapFS{
		"templates/layout.html": {Data: []byte(`{{invalid`)},
		"static/theme.js":       {Data: []byte("disk content must not be served")},
	}}
	ts, err := h.templates()
	if err != nil {
		t.Fatal(err)
	}
	if ts.pages["dashboard.html"] == nil {
		t.Fatal("embedded dashboard missing")
	}
	again, err := h.templates()
	if err != nil || again != ts {
		t.Fatalf("production template cache not reused: %v", err)
	}
	want, err := fs.ReadFile(staticFS, "static/theme.js")
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	Mount(router, h, httpx.Timeouts{})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/static/theme.js", nil))
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), want) {
		t.Fatalf("production static assets did not come from embedded FS: status %d", w.Code)
	}
}

func TestDevTemplateErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source fs.FS
		want   string
		is     error
	}{
		{"nil source", nil, "template source", fs.ErrInvalid},
		{"missing layout", fstest.MapFS{}, "read layout", fs.ErrNotExist},
		{"invalid layout", fstest.MapFS{
			"templates/layout.html":  {Data: []byte(`{{define`)},
			"templates/example.html": {Data: []byte("page")},
		}, "parse layout", nil},
		{"invalid page", fstest.MapFS{
			"templates/layout.html":  {Data: []byte("layout")},
			"templates/example.html": {Data: []byte(`{{define`)},
		}, "parse page", nil},
		{"invalid partial", fstest.MapFS{
			"templates/layout.html":           {Data: []byte("layout")},
			"templates/partials/example.html": {Data: []byte(`{{define`)},
		}, "parse partial", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{dev: true, devFS: tc.source}
			_, err := h.templates()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want error containing %q", err, tc.want)
			}
			if tc.is != nil && !errors.Is(err, tc.is) {
				t.Fatalf("got %v, want wrapped %v", err, tc.is)
			}
			w := httptest.NewRecorder()
			h.render(w, httptest.NewRequest(http.MethodGet, "/", nil), "example.html", nil)
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("render status %d, want 500", w.Code)
			}
		})
	}
}

func TestDevStaticPathsStayUnderStatic(t *testing.T) {
	h := &Handler{dev: true, devFS: fstest.MapFS{
		"templates/private.html": {Data: []byte("private template sentinel")},
		"private.txt":            {Data: []byte("private root sentinel")},
		"static/example.txt":     {Data: []byte("public")},
	}}
	router := chi.NewRouter()
	Mount(router, h, httpx.Timeouts{})
	for _, path := range []string{
		"/static/missing.txt",
		"/static/private.txt",
		"/static/templates/private.html",
		"/static/../private.txt",
		"/static/../templates/private.html",
		"/static/%2e%2e/templates/private.html",
		"/static/%2e%2e%2ftemplates%2fprivate.html",
	} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
			if w.Code == http.StatusOK || strings.Contains(w.Body.String(), "sentinel") {
				t.Fatalf("path escaped static root: status %d, body %q", w.Code, w.Body.String())
			}
		})
	}
}

// A SubFS can fail during mount; ordinary missing files fail during requests.
type failingDevSubFS struct{ fstest.MapFS }

func (failingDevSubFS) Sub(string) (fs.FS, error) { return nil, fs.ErrPermission }

func TestDevStaticFilesystemErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source fs.FS
		want   int
	}{
		{"nil source", nil, http.StatusInternalServerError},
		{"sub error", failingDevSubFS{}, http.StatusInternalServerError},
		{"missing static directory", fstest.MapFS{}, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := chi.NewRouter()
			Mount(router, &Handler{dev: true, devFS: tc.source}, httpx.Timeouts{})
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/static/example.txt", nil))
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d", w.Code, tc.want)
			}
		})
	}
}
