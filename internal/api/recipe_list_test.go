package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/store"
)

type recipeListStore struct {
	store.RecipeStore
	err error
}

func (s recipeListStore) List() ([]*store.Recipe, error) {
	if s.err != nil {
		return nil, s.err
	}
	return []*store.Recipe{}, nil
}

func TestRecipeListDiagnostic(t *testing.T) {
	const sentinel = "SYNTHETIC_PRIVATE_RECIPE_STORE_DETAIL"
	for _, tc := range []struct {
		name   string
		err    error
		status int
		body   string
	}{
		{"success", nil, http.StatusOK, `[]`},
		{"store failure", errors.New(sentinel), http.StatusInternalServerError, `{"error":"Recipe list unavailable; check local recipe storage"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := chi.NewRouter()
			Mount(router, nil, nil, recipeListStore{err: tc.err}, nil, "docker", httpx.Timeouts{}, 0)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/recipes", nil))
			if w.Code != tc.status || w.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("unexpected status or content type: %d %q", w.Code, w.Header().Get("Content-Type"))
			}
			if strings.Contains(w.Body.String(), sentinel) {
				t.Fatal("recipe list returned the synthetic private store detail")
			}
			if strings.TrimSpace(w.Body.String()) != tc.body {
				t.Error("response did not match the expected recipe list or safe diagnostic")
			}
		})
	}
}
