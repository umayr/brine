package webhooks

import (
	"context"
	"encoding/json"
	"testing"
)

func TestObservationJournalFailureCannotAdvanceBaseline(t *testing.T) {
	ctx := context.Background()
	s := testService(t, 10)
	a, b := "running", "exited"
	base := Event{Type: "container.state_changed", Target: Resource{ID: "target"}}
	if e := s.Observe(ctx, "key", base, Observation{Availability: "reported", Value: &a}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.db.ExecContext(ctx, `CREATE TRIGGER fail_observation BEFORE INSERT ON metadata_mutations WHEN NEW.kind='observation' BEGIN SELECT RAISE(ABORT,'synthetic fault');END`); e != nil {
		t.Fatal(e)
	}
	if e := s.Observe(ctx, "key", base, Observation{Availability: "reported", Value: &b}); e == nil {
		t.Fatal("journal failure hidden")
	}
	var payload []byte
	s.db.QueryRowContext(ctx, "SELECT value FROM observations WHERE id='key'").Scan(&payload)
	var v Observation
	if json.Unmarshal(payload, &v) != nil || v.Value == nil || *v.Value != "running" {
		t.Fatal("baseline advanced without durable event intent")
	}
}
func TestObservationSurvivesFullDeliveryQueue(t *testing.T) {
	ctx := context.Background()
	s := testService(t, 1)
	ep, _ := testEndpoint(t, s)
	ep.Events = []string{"container.state_changed", "operation.completed"}
	s.SaveEndpoint(ctx, ep)
	if e := s.Publish(ctx, testEvent("fill")); e != nil {
		t.Fatal(e)
	}
	a, b := "running", "exited"
	base := Event{Type: "container.state_changed", Target: Resource{ID: "target"}}
	s.Observe(ctx, "key", base, Observation{Availability: "reported", Value: &a})
	if e := s.Observe(ctx, "key", base, Observation{Availability: "reported", Value: &b}); e != nil {
		t.Fatal(e)
	}
	if e := s.ReconcileMetadata(ctx); e != nil {
		t.Fatal(e)
	}
	var n int
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM metadata_mutations WHERE kind='observation' AND state='committed'").Scan(&n)
	if n != 1 {
		t.Fatal("full queue lost observation")
	}
	s.db.ExecContext(ctx, "UPDATE deliveries SET status='delivered'")
	if e := s.ReconcileMetadata(ctx); e != nil {
		t.Fatal(e)
	}
	ds, _ := s.Deliveries(ctx, ep.ID)
	if len(ds) != 2 {
		t.Fatal("durable transition not retried after delivery capacity", len(ds))
	}
}
