package server

import (
	"context"
	"errors"
	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/remoteui"
	"github.com/umayr/brine/internal/webhooks"
	"strings"
	"testing"
)

type remoteObservationSession struct {
	testSession
	state       string
	caps        []string
	binding     string
	active      bool
	failInspect bool
}

func (s *remoteObservationSession) Inventory(ctx context.Context, _ int) ([]agentclient.InventoryItem, error) {
	if !s.active || ctx.Err() != nil {
		return nil, errors.New("no lease")
	}
	return []agentclient.InventoryItem{{ID: strings.Repeat("a", 64), Name: "remote-container", Stack: "example", Service: "web", State: s.state}}, nil
}
func (s *remoteObservationSession) Inspect(ctx context.Context, id string) (agentclient.InspectResult, error) {
	if !s.active || ctx.Err() != nil {
		return agentclient.InspectResult{}, errors.New("no lease")
	}
	if s.failInspect {
		return agentclient.InspectResult{}, errors.New("container disappeared")
	}
	r := agentclient.InspectResult{ContainerID: id, Stack: "example", Service: "web"}
	r.Inspect.ID = id
	r.Inspect.State = s.state
	health := "healthy"
	r.Inspect.Health.Status = &health
	return r, nil
}
func (s *remoteObservationSession) Capabilities(context.Context) ([]string, error) {
	return append([]string{}, s.caps...), nil
}
func (s *remoteObservationSession) ObservationBinding() string { return s.binding }

type remoteObservationProvider struct {
	session  *remoteObservationSession
	fail     bool
	released int
}

func (p *remoteObservationProvider) Acquire(ctx context.Context, _ string) (context.Context, remoteui.ReadSession, func() error, error) {
	if p.fail {
		return nil, nil, nil, errors.New("unavailable")
	}
	lease, cancel := context.WithCancel(ctx)
	p.session.active = true
	return lease, p.session, func() error { p.session.active = false; cancel(); p.released++; return nil }, nil
}
func TestRemoteRuntimeUsesVerifiedLeaseAndReportedCapabilities(t *testing.T) {
	ctx := context.Background()
	s, _, _, _ := dispatcherServer(t)
	r := webhookRegistry(t, s)
	row := syntheticRecord()
	if e := r.Add(row, row); e != nil {
		t.Fatal(e)
	}
	provider := &remoteObservationProvider{session: &remoteObservationSession{state: "running", caps: []string{"status", "health"}}}
	s.remoteReadLeases = provider
	if e := s.configureWebhookMetadata(ctx, s.webhooks); e != nil {
		t.Fatal(e)
	}
	ep, _, e := s.webhooks.SaveEndpoint(ctx, webhooks.Endpoint{Name: "remote", URL: "https://hooks.example.test/remote", Enabled: true, Events: []string{"target.capabilities_changed", "container.state_changed", "stack.health_changed"}})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.sampleRemoteRuntime(ctx); e != nil {
		t.Fatal(e)
	}
	provider.session.state = "exited"
	provider.session.caps = append(provider.session.caps, "snapshot-manifest")
	if e = s.sampleRemoteRuntime(ctx); e != nil {
		t.Fatal(e)
	}
	if provider.released != 2 || provider.session.active {
		t.Fatal("observation escaped lease lifetime")
	}
	if e = s.webhooks.ReconcileMetadata(ctx); e != nil {
		t.Fatal(e)
	}
	ds, _ := s.webhooks.Deliveries(ctx, ep.ID)
	if len(ds) != 3 {
		t.Fatal("lost remote observations", len(ds))
	}
	for _, d := range ds {
		if d.Event.Outcome != "" || d.Event.Effects != "" {
			t.Fatal("read observation inferred execution")
		}
		if d.Event.Stack.ID != "" && d.Event.Stack.Kind != "runtime-stack" {
			t.Fatal("unowned runtime stack claimed workspace source")
		}
	}
	provider.fail = true
	if e = s.sampleRemoteRuntime(ctx); e == nil {
		t.Fatal("unavailable target became successful empty inventory")
	}
}
