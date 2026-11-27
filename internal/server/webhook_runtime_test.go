package server

import (
	"context"
	"errors"
	"testing"

	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/engine"
	"github.com/umayr/brine/internal/webhooks"
)

type observationEngine struct {
	engine.Engine
	state, health string
	failed        bool
}

func (e *observationEngine) ListContainers(context.Context) ([]engine.ContainerInfo, error) {
	if e.failed {
		return nil, errors.New("unavailable")
	}
	return []engine.ContainerInfo{{ID: "synthetic", Name: "web", Stack: "example", State: e.state, Labels: map[string]string{"dill.managed": "true", "dill.stack": "example"}}}, nil
}
func (e *observationEngine) InspectContainer(context.Context, string) (*engine.ContainerInfo, error) {
	yes := true
	h := e.health
	return &engine.ContainerInfo{ID: "synthetic", State: e.state, HealthStatus: &h, HealthConfigured: &yes}, nil
}
func TestRuntimeSamplingPreservesReportedStateAndUnavailable(t *testing.T) {
	ctx := context.Background()
	s, _, _, _ := dispatcherServer(t)
	runtime := &observationEngine{state: "running", health: "healthy"}
	s.agent = agent.NewLocalAgent(runtime, s.agent.Dill())
	ep, _, e := s.webhooks.SaveEndpoint(ctx, webhooks.Endpoint{Name: "runtime", URL: "https://hooks.example.test/runtime", Enabled: true, Events: []string{"container.state_changed", "container.health_changed", "stack.health_changed", "target.connectivity_changed"}})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.sampleLocalRuntime(ctx); e != nil {
		t.Fatal(e)
	}
	s.webhooks.ReconcileMetadata(ctx)
	ds, _ := s.webhooks.Deliveries(ctx, ep.ID)
	if len(ds) != 0 {
		t.Fatal("invented initial transition")
	}
	runtime.state = "exited"
	if e = s.sampleLocalRuntime(ctx); e != nil {
		t.Fatal(e)
	}
	runtime.failed = true
	if e = s.sampleLocalRuntime(ctx); e == nil {
		t.Fatal("unavailable inventory accepted as empty success")
	}
	if e = s.webhooks.ReconcileMetadata(ctx); e != nil {
		t.Fatal(e)
	}
	ds, _ = s.webhooks.Deliveries(ctx, ep.ID)
	if len(ds) != 5 {
		t.Fatal("lost/coalesced runtime observations", len(ds))
	}
	reportedExit := false
	for _, d := range ds {
		if d.Event.Outcome != "" || d.Event.Effects != "" {
			t.Fatal("read observation implied execution")
		}
		if d.Event.Type == "container.state_changed" && d.Event.Observation.Current.Value != nil && *d.Event.Observation.Current.Value == "exited" {
			reportedExit = true
		}
	}
	if !reportedExit {
		t.Fatal("raw reported state lost")
	}
}
func TestAggregateHealthNeverTurnsMissingOrUncheckedIntoHealthy(t *testing.T) {
	running := []engine.ContainerInfo{{State: "running"}}
	for _, h := range []webhooks.Observation{unavailable(), {Availability: "not_configured"}, reported(""), reported("future-status")} {
		v := aggregateHealth(running, []webhooks.Observation{h})
		if v.Value != nil && *v.Value == "healthy" {
			t.Fatal("unknown became healthy")
		}
	}
	v := aggregateHealth(running, []webhooks.Observation{reported("healthy")})
	if v.Value == nil || *v.Value != "healthy" {
		t.Fatal(v)
	}
	two := []engine.ContainerInfo{{State: "running"}, {State: "running"}}
	for _, hs := range [][]webhooks.Observation{{reported("unhealthy"), unavailable()}, {unavailable(), reported("unhealthy")}} {
		v = aggregateHealth(two, hs)
		if v.Availability != "unavailable" {
			t.Fatal("aggregate depends on iteration order", v)
		}
	}
}
