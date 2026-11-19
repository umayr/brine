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

type imageInventoryEngine struct {
	engine.Engine
	err error
}

func (e imageInventoryEngine) ListImages(context.Context) ([]engine.ImageInfo, error) {
	if e.err != nil {
		return nil, e.err
	}
	return []engine.ImageInfo{}, nil
}

func TestImageInventoryUnavailableDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		body   string
	}{
		{"confirmed empty", nil, http.StatusOK, `[]`},
		{"engine unavailable", errors.New("SYNTHETIC_PRIVATE_IMAGE_ENGINE_DETAIL"), http.StatusInternalServerError, `{"error":"Image inventory unavailable; check the local container engine and retry"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := chi.NewRouter()
			ag := agent.NewLocalAgent(imageInventoryEngine{err: tc.err}, nil)
			Mount(router, ag, nil, nil, nil, "docker", httpx.Timeouts{}, 0)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/images", nil))
			if w.Code != tc.status {
				t.Errorf("image inventory status = %d, want %d", w.Code, tc.status)
			}
			if strings.TrimSpace(w.Body.String()) != tc.body {
				t.Error("image inventory response did not match the expected safe result")
			}
		})
	}
}
