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

type containerListEngine struct {
	engine.Engine
	err error
}

func (e containerListEngine) ListContainers(context.Context) ([]engine.ContainerInfo, error) {
	if e.err != nil {
		return nil, e.err
	}
	return []engine.ContainerInfo{}, nil
}

func TestContainerListUnavailableDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		body   string
	}{
		{"confirmed empty", nil, http.StatusOK, `[]`},
		{"engine unavailable", errors.New("SYNTHETIC_PRIVATE_CONTAINER_ENGINE_DETAIL"), http.StatusInternalServerError, `{"error":"Container inventory unavailable; check the local container engine and retry"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := chi.NewRouter()
			ag := agent.NewLocalAgent(containerListEngine{err: tc.err}, nil)
			Mount(router, ag, nil, nil, nil, "docker", httpx.Timeouts{}, 0)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/containers", nil))
			if w.Code != tc.status {
				t.Errorf("container list status = %d, want %d", w.Code, tc.status)
			}
			if strings.TrimSpace(w.Body.String()) != tc.body {
				t.Error("container list response did not match the expected safe result")
			}
		})
	}
}
