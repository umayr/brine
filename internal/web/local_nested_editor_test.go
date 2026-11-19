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
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/store"
)

// Opt in with a local Dill, Pkl, and matching schema; never fetches dependencies.
func TestLocalNestedSavedValidateRealDill(t *testing.T) {
	binary, pkl, schema := os.Getenv("BRINE_TEST_DILL"), os.Getenv("BRINE_TEST_PKL"), os.Getenv("BRINE_TEST_DILL_SCHEMA")
	if binary == "" || pkl == "" || schema == "" {
		t.Skip("set BRINE_TEST_DILL, BRINE_TEST_PKL and BRINE_TEST_DILL_SCHEMA")
	}
	root := t.TempDir()
	st, err := store.NewFSStore(root)
	if err != nil {
		t.Fatal(err)
	}
	source := `amends "dill.pkl"
import "modules/settings.pkl"
config {
 name = "test"
 engine = "docker"
 services { ["web"] { image = settings.image } }
}
`
	if _, err = st.Create("test", source); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "stacks", "test")
	data, err := os.ReadFile(schema)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "dill.pkl"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(dir, "modules"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "modules", "settings.pkl"), []byte("image = \"old\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := dill.New(binary)
	runner.PklBinary = pkl
	runner.Engine = "docker"
	runner.Socket = filepath.Join(root, "absent.sock")
	runner.ResolveStack = func(string) (string, error) { return "test", nil }
	h := &Handler{store: st, agent: agent.NewLocalAgent(nil, runner)}
	request := func(fn func(http.ResponseWriter, *http.Request), vals url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/", strings.NewReader(vals.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		route := chi.NewRouteContext()
		route.URLParams.Add("name", "test")
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
		w := httptest.NewRecorder()
		fn(w, r)
		return w
	}
	w := request(h.stackUpdate, url.Values{"active_file": {"modules/settings.pkl"}, "pkl_content": {"image = \"nginx:alpine\"\n"}})
	if w.Header().Get("HX-Redirect") == "" {
		t.Fatalf("save: %s", w.Body.String())
	}
	w = request(h.stackValidate, url.Values{})
	if !strings.Contains(w.Body.String(), "Saved entrypoint validated") {
		t.Fatalf("saved validate: %s", w.Body.String())
	}
	w = request(h.stackUpdate, url.Values{"active_file": {"modules/settings.pkl"}, "pkl_content": {"image = 42\n"}})
	if w.Header().Get("HX-Redirect") == "" {
		t.Fatalf("save invalid: %s", w.Body.String())
	}
	w = request(h.stackValidate, url.Values{})
	if !strings.Contains(w.Body.String(), "Saved entrypoint validation failed") {
		t.Fatalf("invalid saved validate: %s", w.Body.String())
	}
}
