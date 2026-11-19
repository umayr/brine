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
)

type restartDiagnosticEngine struct {
	engine.Engine
	info     engine.ContainerInfo
	err      error
	restarts []string
}

func (e *restartDiagnosticEngine) InspectContainer(context.Context, string) (*engine.ContainerInfo, error) {
	return &e.info, nil
}

func (e *restartDiagnosticEngine) RestartContainer(_ context.Context, id string) error {
	e.restarts = append(e.restarts, id)
	return e.err
}

func TestContainerRestartDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		blocked bool
		status  int
		body    string
	}{
		{"success", nil, false, http.StatusOK, `{"status":"restarted"}`},
		{"engine error", errors.New("SYNTHETIC_PRIVATE_RESTART_ERROR"), false, http.StatusInternalServerError, `{"error":"Container restart failed; check the container state before retrying"}`},
		{"guard denial", nil, true, http.StatusInternalServerError, `{"error":"container cannot be started or restarted directly; use checked stack up"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng := &restartDiagnosticEngine{info: engine.ContainerInfo{ID: "synthetic"}, err: tc.err}
			if tc.blocked {
				eng.info.Labels = map[string]string{"dill.secret-enabled": "true"}
			}
			router := chi.NewRouter()
			Mount(router, agent.NewLocalAgent(eng, nil), nil, nil, nil, "docker", httpx.Timeouts{}, 0)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/containers/synthetic/restart", nil))
			if w.Code != tc.status || w.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("status/type = %d %q, want %d application/json", w.Code, w.Header().Get("Content-Type"), tc.status)
			}
			if w.Body.String() != tc.body+"\n" {
				t.Error("restart did not return the expected safe JSON response")
			}
			if tc.blocked {
				if len(eng.restarts) != 0 {
					t.Fatal("guard denial reached the restart method")
				}
			} else if len(eng.restarts) != 1 || eng.restarts[0] != "synthetic" {
				t.Fatalf("restart calls = %q, want one call for synthetic", eng.restarts)
			}
		})
	}
}
