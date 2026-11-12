//go:build unix

package api

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/httpx"
)

func TestSecretBindingsUnavailableInNoAuthMode(t *testing.T) {
	a, err := auth.New(auth.Config{NoAuth: true})
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Use(a.Middleware)
	Mount(r, nil, nil, nil, nil, "docker", httpx.Timeouts{}, time.Second)
	req := httptest.NewRequest("GET", "http://127.0.0.1/api/stacks/demo/secret-bindings", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("no-auth mode: %d", w.Code)
	}
}
