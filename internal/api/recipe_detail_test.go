package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/store"
)

type recipeDetailStore struct {
	store.RecipeStore
	get func(string) (*store.Recipe, error)
}

func (s recipeDetailStore) Get(slug string) (*store.Recipe, error) { return s.get(slug) }

func TestRecipeDetailDiagnostic(t *testing.T) {
	const sentinel = "SYNTHETIC_PRIVATE_RECIPE_DETAIL"
	const slug = "synthetic-snippet"
	recipe := store.Recipe{Slug: slug, Name: "Synthetic Snippet", Body: "value = 1"}
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"success", nil, http.StatusOK},
		{"store failure", errors.New(sentinel), http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := chi.NewRouter()
			rs := recipeDetailStore{get: func(got string) (*store.Recipe, error) {
				if got != slug {
					t.Fatalf("store lookup slug = %q, want %q", got, slug)
				}
				if tc.err != nil {
					return nil, tc.err
				}
				return &recipe, nil
			}}
			Mount(router, nil, nil, rs, nil, "docker", httpx.Timeouts{}, 0)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/recipes/"+slug, nil))
			if w.Code != tc.status || w.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("unexpected status or content type: %d %q", w.Code, w.Header().Get("Content-Type"))
			}
			if tc.err != nil {
				if strings.Contains(w.Body.String(), sentinel) {
					t.Fatal("recipe detail returned the synthetic private store detail")
				}
				if strings.TrimSpace(w.Body.String()) != `{"error":"Recipe unavailable; check the recipe name and local recipe storage"}` {
					t.Error("recipe detail did not return the expected safe diagnostic")
				}
				return
			}
			var got store.Recipe
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got != recipe {
				t.Fatalf("recipe detail = %+v (%v), want %+v", got, err, recipe)
			}
		})
	}
}
