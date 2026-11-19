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
	"github.com/umayr/brine/internal/target"
)

type stopDiagnosticEngine struct {
	engine.Engine
	stop func(string) error
}

func (e stopDiagnosticEngine) StopContainer(_ context.Context, id string) error {
	return e.stop(id)
}

func TestContainerStopDiagnostic(t *testing.T) {
	const sentinel = "SYNTHETIC_PRIVATE_STOP_ERROR"
	for _, tc := range []struct {
		name   string
		err    error
		status int
		body   string
	}{
		{"success", nil, http.StatusOK, `{"status":"stopped"}`},
		{"engine error", errors.New(sentinel), http.StatusInternalServerError, `{"error":"Container stop failed; check the container state before retrying"}`},
		{"guard denial", target.ErrNegotiationUnavailable, http.StatusInternalServerError, `{"error":"Alpha mutations unavailable: local target and ownership were not qualified"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			eng := stopDiagnosticEngine{stop: func(id string) error {
				calls++
				if id != "synthetic" {
					t.Fatalf("StopContainer(%q), want synthetic", id)
				}
				return tc.err
			}}
			r := chi.NewRouter()
			Mount(r, agent.NewLocalAgent(eng, nil), nil, nil, nil, "docker", httpx.Timeouts{}, 0)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/containers/synthetic/stop", nil))
			if calls != 1 || w.Code != tc.status || w.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("stop calls/status/type = %d/%d/%q, want 1/%d/application/json", calls, w.Code, w.Header().Get("Content-Type"), tc.status)
			}
			if strings.Contains(w.Body.String(), sentinel) {
				t.Fatal("stop response returned the raw engine diagnostic")
			}
			if w.Body.String() != tc.body+"\n" {
				t.Error("unexpected stop response body")
			}
		})
	}
}
