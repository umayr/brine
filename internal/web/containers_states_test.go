package web

import (
	"context"
	"errors"
	"net/http"
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

type containerActionEngine struct {
	engine.Engine
	actionErr, inspectErr error
	container             *engine.ContainerInfo
}

func (e containerActionEngine) StartContainer(context.Context, string) error   { return e.actionErr }
func (e containerActionEngine) StopContainer(context.Context, string) error    { return e.actionErr }
func (e containerActionEngine) RestartContainer(context.Context, string) error { return e.actionErr }
func (e containerActionEngine) InspectContainer(context.Context, string) (*engine.ContainerInfo, error) {
	return e.container, e.inspectErr
}

func TestContainerActionFailuresAreUnconfirmedAndRedacted(t *testing.T) {
	private := errors.New("private engine detail")
	for _, inspectFailure := range []bool{false, true} {
		eng := containerActionEngine{actionErr: private}
		if inspectFailure {
			eng = containerActionEngine{inspectErr: private}
		}
		h := &Handler{agent: agent.NewLocalAgent(eng, nil)}
		for _, handler := range []http.HandlerFunc{h.containerStart, h.containerStop, h.containerRestart} {
			w := httptest.NewRecorder()
			handler(w, httptest.NewRequest(http.MethodPost, "/", nil))
			if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "could not be confirmed") || strings.Contains(w.Body.String(), private.Error()) {
				t.Fatalf("failure %t: status %d, body %q", inspectFailure, w.Code, w.Body.String())
			}
		}
	}
}

func TestContainerActionRendersRequestedView(t *testing.T) {
	eng := containerActionEngine{container: &engine.ContainerInfo{ID: "abc", Name: "example", State: "running"}}
	h := &Handler{agent: agent.NewLocalAgent(eng, nil)}
	for _, tc := range []struct{ query, want, unwanted string }{
		{"?view=state", "running</span>", "<tr"},
		{"", "<tr", "container-state-badge"},
	} {
		w := httptest.NewRecorder()
		h.containerStart(w, httptest.NewRequest(http.MethodPost, "/"+tc.query, nil))
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, tc.want) || strings.Contains(body, tc.unwanted) {
			t.Fatalf("view %q: status %d, body %q", tc.query, w.Code, body)
		}
	}
}

func TestContainerRowsHandlerStates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		engine containerListEngine
		want   []string
	}{
		{"empty", containerListEngine{}, []string{"No containers found.", "configured engine returned no containers"}},
		{"engine error", containerListEngine{err: errors.New("engine <offline>")}, []string{"Inventory is unavailable", "engine &lt;offline&gt;", "colspan=\"7\"", "Retry", "hx-sync=\"#containers-body:replace\""}},
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
