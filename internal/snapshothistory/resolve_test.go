package snapshothistory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestLeasedAttendedReadsDoNotWaitForArchiveLock(t *testing.T) {
	s := &Store{}
	q := AttendedBindingSelector{Locator: ObjectID{1}, Scope: "generated-scope", Target: strings.Repeat("a", 64), AuthorityRevision: strings.Repeat("b", 64), Stack: "synthetic"}
	ref := OpenRequest{ObjectID: q.Locator, Intent: Intent{Attended: &AttendedIntent{}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	done := make(chan [3]error, 1)
	go func() {
		_, e1 := s.ResolveAttendedBinding(ctx, q, func(context.Context, AttendedBindingSelector) error { return nil })
		_, e2 := s.TryReadAttendedEvidence(ctx, ref)
		_, e3 := s.TryOpenAttended(ctx, ref)
		done <- [3]error{e1, e2, e3}
	}()
	select {
	case errs := <-done:
		for _, err := range errs {
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("contended read: %v", err)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("archive read waited on held lock")
	}
	called := false
	cancel()
	_, err := s.ResolveAttendedBinding(ctx, q, func(context.Context, AttendedBindingSelector) error { called = true; return nil })
	if called || !errors.Is(err, ErrUnavailable) {
		t.Fatal("cancelled request reached authorization or archive")
	}
}
