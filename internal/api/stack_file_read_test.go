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

type stackFileReadStore struct {
	store.StackStore
	read func(name, filename string) (string, error)
}

func (s stackFileReadStore) ReadFile(name, filename string) (string, error) {
	return s.read(name, filename)
}

func TestStackFileReadDiagnostic(t *testing.T) {
	const sentinel = "SYNTHETIC_PRIVATE_STACK_FILE_READ_ERROR"
	for _, tc := range []struct {
		name, content string
		err           error
		status        int
	}{
		{"nonempty file", "value = 1\n", nil, http.StatusOK},
		{"empty file", "", nil, http.StatusOK},
		{"read failure", "", errors.New(sentinel), http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := chi.NewRouter()
			st := stackFileReadStore{read: func(name, filename string) (string, error) {
				if name != "demo" || filename != "compose.pkl" {
					t.Fatalf("unexpected file lookup: %q/%q", name, filename)
				}
				return tc.content, tc.err
			}}
			Mount(router, nil, st, nil, nil, "docker", httpx.Timeouts{}, 0)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/stacks/demo/files/compose.pkl", nil))
			if w.Code != tc.status || w.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("unexpected status or content type: %d %q", w.Code, w.Header().Get("Content-Type"))
			}
			if strings.Contains(w.Body.String(), sentinel) {
				t.Fatal("stack file response exposed the synthetic private store error")
			}
			var body map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if tc.err != nil {
				if len(body) != 1 || body["error"] != "Stack file unavailable; check the file name and local workspace storage" {
					t.Error("stack file response did not match the expected safe diagnostic")
				}
				return
			}
			if content, ok := body["content"]; len(body) != 2 || body["filename"] != "compose.pkl" || !ok || content != tc.content {
				t.Errorf("unexpected successful file response: %+v", body)
			}
		})
	}
}
