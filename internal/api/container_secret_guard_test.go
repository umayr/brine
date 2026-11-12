package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/engine"
)

type secretRouteEngine struct {
	engine.Engine
	info                    *engine.ContainerInfo
	starts, stops, restarts int
}

func (e *secretRouteEngine) InspectContainer(context.Context, string) (*engine.ContainerInfo, error) {
	return e.info, nil
}
func (e *secretRouteEngine) StartContainer(context.Context, string) error   { e.starts++; return nil }
func (e *secretRouteEngine) StopContainer(context.Context, string) error    { e.stops++; return nil }
func (e *secretRouteEngine) RestartContainer(context.Context, string) error { e.restarts++; return nil }
func TestSecretContainerRoutesDenyStartAndAllowStop(t *testing.T) {
	e := &secretRouteEngine{info: &engine.ContainerInfo{ID: "id", Stack: "demo", Labels: map[string]string{"dill.managed": "true", "dill.stack": "demo", "dill.service": "web", "dill.secret-enabled": "true"}}}
	a := agent.NewLocalAgent(e, nil)
	h := containerHandler{agent: a}
	request := func() *http.Request {
		r := httptest.NewRequest("POST", "/", nil)
		route := chi.NewRouteContext()
		route.URLParams.Add("id", "id")
		return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
	}
	w := httptest.NewRecorder()
	h.start(w, request())
	if w.Code == http.StatusOK {
		t.Fatal("secret start permitted")
	}
	w = httptest.NewRecorder()
	h.restart(w, request())
	if w.Code == http.StatusOK {
		t.Fatal("secret restart permitted")
	}
	w = httptest.NewRecorder()
	h.stop(w, request())
	if w.Code != http.StatusOK || e.stops != 1 || e.starts != 0 || e.restarts != 0 {
		t.Fatal("cleanup or gate failed")
	}
	delete(e.info.Labels, "dill.secret-enabled")
	a.SetSecretRecordLookup(func(string) (bool, error) { return true, nil })
	w = httptest.NewRecorder()
	h.start(w, request())
	if w.Code == http.StatusOK || e.starts != 0 {
		t.Fatal("persisted record ignored")
	}
}
