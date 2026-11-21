package api

import (
	"bytes"
	"encoding/json"
	"errors"
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

func TestRecipeMetadataUpdateWithUnavailableBody(t *testing.T) {
	for _, damage := range []string{"missing", "directory", "permission denied"} {
		t.Run(damage, func(t *testing.T) {
			rs, dir := newRecipeMetadataFixture(t)
			bodyPath := filepath.Join(dir, "body.pkl")
			if damage == "permission denied" {
				if err := os.Chmod(bodyPath, 0000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(bodyPath, 0600) })
				if _, err := os.ReadFile(bodyPath); err == nil {
					t.Skip("test user can read owner-inaccessible files")
				} else if !errors.Is(err, os.ErrPermission) {
					t.Fatalf("expected permission error reading fixture: %v", err)
				}
			} else {
				if err := os.Remove(bodyPath); err != nil {
					t.Fatal(err)
				}
				if damage == "directory" {
					if err := os.Mkdir(bodyPath, 0700); err != nil {
						t.Fatal(err)
					}
				}
			}

			metaPath := filepath.Join(dir, "meta.json")
			before, err := os.ReadFile(metaPath)
			if err != nil {
				t.Fatal(err)
			}
			w := patchRecipeMetadata(rs, "audit-fixture")
			if w.Code != http.StatusInternalServerError || strings.TrimSpace(w.Body.String()) != `{"error":"Recipe metadata update unavailable; check local recipe storage"}` {
				t.Errorf("response = %d %q, want safe 500", w.Code, w.Body.String())
			}
			after, err := os.ReadFile(metaPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Errorf("metadata changed after body read failure: %v", err)
			}
		})
	}
}

func TestRecipeMetadataUpdatePreservesEmptyBody(t *testing.T) {
	rs, dir := newRecipeMetadataFixture(t)
	bodyPath := filepath.Join(dir, "body.pkl")
	if err := os.WriteFile(bodyPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	w := patchRecipeMetadata(rs, "audit-fixture")
	if w.Code != http.StatusOK {
		t.Fatalf("response = %d %q, want 200", w.Code, w.Body.String())
	}
	var response store.Recipe
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Name != "Renamed" || response.Body != "" {
		t.Fatalf("response did not preserve empty body and update name: %+v, %v", response, err)
	}
	got, err := rs.Get("audit-fixture")
	if err != nil || got.Name != "Renamed" || got.Body != "" {
		t.Errorf("persisted recipe did not preserve the empty body: %+v, %v", got, err)
	}
}

func TestRecipeMetadataUpdateNotFound(t *testing.T) {
	rs, _ := newRecipeMetadataFixture(t)
	for _, slug := range []string{"missing", "invalid.slug"} {
		w := patchRecipeMetadata(rs, slug)
		if w.Code != http.StatusNotFound || strings.TrimSpace(w.Body.String()) != `{"error":"Recipe not found"}` {
			t.Errorf("%q: response = %d %q, want safe 404", slug, w.Code, w.Body.String())
		}
	}
}

func newRecipeMetadataFixture(t *testing.T) (*store.FSRecipeStore, string) {
	t.Helper()
	dir := t.TempDir()
	rs, err := store.NewFSRecipeStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rs.Create("Audit Fixture", "Tests", "before", "value = 1"); err != nil {
		t.Fatal(err)
	}
	return rs, filepath.Join(dir, "recipes", "audit-fixture")
}

func patchRecipeMetadata(rs store.RecipeStore, slug string) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	Mount(router, nil, nil, rs, nil, "docker", httpx.Timeouts{}, 0)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPatch, "/api/recipes/"+slug, strings.NewReader(`{"name":"Renamed","category":"Other","description":"after"}`)))
	return w
}
