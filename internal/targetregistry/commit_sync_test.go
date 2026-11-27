package targetregistry

import (
	"context"
	"errors"
	"testing"
)

func TestVisibleHeadCannotAcknowledgeUnsyncedRegistryPredecessor(t *testing.T) {
	r := registry(t)
	observed, confirmed, syncs := 0, 0, 0
	fault := errors.New("directory sync unavailable")
	r.syncDirectory = func() error {
		syncs++
		if syncs >= 2 {
			return fault
		}
		return nil
	}
	r.SetCommitObserver(func(context.Context, string, []CommitChange) (CommitReceipt, error) {
		observed++
		return CommitReceipt{ID: "meta_pending", Done: func(e error) {
			if e == nil {
				confirmed++
			}
		}}, nil
	})
	row := record()
	if e := r.Add(row, row); !errors.Is(e, fault) {
		t.Fatal(e)
	}
	visible, e := r.Get(row.ID)
	if e != nil || visible != row {
		t.Fatal("fixture did not reproduce renamed but unconfirmed value", e)
	}
	next := row
	next.Revision++
	next.Host = "next.example"
	if e = r.Update(row, next, ReviewedUpdate{Previous: row, Next: next}); !errors.Is(e, fault) {
		t.Fatal(e)
	}
	if observed != 1 || confirmed != 0 {
		t.Fatal("unsynced predecessor became confirmed", observed, confirmed)
	}
	if _, e = r.CommitHead(context.Background()); !errors.Is(e, fault) {
		t.Fatal("recovery ignored sync failure", e)
	}
}
func TestRegistryJournalIdentitySurvivesOpenNotReinitialization(t *testing.T) {
	ctx := context.Background()
	r := registry(t)
	id, e := r.JournalIdentity(ctx)
	if e != nil {
		t.Fatal(e)
	}
	again, e := Open(r.dir)
	if e != nil {
		t.Fatal(e)
	}
	same, e := again.JournalIdentity(ctx)
	if e != nil || same != id {
		t.Fatal("same registry identity changed", e)
	}
	other := registry(t)
	different, e := other.JournalIdentity(ctx)
	if e != nil || different == id {
		t.Fatal("distinct registry reused pending mutation scope", e)
	}
}
