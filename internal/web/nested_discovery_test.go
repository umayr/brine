package web

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/store"
)

type failingNestedDiscovery struct{ *store.FSStore }

func (f failingNestedDiscovery) LocalEditorFiles(string) ([]string, error) {
	return nil, errors.New("private error")
}

func TestNestedDiscoveryFailureKeepsRootEditor(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewFSStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Create("test", "root content"); err != nil {
		t.Fatal(err)
	}
	recipes, err := store.NewFSRecipeStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{store: failingNestedDiscovery{st}, recipes: recipes}
	r := httptest.NewRequest("GET", "/", nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("name", "test")
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
	w := httptest.NewRecorder()
	h.stackEditor(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Nested module list unavailable") || !strings.Contains(w.Body.String(), "root content") || strings.Contains(w.Body.String(), "private error") {
		t.Fatalf("editor: %d %s", w.Code, w.Body.String())
	}
}
