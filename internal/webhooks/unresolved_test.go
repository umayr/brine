package webhooks

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestUnresolvedPagesSurviveRetiredTriggerAndRecentTraffic(t *testing.T) {
	s := testService(t, 10)
	tr, _ := testTrigger(t, s)
	ctx := context.Background()
	old := s.now().Add(-48 * time.Hour)
	for i := 0; i < 455; i++ {
		status := "unknown"
		if i >= 205 {
			status = "completed"
		}
		if i == 204 {
			status = "awaiting_review"
		}
		r := IncomingRequest{ID: fmt.Sprintf("req_%032d", i), TriggerID: tr.ID, IdempotencyKey: fmt.Sprint(i), Binding: tr, Status: status, CreatedAt: old, UpdatedAt: old}
		if i >= 205 {
			r.CreatedAt = s.now()
			r.UpdatedAt = s.now()
		}
		if _, e := s.db.Exec("INSERT INTO incoming_requests(id,trigger_id,idempotency_key,digest,binding,record,status,created,updated) VALUES(?,?,?,?,?,?,?,?,?)", r.ID, tr.ID, r.IdempotencyKey, "digest", marshal(tr), marshal(r), status, encodeTime(r.CreatedAt), encodeTime(r.UpdatedAt)); e != nil {
			t.Fatal(e)
		}
	}
	if e := s.DeleteTrigger(ctx, tr.ID, tr.Version); e != nil {
		t.Fatal(e)
	}
	recent, e := s.Requests(ctx, "")
	if e != nil || len(recent) != 200 {
		t.Fatal("recent fixture", e, len(recent))
	}
	for _, r := range recent {
		if r.Status != "completed" {
			t.Fatal("fixture did not bury old unresolved")
		}
	}
	page, next, e := s.UnresolvedRequests(ctx, "")
	if e != nil || len(page) != 200 || next != page[199].ID {
		t.Fatal("first page", len(page), next, e)
	}
	// Removing a blocker on a prior page must not shift/skip the next page.
	if _, e = s.db.Exec("DELETE FROM incoming_requests WHERE id=?", page[0].ID); e != nil {
		t.Fatal(e)
	}
	last, end, e := s.UnresolvedRequests(ctx, next)
	if e != nil || len(last) != 5 || end != "" || last[4].Status != "awaiting_review" {
		t.Fatal("last page", last, end, e)
	}
	for _, r := range last {
		if r.Binding.StackID != tr.StackID || r.Binding.TargetID != tr.TargetID {
			t.Fatal("binding lost")
		}
	}
	if _, _, e = s.UnresolvedRequests(ctx, "invalid cursor?"); e != ErrInvalid {
		t.Fatal("invalid cursor", e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.UnresolvedRequests(ctx, ""); e != errStorage {
		t.Fatal("closed storage inferred empty")
	}
}
