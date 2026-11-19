package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/engine"
	"github.com/umayr/brine/internal/httpx"
)

type imageRemoveEngine struct {
	engine.Engine
	remove func(string, bool) error
}

func (e imageRemoveEngine) RemoveImage(_ context.Context, id string, force bool) error {
	return e.remove(id, force)
}

func TestImageRemoveDiagnostic(t *testing.T) {
	const sentinel = "SYNTHETIC_PRIVATE_IMAGE_REMOVE_ERROR"
	for _, tc := range []struct {
		name, query string
		force       bool
		err         error
		status      int
		body        string
	}{
		{"default success", "", false, nil, http.StatusNoContent, ""},
		{"forced success", "?force=true", true, nil, http.StatusNoContent, ""},
		{"forced failure", "?force=true", true, errors.New(sentinel), http.StatusInternalServerError, `{"error":"Image removal failed; check whether the image is in use and whether the local container engine is available"}` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			eng := imageRemoveEngine{remove: func(id string, force bool) error {
				calls++
				if id != "synthetic" || force != tc.force {
					t.Fatalf("RemoveImage(%q, %v), want synthetic and force=%v", id, force, tc.force)
				}
				return tc.err
			}}
			r := chi.NewRouter()
			Mount(r, agent.NewLocalAgent(eng, nil), nil, nil, nil, "docker", httpx.Timeouts{}, 0)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/images/synthetic"+tc.query, nil))
			if calls != 1 || w.Code != tc.status {
				t.Fatalf("got %d engine calls and status %d, want one call and status %d", calls, w.Code, tc.status)
			}
			if strings.Contains(w.Body.String(), sentinel) {
				t.Fatal("raw engine error reached the API response")
			}
			if tc.err != nil && w.Header().Get("Content-Type") != "application/json" {
				t.Fatal("image removal error did not return JSON")
			}
			if w.Body.String() != tc.body {
				t.Error("unexpected image removal response body")
			}
		})
	}
}
