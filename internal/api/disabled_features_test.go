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
	h := &stackHandler{}
	if _, err := NewAttendedContext(nil, nil, nil); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("attended constructor: %v", err)
	}
	if err := MountAttended(chi.NewRouter(), nil, nil, nil, nil, "docker", httpx.Timeouts{}, time.Second, nil, nil); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("attended mount: %v", err)
	}
	r := chi.NewRouter()
	r.Use(a.Middleware)
	Mount(r, nil, nil, nil, nil, "docker", httpx.Timeouts{}, time.Second)
	for _, tc := range []struct {
		name, method, path string
		fn                 http.HandlerFunc
	}{
		{"get", "GET", "/api/stacks/demo/secret-bindings", h.secretBindingsGet},
		{"put", "PUT", "/api/stacks/demo/secret-bindings", h.secretBindingsPut},
		{"status", "POST", "/api/stacks/demo/secret-bindings/status", h.secretStatus},
		{"preview", "POST", "/api/stacks/demo/secret-bindings/preview", h.secretPreview},
		{"confirm", "POST", "/api/stacks/demo/secret-bindings/confirm", h.secretConfirm},
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
	for name, fn := range map[string]http.HandlerFunc{"preview": h.attendedPreview, "apply": func(w http.ResponseWriter, r *http.Request) { h.attendedApply(w, r, "up") }} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			fn(w, httptest.NewRequest("POST", "/", nil))
			if w.Code != 503 || !strings.Contains(w.Body.String(), "disabled") {
				t.Fatalf("got %d %s", w.Code, w.Body.String())
			}
		})
	}
}
