package webhooks

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestUnreadableMetadataDoesNotStarveRecoverableCommits(t *testing.T) {
	ctx := context.Background()
	s := testService(t, 10)
	ep, _ := testEndpoint(t, s)
	ep.Events = []string{"stack.updated"}
	s.SaveEndpoint(ctx, ep)
	now := s.now()
	head := ""
	for i := 0; i < 101; i++ {
		at := now.Add(time.Duration(i) * time.Millisecond)
		s.now = func() time.Time { return at }
		name, sid := fmt.Sprintf("m%03d", i), fmt.Sprintf("stack_%03d", i)
		id, e := s.BeginMetadataMutation(ctx, name, sid, "", "write", []Event{{Type: "stack.updated", Stack: Resource{ID: sid, Name: name}}})
		if e != nil {
			t.Fatal(e)
		}
		if i == 100 {
			head = id
		}
	}
	s.now = func() time.Time { return now.Add(time.Hour) }
	s.ConfigureMetadataObservation(func(_ context.Context, name string) (MetadataObservation, error) {
		if name != "m100" {
			return MetadataObservation{}, errors.New("unreadable")
		}
		return MetadataObservation{Exists: true, StackID: "stack_100", Head: head}, nil
	})
	for i := 0; i < 2; i++ {
		if e := s.ReconcileMetadata(ctx); e != nil {
			t.Fatal(e)
		}
	}
	ds, _ := s.Deliveries(ctx, ep.ID)
	if len(ds) != 1 || ds[0].Event.Stack.ID != "stack_100" {
		t.Fatal("older unreadable records starved authoritative recovery", ds)
	}
}
