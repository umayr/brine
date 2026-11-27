package webhooks

import (
	"context"
	"testing"
	"time"
)

func TestRetiredObservationExpiresWithoutLosingCommittedEvent(t *testing.T) {
	ctx := context.Background()
	s := testService(t, 10)
	now := s.now()
	s.now = func() time.Time { return now }
	v := "running"
	base := Event{Type: "container.state_changed", Target: Resource{ID: "target"}}
	if e := s.Observe(ctx, "retired", base, Observation{Availability: "reported", Value: &v}); e != nil {
		t.Fatal(e)
	}
	if e := s.MarkUnobserved(ctx, "target", nil); e != nil {
		t.Fatal(e)
	}
	now = now.Add(s.cfg.Retention + time.Hour)
	if e := s.MarkUnobserved(ctx, "target", nil); e != nil {
		t.Fatal(e)
	}
	if e := s.prune(ctx); e != nil {
		t.Fatal(e)
	}
	var states, intents int
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM observations").Scan(&states)
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM metadata_mutations WHERE kind='observation' AND state='committed'").Scan(&intents)
	if states != 0 || intents != 1 {
		t.Fatal("retired state pinned capacity or committed event lost", states, intents)
	}
}
