package server

import (
	"context"
	"errors"
	"fmt"
	"github.com/umayr/brine/internal/remoteui"
	"github.com/umayr/brine/internal/webhooks"
	"testing"
)

type canceledObservationProvider struct {
	cancel   context.CancelFunc
	attempts []string
}

func (p *canceledObservationProvider) Acquire(_ context.Context, id string) (context.Context, remoteui.ReadSession, func() error, error) {
	p.attempts = append(p.attempts, id)
	p.cancel()
	return nil, nil, nil, errors.New("budget expired")
}
func TestRemoteSamplerCursorAdvancesOnlyAttemptedTargets(t *testing.T) {
	s, _, _, _ := dispatcherServer(t)
	r := webhookRegistry(t, s)
	for i := 0; i < 4; i++ {
		row := syntheticRecord()
		row.ID = fmt.Sprintf("target-%d", i)
		if e := r.Add(row, row); e != nil {
			t.Fatal(e)
		}
	}
	if e := s.configureWebhookMetadata(context.Background(), s.webhooks); e != nil {
		t.Fatal(e)
	}
	p := &canceledObservationProvider{}
	s.remoteReadLeases = p
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		p.cancel = cancel
		_ = s.sampleRemoteRuntime(ctx)
		cancel()
	}
	if len(p.attempts) != 2 || p.attempts[0] != "target-0" || p.attempts[1] != "target-1" {
		t.Fatal("deadline exhaustion skipped unattempted targets", p.attempts)
	}
}
func TestOneRemoteInspectionFailureDoesNotDiscardInventoryAndCapabilities(t *testing.T) {
	ctx := context.Background()
	s, _, _, _ := dispatcherServer(t)
	r := webhookRegistry(t, s)
	row := syntheticRecord()
	r.Add(row, row)
	s.remoteReadLeases = &remoteObservationProvider{session: &remoteObservationSession{state: "running", caps: []string{"monitor.session", "monitor.capabilities"}}}
	if e := s.configureWebhookMetadata(ctx, s.webhooks); e != nil {
		t.Fatal(e)
	}
	ep, _, e := s.webhooks.SaveEndpoint(ctx, webhooks.Endpoint{Name: "read-errors", URL: "https://hooks.example.test/errors", Enabled: true, Events: []string{"container.state_changed", "container.health_changed", "target.connectivity_changed", "target.capabilities_changed"}})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.sampleRemoteRuntime(ctx); e != nil {
		t.Fatal(e)
	}
	session := s.remoteReadLeases.(*remoteObservationProvider).session
	session.failInspect = true
	session.state = "exited"
	if e = s.sampleRemoteRuntime(ctx); e != nil {
		t.Fatal(e)
	}
	s.webhooks.ReconcileMetadata(ctx)
	ds, _ := s.webhooks.Deliveries(ctx, ep.ID)
	if len(ds) != 2 {
		t.Fatal("item failure invalidated valid inventory", len(ds))
	}
	for _, d := range ds {
		if d.Event.Type == "target.connectivity_changed" || d.Event.Type == "target.capabilities_changed" {
			t.Fatal("container failure invented target loss")
		}
	}
}
