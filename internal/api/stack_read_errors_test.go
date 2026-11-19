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
	r := chi.NewRouter()
	Mount(r, nil, st, nil, nil, "docker", httpx.Timeouts{}, 0)
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"missing", `{"error":"stack not found"}`, http.StatusNotFound},
		{"invalid.name", `{"error":"stack not found"}`, http.StatusNotFound},
		{"unavailable", `{"error":"stack metadata unavailable; check local stack storage"}`, http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/stacks/"+tc.name, nil))
			if w.Code != tc.status || strings.TrimSpace(w.Body.String()) != tc.body {
				t.Errorf("GET status = %d, want %d; response must contain only the expected safe diagnostic", w.Code, tc.status)
			}
		})
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
