package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/engine"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/target"
)

type startDiagnosticEngine struct {
	engine.Engine
	info  engine.ContainerInfo
	start func(string) error
}

func (e *startDiagnosticEngine) InspectContainer(context.Context, string) (*engine.ContainerInfo, error) {
	return &e.info, nil
}

func (e *startDiagnosticEngine) StartContainer(_ context.Context, id string) error {
	return e.start(id)
}

func TestContainerStartDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		blocked bool
		status  int
		body    string
	}{
		{"success", nil, false, http.StatusOK, `{"status":"started"}`},
		{"engine error", errors.New("SYNTHETIC_PRIVATE_START_ERROR"), false, http.StatusInternalServerError, `{"error":"Container start failed; check the container state before retrying"}`},
		{"checked-up denial", nil, true, http.StatusInternalServerError, `{"error":"container cannot be started or restarted directly; use checked stack up"}`},
		{"target guard guidance", target.ErrNegotiationUnavailable, false, http.StatusInternalServerError, `{"error":"Alpha mutations unavailable: local target and ownership were not qualified"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			eng := &startDiagnosticEngine{info: engine.ContainerInfo{ID: "synthetic"}, start: func(id string) error {
				calls++
				if id != "synthetic" {
					t.Fatalf("StartContainer(%q), want synthetic", id)
				}
				return tc.err
			}}
			wantCalls := 1
			if tc.blocked {
				eng.info.Labels = map[string]string{"dill.secret-enabled": "true"}
				wantCalls = 0
			}
			r := chi.NewRouter()
			Mount(r, agent.NewLocalAgent(eng, nil), nil, nil, nil, "docker", httpx.Timeouts{}, 0)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/containers/synthetic/start", nil))
			if calls != wantCalls || w.Code != tc.status || w.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("calls/status/type = %d/%d/%q, want %d/%d/application/json", calls, w.Code, w.Header().Get("Content-Type"), wantCalls, tc.status)
			}
			if w.Body.String() != tc.body+"\n" {
				t.Error("start did not return the expected safe JSON response")
			}
		})
	}
}
