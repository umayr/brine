package webhooks

import (
	"context"
	"testing"
)

func TestCorruptObservationDoesNotBecomeEmptyBaseline(t *testing.T) {
	ctx := context.Background()
	s := testService(t, 10)
	v := "running"
	base := Event{Type: "container.state_changed", Target: Resource{ID: "target"}}
	if e := s.Observe(ctx, "key", base, Observation{Availability: "reported", Value: &v}); e != nil {
		t.Fatal(e)
	}
	s.db.ExecContext(ctx, "UPDATE observations SET value='{}' WHERE id='key'")
	if e := s.Observe(ctx, "key", base, Observation{Availability: "unavailable"}); e == nil {
		t.Fatal("invalid prior state silently replaced")
	}
	if e := s.MarkUnobserved(ctx, "target", nil); e == nil {
		t.Fatal("invalid observation treated as empty inventory")
	}
}
