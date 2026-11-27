package webhooks

import (
	"context"
	"testing"
)

func TestMetadataRecoveryPreservesRestoreReasonAndReclaimsTerminalJournal(t *testing.T) {
	ctx := context.Background()
	s := testService(t, 10)
	ep, _ := testEndpoint(t, s)
	ep.Events = []string{"source.restored"}
	s.SaveEndpoint(ctx, ep)
	event := Event{Type: "source.restored", Stack: Resource{ID: "stack_example", Name: "example"}, Reason: "reviewed_source_only"}
	id, e := s.BeginMetadataMutation(ctx, "example", "stack_example", "", "restore", []Event{event})
	if e != nil {
		t.Fatal(e)
	}
	s.ConfigureMetadataObservation(func(context.Context, string) (MetadataObservation, error) {
		return MetadataObservation{Exists: true, StackID: "stack_example", Head: id}, nil
	})
	if e = s.ReconcileMetadata(ctx); e != nil {
		t.Fatal(e)
	}
	ds, _ := s.Deliveries(ctx, ep.ID)
	if len(ds) != 1 || ds[0].Event.Reason != "reviewed_source_only" || !ds[0].Event.Reconciled {
		t.Fatal("recovery overwrote source-only review evidence", ds)
	}
	if _, e = s.BeginMetadataMutation(ctx, "example", "stack_example", id, "write", []Event{event}); e != nil {
		t.Fatal(e)
	}
	var count int
	if e = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM metadata_mutations").Scan(&count); e != nil || count != 1 {
		t.Fatal("published journal rows retained against active mutation capacity", count, e)
	}
	ds, _ = s.Deliveries(ctx, ep.ID)
	if len(ds) != 1 {
		t.Fatal("journal reclamation removed immutable delivery evidence")
	}
}
