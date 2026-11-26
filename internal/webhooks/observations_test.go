package webhooks

import (
	"context"
	"testing"
)

func TestObservationBaselineAndDurableTransitions(t *testing.T) {
	ctx := context.Background()
	s := testService(t, 10)
	ep, _ := testEndpoint(t, s)
	ep.Events = []string{"container.state_changed"}
	s.SaveEndpoint(ctx, ep)
	base := Event{Type: "container.state_changed", Target: Resource{ID: "target"}, Container: &Resource{ID: "container"}}
	running := "running"
	exited := "exited"
	for _, v := range []Observation{{Availability: "reported", Value: &running}, {Availability: "reported", Value: &running}, {Availability: "reported", Value: &exited}, {Availability: "unavailable"}} {
		if e := s.Observe(ctx, "container-state", base, v); e != nil {
			t.Fatal(e)
		}
	}
	for i := 0; i < 2; i++ {
		if e := s.ReconcileMetadata(ctx); e != nil {
			t.Fatal(e)
		}
	}
	ds, _ := s.Deliveries(ctx, ep.ID)
	if len(ds) != 2 {
		t.Fatal("baseline, replay or lost transition", len(ds))
	}
	for _, d := range ds {
		if d.Event.Observation == nil || d.Event.Outcome != "" || d.Event.Effects != "" {
			t.Fatal("observation invented execution outcome")
		}
	}
}
func TestMissingObservationsInvalidateOnceWithoutInventingRemoval(t *testing.T) {
	ctx := context.Background()
	s := testService(t, 10)
	v := "running"
	base := Event{Type: "container.state_changed", Target: Resource{ID: "target"}, Container: &Resource{ID: "container"}}
	s.Observe(ctx, "state", base, Observation{Availability: "reported", Value: &v})
	for i := 0; i < 2; i++ {
		if e := s.MarkUnobserved(ctx, "target", nil); e != nil {
			t.Fatal(e)
		}
	}
	var n int
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM metadata_mutations WHERE kind='observation'").Scan(&n)
	if n != 1 {
		t.Fatal("missing readings replayed or invented removal", n)
	}
}
func TestSamplingJoinedByClose(t *testing.T) {
	s := testService(t, 10)
	started := make(chan struct{})
	ended := make(chan struct{})
	s.ConfigureSampling(func(ctx context.Context) error { close(started); <-ctx.Done(); close(ended); return ctx.Err() })
	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background()) }()
	<-started
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	<-ended
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}
