package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/store"
)

type brokenStackStore struct {
	store.StackStore
	updateErr error
}

func (s brokenStackStore) Get(string) (*store.Stack, error) { return &store.Stack{Name: "demo"}, nil }
func (s brokenStackStore) ReadPkl(string) (string, error) {
	return "", errors.New("private diagnostic")
}
func (s brokenStackStore) Update(string, string) (*store.Stack, error) { return nil, s.updateErr }

func TestStackReadDistinguishesMissingFromUnavailableMetadata(t *testing.T) {
	st, err := store.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stack, err := st.Create("unavailable", "// synthetic\n")
	if err != nil {
		t.Fatal(err)
	}
	// An unusable stored Git folder produces a private diagnostic in the store.
	meta := []byte(`{"git":{"folder":"/SYNTHETIC_PRIVATE_WORKSPACE"}}`)
	if err := os.WriteFile(filepath.Join(filepath.Dir(stack.PklPath), "meta.json"), meta, 0600); err != nil {
		t.Fatal(err)
	}
	const token = "synthetic-api-token"
	a, err := auth.New(auth.Config{User: "test", Password: "synthetic-password", APIToken: token})
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Use(a.Middleware)
	Mount(r, nil, st, nil, nil, "docker", httpx.Timeouts{}, 0)
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"missing", `{"error":"stack not found"}`, http.StatusNotFound},
		{"invalid.name", `{"error":"stack not found"}`, http.StatusNotFound},
		{"unavailable", `{"error":"stack metadata unavailable; check local stack storage"}`, http.StatusInternalServerError},
	} {
		for _, route := range []struct{ method, suffix string }{
			{http.MethodGet, ""},
			{http.MethodPost, "/up"},
			{http.MethodPost, "/down"},
			{http.MethodGet, "/plan"},
			{http.MethodGet, "/validate"},
		} {
			t.Run(tc.name+route.suffix, func(t *testing.T) {
				path := "/api/stacks/" + tc.name + route.suffix
				req := httptest.NewRequest(route.method, path, nil)
				req.Header.Set("Accept", "application/json")
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				if w.Code != http.StatusUnauthorized {
					t.Fatalf("unauthenticated status = %d, want 401", w.Code)
				}
				req = httptest.NewRequest(route.method, path, nil)
				req.Header.Set("Authorization", "Bearer "+token)
				w = httptest.NewRecorder()
				r.ServeHTTP(w, req)
				if w.Code != tc.status || strings.TrimSpace(w.Body.String()) != tc.body {
					t.Errorf("status = %d, want %d; response must contain only the expected safe diagnostic", w.Code, tc.status)
				}
			})
		}
	}
}

func TestStackReadAndUpdateErrors(t *testing.T) {
	for _, tc := range []struct {
		method, body string
		err          error
		status       int
	}{
		{"GET", "", nil, http.StatusInternalServerError},
		{"PUT", `{"pkl_content":"x"}`, fmt.Errorf("metadata: %w", os.ErrNotExist), http.StatusNotFound},
		{"PUT", `{"pkl_content":"x"}`, store.ErrReadOnly, http.StatusForbidden},
		{"PUT", `{"pkl_content":"x"}`, errors.New("private diagnostic"), http.StatusInternalServerError},
	} {
		r := chi.NewRouter()
		Mount(r, nil, brokenStackStore{updateErr: tc.err}, nil, nil, "docker", httpx.Timeouts{}, time.Second)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(tc.method, "/api/stacks/demo", strings.NewReader(tc.body)))
		if w.Code != tc.status || strings.Contains(w.Body.String(), "private diagnostic") {
			t.Fatalf("%s: %d %s", tc.method, w.Code, w.Body.String())
		}
	}
}
