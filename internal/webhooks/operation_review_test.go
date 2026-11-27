package webhooks

import (
	"context"
	"errors"
	"testing"
)

func TestRecoveryPreservesDurablyKnownOutcome(t *testing.T) {
	ctx := context.Background()
	s := testService(t, 10)
	tr, key := testTrigger(t, s)
	d := &testDispatcher{}
	r, e := acceptRequest(s, tr, key, `{"id":"known-before-crash"}`, d)
	if e != nil {
		t.Fatal(e)
	}
	s.claimIncoming(ctx)
	op, e := s.BeginOperation(ctx, Event{ID: r.ID, Stack: Resource{ID: tr.StackID}, Target: Resource{ID: tr.TargetID}, Action: tr.Action})
	if e != nil {
		t.Fatal(e)
	}
	op.Started(ctx)
	if e = op.Finish(ctx, "operation.completed", "success", "unknown", ""); e != nil {
		t.Fatal(e)
	}
	if e = s.recoverIncoming(ctx); e != nil {
		t.Fatal(e)
	}
	rows, _ := s.Requests(ctx, tr.ID)
	if rows[0].Status != "completed" || rows[0].Outcome != "success" || rows[0].OperationID != r.ID {
		t.Fatalf("known result discarded: %+v", rows[0])
	}
	s.executeOne(ctx, d)
	if d.calls != 0 {
		t.Fatal("known operation replayed")
	}
}
func TestAdmissionDenialHasDurableLifecycleEvent(t *testing.T) {
	ctx := context.Background()
	s := testService(t, 10)
	ep, _ := testEndpoint(t, s)
	ep.Events = []string{"operation.rejected"}
	s.SaveEndpoint(ctx, ep)
	tr, key := testTrigger(t, s)
	d := &testDispatcher{}
	if _, e := acceptRequest(s, tr, key, `{"id":"denied-after-acceptance"}`, d); e != nil {
		t.Fatal(e)
	}
	d.validate = func(Trigger) error { return errors.New("protected state unavailable") }
	if _, e := s.executeOne(ctx, d); e != nil {
		t.Fatal(e)
	}
	if e := s.reconcileOperations(ctx); e != nil {
		t.Fatal(e)
	}
	deliveries, e := s.Deliveries(ctx, ep.ID)
	if e != nil || len(deliveries) != 1 || deliveries[0].Event.Type != "operation.rejected" || deliveries[0].Event.Effects != "none" {
		t.Fatalf("missing admission denial: %+v %v", deliveries, e)
	}
}
