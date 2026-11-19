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

type stackListStore struct {
	store.StackStore
	stacks []*store.Stack
	err    error
}

func (s stackListStore) List() ([]*store.Stack, error) { return s.stacks, s.err }

func TestStackListDiagnostic(t *testing.T) {
	const sentinel = "SYNTHETIC_PRIVATE_STACK_LIST_ERROR"
	legacy := &store.Stack{Name: "demo"}
	for _, tc := range []struct {
		name   string
		store  stackListStore
		status int
		want   any
	}{
		{"store failure", stackListStore{err: errors.New(sentinel)}, http.StatusInternalServerError, map[string]string{"error": "Stack list unavailable; check local stack storage"}},
		{"nil list", stackListStore{}, http.StatusOK, []*store.Stack(nil)},
		{"empty list", stackListStore{stacks: []*store.Stack{}}, http.StatusOK, []*store.Stack{}},
		{"stack metadata", stackListStore{stacks: []*store.Stack{legacy}}, http.StatusOK, []*store.Stack{{Name: "demo", Entrypoint: "compose.pkl"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := chi.NewRouter()
			Mount(router, nil, tc.store, nil, nil, "docker", httpx.Timeouts{}, 0)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/stacks", nil))
			if w.Code != tc.status || w.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("status/type = %d %q, want %d application/json", w.Code, w.Header().Get("Content-Type"), tc.status)
			}
			if strings.Contains(w.Body.String(), sentinel) {
				t.Fatal("stack list returned the synthetic private store error")
			}
			want, err := json.Marshal(tc.want)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(w.Body.String()); got != string(want) {
				t.Errorf("body = %q, want %q", got, want)
			}
		})
	}
	if legacy.Entrypoint != "" {
		t.Fatal("listing mutated store metadata")
	}
}
