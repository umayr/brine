package webhooks

import (
	"context"
	"errors"
	"testing"
)

func TestMetadataRecoveryRequiresReadableExactIdentity(t *testing.T) {
	ctx := context.Background()
	s := testService(t, 10)
	ep, _ := testEndpoint(t, s)
	ep.Events = []string{"stack.updated"}
	s.SaveEndpoint(ctx, ep)
	event := testEvent("unused")
	event.Type = "stack.updated"
	event.Stack.ID = "stack_example"
	id, e := s.BeginMetadataMutation(ctx, "example", "stack_example", "old_head", "write", []Event{event})
	if e != nil {
		t.Fatal(e)
	}
	readable := false
	observation := MetadataObservation{Exists: true, StackID: "stack_example", Head: id}
	s.ConfigureMetadataObservation(func(context.Context, string) (MetadataObservation, error) {
		if !readable {
			return MetadataObservation{}, errors.New("unreadable state")
		}
		return observation, nil
	})
	if e = s.ReconcileMetadata(ctx); e != nil {
		t.Fatal(e)
	}
	ds, _ := s.Deliveries(ctx, ep.ID)
	if len(ds) != 0 {
		t.Fatal("unreadable metadata became committed or empty")
	}
	readable = true
	observation.StackID = "another-stack"
	s.ReconcileMetadata(ctx)
	ds, _ = s.Deliveries(ctx, ep.ID)
	if len(ds) != 0 {
		t.Fatal("wrong incarnation confirmed mutation")
	}
	observation.StackID = "stack_example"
	for i := 0; i < 2; i++ {
		if e = s.ReconcileMetadata(ctx); e != nil {
			t.Fatal(e)
		}
	}
	ds, _ = s.Deliveries(ctx, ep.ID)
	if len(ds) != 1 || ds[0].Event.Reason != "reconciled_metadata_commit" {
		t.Fatal("exact commit marker not reconciled once", ds)
	}
}
func TestMetadataPredecessorClosesCallbackWindow(t *testing.T) {
	ctx := context.Background()
	s := testService(t, 10)
	event := testEvent("unused")
	event.Type = "stack.updated"
	event.Stack.ID = "stack_example"
	id, e := s.BeginMetadataMutation(ctx, "example", "stack_example", "", "write", []Event{event})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.BeginMetadataMutation(ctx, "example", "stack_example", "wrong_head", "write", []Event{event}); !errors.Is(e, ErrConflict) {
		t.Fatal("unconfirmed intent silently overwritten", e)
	}
	if _, e = s.BeginMetadataMutation(ctx, "example", "different_incarnation", id, "write", []Event{event}); !errors.Is(e, ErrConflict) {
		t.Fatal("cross-scope head confirmed prior intent", e)
	}
	if _, e = s.BeginMetadataMutation(ctx, "example", "stack_example", id, "write", []Event{event}); e != nil {
		t.Fatal("proven predecessor not acknowledged", e)
	}
}
func TestMetadataDeleteNeedsConfirmedAbsence(t *testing.T) {
	ctx := context.Background()
	s := testService(t, 10)
	ep, _ := testEndpoint(t, s)
	ep.Events = []string{"stack.deleted"}
	s.SaveEndpoint(ctx, ep)
	event := testEvent("unused")
	event.Type = "stack.deleted"
	event.Stack.ID = "stack_example"
	s.BeginMetadataMutation(ctx, "example", "stack_example", "old_head", "delete", []Event{event})
	absent := false
	s.ConfigureMetadataObservation(func(context.Context, string) (MetadataObservation, error) {
		if !absent {
			return MetadataObservation{}, errors.New("missing metadata in existing directory")
		}
		return MetadataObservation{Exists: false}, nil
	})
	s.ReconcileMetadata(ctx)
	ds, _ := s.Deliveries(ctx, ep.ID)
	if len(ds) != 0 {
		t.Fatal("unreadable treated as deleted")
	}
	absent = true
	s.ReconcileMetadata(ctx)
	ds, _ = s.Deliveries(ctx, ep.ID)
	if len(ds) != 1 {
		t.Fatal("confirmed deletion lost")
	}
}
