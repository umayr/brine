package server

import (
	"context"
	"errors"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/webhooks"
	"testing"
)

type membershipStore struct {
	store.StackStore
	store.ManualDeploymentStore
	failed, remote bool
}

func (s *membershipStore) List() ([]*store.Stack, error) {
	if s.failed {
		return nil, errors.New("unavailable metadata")
	}
	rows, e := s.StackStore.List()
	if s.remote {
		for i, r := range rows {
			copy := *r
			copy.RemoteGit = &store.RemoteGitPolicy{Enabled: true}
			rows[i] = &copy
		}
	}
	return rows, e
}
func TestUnavailableMembershipInvalidatesHealthAndRemoteOwnershipDoesNotBecomeLocal(t *testing.T) {
	ctx := context.Background()
	s, st, _, _ := dispatcherServer(t)
	runtime := &observationEngine{state: "running", health: "healthy"}
	s.agent = agent.NewLocalAgent(runtime, s.agent.Dill())
	membership := &membershipStore{StackStore: st, ManualDeploymentStore: st}
	s.store = membership
	ep, _, e := s.webhooks.SaveEndpoint(ctx, webhooks.Endpoint{Name: "health", URL: "https://hooks.example.test/health", Enabled: true, Events: []string{"stack.health_changed"}})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.sampleLocalRuntime(ctx); e != nil {
		t.Fatal(e)
	}
	membership.failed = true
	if e = s.sampleLocalRuntime(ctx); e == nil {
		t.Fatal("failed membership lookup treated as empty")
	}
	membership.failed = false
	membership.remote = true
	if e = s.sampleLocalRuntime(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.webhooks.ReconcileMetadata(ctx); e != nil {
		t.Fatal(e)
	}
	ds, _ := s.webhooks.Deliveries(ctx, ep.ID)
	if len(ds) != 1 || ds[0].Event.Observation.Current.Availability != "unavailable" {
		t.Fatal("unknown membership or remote ownership became local health", ds)
	}
}
