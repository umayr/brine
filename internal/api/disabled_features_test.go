//go:build unix

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/httpx"
)

func TestDisabledHandlersAndRoutedAPI(t *testing.T) {
	a, err := auth.New(auth.Config{User: "admin", Password: "password", APIToken: "synthetic-token"})
	if err != nil {
		t.Fatal(err)
	}

	r := chi.NewRouter()
	r.Use(a.Middleware)
	Mount(r, nil, nil, nil, nil, "docker", httpx.Timeouts{}, time.Second)
	for _, tc := range []struct {
		name, method, path string
		fn                 http.HandlerFunc
	}{
		{"get", "GET", "/api/stacks/demo/secret-bindings", retiredSecretBindings},
		{"put", "PUT", "/api/stacks/demo/secret-bindings", retiredSecretBindings},
		{"status", "POST", "/api/stacks/demo/secret-bindings/status", retiredSecretBindings},
		{"preview", "POST", "/api/stacks/demo/secret-bindings/preview", retiredSecretBindings},
		{"confirm", "POST", "/api/stacks/demo/secret-bindings/confirm", retiredSecretBindings},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, handler := range []http.Handler{r, a.Middleware(tc.fn)} {
				req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
				req.Header.Set("Authorization", "Bearer synthetic-token")
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, req)
				if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "disabled") {
					t.Fatalf("got %d %s", w.Code, w.Body.String())
				}
			}
		})
	}
}

func TestRetiredLocalPreviewNotRouted(t *testing.T) {
	a, err := auth.New(auth.Config{User: "admin", Password: "password", APIToken: "synthetic-token"})
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Use(a.Middleware)
	Mount(r, nil, nil, nil, nil, "docker", httpx.Timeouts{}, time.Second)
	req := httptest.NewRequest(http.MethodPost, "/api/stacks/demo/operations/preview", strings.NewReader(`{"action":"up"}`))
	req.Header.Set("Authorization", "Bearer synthetic-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("retired preview: got %d %s", w.Code, w.Body.String())
	}
}
