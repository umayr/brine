package targetregistry

import (
	"context"
	"errors"
	"testing"
)

func TestRegistryCommitObserverFollowsReviewedCAS(t *testing.T) {
	r := registry(t)
	ctx := context.Background()
	calls, done := 0, 0
	r.SetCommitObserver(func(_ context.Context, head string, c []CommitChange) (CommitReceipt, error) {
		calls++
		if len(c) != 1 {
			t.Fatal("unexpected mutation batch")
		}
		if calls == 1 && (head != "" || c[0].Before != nil || c[0].After.ID != "server") {
			t.Fatal("wrong enrollment change")
		}
		if calls == 2 && (head != "meta_one" || c[0].Before.Revision != 1 || c[0].After.Revision != 2) {
			t.Fatal("wrong reviewed update")
		}
		return CommitReceipt{ID: "meta_one", Done: func(e error) {
			if e != nil {
				t.Error(e)
			}
			done++
		}}, nil
	})
	row := record()
	if e := r.Add(row, Record{}); !errors.Is(e, ErrUnconfirmed) {
		t.Fatal(e)
	}
	if calls != 0 {
		t.Fatal("unapproved intent journaled")
	}
	if e := r.Add(row, row); e != nil {
		t.Fatal(e)
	}
	head, e := r.CommitHead(ctx)
	if e != nil || head != "meta_one" {
		t.Fatal(head, e)
	}
	next := row
	next.Revision++
	next.Host = "changed.example"
	if e = r.Update(row, next, ReviewedUpdate{Previous: row, Next: next}); e != nil {
		t.Fatal(e)
	}
	if calls != 2 || done != 2 {
		t.Fatal(calls, done)
	}
}
func TestRegistryObserverAdmissionPrecedesPublication(t *testing.T) {
	r := registry(t)
	row := record()
	denied := errors.New("journal capacity")
	r.SetCommitObserver(func(context.Context, string, []CommitChange) (CommitReceipt, error) { return CommitReceipt{}, denied })
	if e := r.Add(row, row); !errors.Is(e, denied) {
		t.Fatal(e)
	}
	if _, e := r.Get(row.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal("unrecorded target published", e)
	}
}
func TestRegistryRemovalCarriesOnlyConfirmedBeforeState(t *testing.T) {
	r := registry(t)
	row := syntheticHistoryTarget("paired")
	if e := r.Add(row, row); e != nil {
		t.Fatal(e)
	}
	called := false
	r.SetCommitObserver(func(_ context.Context, _ string, c []CommitChange) (CommitReceipt, error) {
		called = true
		if len(c) != 1 || c[0].After != nil || c[0].Before.ID != "paired" {
			t.Fatal("wrong removed target")
		}
		return CommitReceipt{ID: "meta_removed"}, nil
	})
	if e := r.ForgetHTTPS(context.Background(), "paired"); e != nil {
		t.Fatal(e)
	}
	head, e := r.CommitHead(context.Background())
	if !called || e != nil || head != "meta_removed" {
		t.Fatal(head, e)
	}
}
