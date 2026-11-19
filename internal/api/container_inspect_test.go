package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/engine"
	"github.com/umayr/brine/internal/httpx"
)

func TestContainerInspectionDistinguishesAbsenceFromEngineFailure(t *testing.T) {
	const upstreamDiagnostic = "SYNTHETIC_PRIVATE_ENGINE_DIAGNOSTIC"
	for _, tc := range []struct {
		name           string
		upstreamStatus int
		wantStatus     int
		wantError      string
	}{
		{"not found", http.StatusNotFound, http.StatusNotFound, "Container not found"},
		{"engine failure", http.StatusInternalServerError, http.StatusBadGateway, "Container inspection unavailable; check the local container engine and retry"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodHead && r.URL.Path == "/_ping":
					w.Header().Set("API-Version", "1.44")
				case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/synthetic/json":
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.upstreamStatus)
					_, _ = io.WriteString(w, `{"message":"`+upstreamDiagnostic+`"}`)
				default:
					t.Errorf("unexpected daemon request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer daemon.Close()
			eng, err := engine.NewDockerEngineWithHost("tcp://" + strings.TrimPrefix(daemon.URL, "http://"))
			if err != nil {
				t.Fatal(err)
			}
			defer eng.Close()
			router := chi.NewRouter()
			Mount(router, agent.NewLocalAgent(eng, nil), nil, nil, nil, "docker", httpx.Timeouts{}, 0)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/containers/synthetic", nil))
			if w.Code != tc.wantStatus {
				t.Errorf("inspect status = %d, want %d", w.Code, tc.wantStatus)
			}
			var body map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["error"] != tc.wantError || strings.Contains(w.Body.String(), upstreamDiagnostic) {
				t.Error("inspect response did not return only the safe diagnostic")
			}
		})
	}
}
