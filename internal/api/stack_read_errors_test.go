package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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
