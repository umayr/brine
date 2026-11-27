package webhooks

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestOperationJournalRecoversWithoutReplay(t *testing.T) {
	ctx := context.Background()
	s := testService(t, 10)
	ep, _ := testEndpoint(t, s)
	ep.Events = []string{"operation.accepted", "operation.started", "operation.outcome_unknown", "operation.completed", "operation.rejected"}
	s.SaveEndpoint(ctx, ep)
	op, e := s.BeginOperation(ctx, testEvent("op_interrupted"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.BeginOperation(ctx, testEvent("op_interrupted")); !errors.Is(e, ErrConflict) {
		t.Fatal("duplicate operation admitted")
	}
	if e = op.Started(ctx); e != nil {
		t.Fatal(e)
	}
	cfg := s.cfg
	cfg.EncryptionKey = bytes.Repeat([]byte{1}, 32)
	s.Close()
	reopened, e := OpenExisting(cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	if e = reopened.reconcileOperations(ctx); e != nil {
		t.Fatal(e)
	}
	if e = reopened.reconcileOperations(ctx); e != nil {
		t.Fatal(e)
	}
	ds, e := reopened.Deliveries(ctx, ep.ID)
	if e != nil || len(ds) != 3 {
		t.Fatalf("lost or duplicated transitions: %+v %v", ds, e)
	}
	found := false
	for _, d := range ds {
		if d.Event.Type == "operation.outcome_unknown" {
			found = d.Event.Outcome == "unknown" && d.Event.Effects == "unknown"
		}
	}
	if !found {
		t.Fatal("interrupted execution became a success")
	}
}
func TestOutcomePersistsWhenDeliveryQueueFull(t *testing.T) {
	ctx := context.Background()
	s := testService(t, 1)
	ep, _ := testEndpoint(t, s)
	ep.Events = []string{"operation.accepted", "operation.started", "operation.completed"}
	s.SaveEndpoint(ctx, ep)
	op, e := s.BeginOperation(ctx, testEvent("op_queue_full"))
	if e != nil {
		t.Fatal(e)
	}
	op.Started(ctx)
	if e = op.Finish(ctx, "operation.completed", "success", "unknown", ""); e != nil {
		t.Fatal(e)
	}
	if e = s.reconcileOperations(ctx); e != nil {
		t.Fatal(e)
	}
	ds, _ := s.Deliveries(ctx, ep.ID)
	if len(ds) != 1 {
		t.Fatal("capacity bypass")
	}
	for i := 0; i < 3; i++ {
		s.db.Exec("UPDATE deliveries SET status='delivered'")
		if e = s.reconcileOperations(ctx); e != nil {
			t.Fatal(e)
		}
	}
	ds, _ = s.Deliveries(ctx, ep.ID)
	if len(ds) != 3 {
		t.Fatal("terminal event lost to backpressure")
	}
	for _, d := range ds {
		if d.Event.Type == "operation.completed" && d.Event.Outcome != "success" {
			t.Fatal("notification failure changed outcome")
		}
	}
}
