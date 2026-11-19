package api

import (
	"context"
	"encoding/json"
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

type stackInventoryEngine struct {
	engine.Engine
	containers []engine.ContainerInfo
	err        error
}

func (e stackInventoryEngine) ListContainers(context.Context) ([]engine.ContainerInfo, error) {
	return e.containers, e.err
}

func TestStackContainerInventoryDistinguishesEmptyFromUnavailable(t *testing.T) {
	const privateDiagnostic = "SYNTHETIC_PRIVATE_ENGINE_DIAGNOSTIC"
	other := engine.ContainerInfo{ID: "other-container", Stack: "other"}
	matching := engine.ContainerInfo{ID: "matching-container", Stack: "demo"}
	for _, tc := range []struct {
		name       string
		containers []engine.ContainerInfo
		err        error
		status     int
		wantIDs    []string
	}{
		{"confirmed empty", []engine.ContainerInfo{other}, nil, http.StatusOK, nil},
		{"confirmed match", []engine.ContainerInfo{other, matching}, nil, http.StatusOK, []string{matching.ID}},
		{"unavailable", nil, errors.New(privateDiagnostic), http.StatusInternalServerError, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := chi.NewRouter()
			ag := agent.NewLocalAgent(stackInventoryEngine{containers: tc.containers, err: tc.err}, nil)
			Mount(router, ag, nil, nil, nil, "docker", httpx.Timeouts{}, 0)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/stacks/demo/containers", nil))
			if w.Code != tc.status {
				t.Fatalf("inventory status = %d, want %d", w.Code, tc.status)
			}
			if tc.err != nil {
				var body map[string]string
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil ||
					body["error"] != "Stack container inventory unavailable; check the local container engine and retry" ||
					strings.Contains(w.Body.String(), privateDiagnostic) {
					t.Fatal("inventory failure did not return the safe unavailable diagnostic")
				}
				return
			}
			var got []engine.ContainerInfo
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got == nil {
				t.Fatalf("confirmed inventory must be an array, got %q (%v)", w.Body.String(), err)
			}
			if len(got) != len(tc.wantIDs) {
				t.Fatalf("inventory returned %d containers, want %d", len(got), len(tc.wantIDs))
			}
			for i, id := range tc.wantIDs {
				if got[i].ID != id {
					t.Errorf("container %d has ID %q, want %q", i, got[i].ID, id)
				}
			}
		})
	}
}
