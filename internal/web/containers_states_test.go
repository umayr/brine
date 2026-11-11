package web

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/engine"
)

type containerListEngine struct {
	engine.Engine
	containers []engine.ContainerInfo
	err        error
}

func (e containerListEngine) ListContainers(context.Context) ([]engine.ContainerInfo, error) {
	return e.containers, e.err
}

func TestContainerRowsHandlerStates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		engine containerListEngine
		want   []string
	}{
		{"empty", containerListEngine{}, []string{"No containers found.", "configured engine returned no containers"}},
		{"engine error", containerListEngine{err: errors.New("engine <offline>")}, []string{"Inventory is unavailable", "engine &lt;offline&gt;", "colspan=\"7\"", "Retry"}},
		{"populated", containerListEngine{containers: []engine.ContainerInfo{{ID: "123", Name: "example", State: "running"}}}, []string{"example", "running", "Stop"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{agent: agent.NewLocalAgent(tc.engine, nil)}
			w := httptest.NewRecorder()
			h.containerRows(w, httptest.NewRequest("GET", "/web/containers", nil))
			if w.Code != 200 {
				t.Fatalf("status %d", w.Code)
			}
			for _, want := range tc.want {
				if !strings.Contains(w.Body.String(), want) {
					t.Errorf("missing %q in %s", want, w.Body.String())
				}
			}
		})
	}
}
