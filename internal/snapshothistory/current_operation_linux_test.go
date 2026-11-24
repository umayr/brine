//go:build linux

package snapshothistory

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func nextAttendedCommit(r OpenRequest, n byte, stack string) CommitRequest {
	next := request(n)
	next.Intent.ApprovedTargetScope = append([]byte(nil), r.Intent.ApprovedTargetScope...)
	next.Intent.Attended = cloneAttended(r.Intent.Attended)
	next.Intent.Attended.Stack = stack
	return next
}

func TestAttendedGuardSerializesConcurrentCommitsAndSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	o, s, older, receipt := receiptFixture(t, 10)
	scope, stack := string(older.Intent.ApprovedTargetScope), older.Intent.Attended.Stack
	if err := s.CheckAttendedIdle(ctx, scope, stack); !errors.Is(err, ErrUnresolvedOperation) {
		t.Fatalf("intent-only escaped guard: %v", err)
	}
	if err := s.MarkDeliveryUnknown(ctx, older, receipt.ObservedAt); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordTerminalObservation(ctx, older, receipt); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckAttendedIdle(ctx, scope, stack); err != nil {
		t.Fatalf("single resolved intent blocked: %v", err)
	}
	// Both callers can successfully pre-check. Only the exclusive reservation
	// at Commit can decide the race. Different IDs and journal are intentional.
	start := make(chan struct{})
	type result struct {
		ref OpenRequest
		err error
	}
	results := make(chan result, 2)
	for _, n := range []byte{73, 74} {
		r := nextAttendedCommit(older, n, stack)
		go func() { <-start; ref, err := s.Commit(ctx, r); results <- result{ref, err} }()
	}
	close(start)
	var winner OpenRequest
	wins, blocks := 0, 0
	for range 2 {
		r := <-results
		if r.err == nil {
			winner = r.ref
			wins++
		} else if errors.Is(r.err, ErrUnresolvedOperation) {
			blocks++
		} else {
			t.Fatal(r.err)
		}
	}
	if wins != 1 || blocks != 1 {
		t.Fatalf("wins=%d blocks=%d", wins, blocks)
	}
	if err := s.MarkDeliveryUnknown(ctx, winner, receipt.ObservedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commit(ctx, nextAttendedCommit(older, 75, "other-stack")); err != nil {
		t.Fatalf("different stack blocked: %v", err)
	}
	different := nextAttendedCommit(older, 76, stack)
	different.Intent.ApprovedTargetScope = []byte("different-enrollment")
	if _, err := s.Commit(ctx, different); err != nil {
		t.Fatalf("different scope blocked: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, err := reopened.ReadEvidence(ctx, older); err != nil || got.State != VerifiedReceipt {
		t.Fatalf("old receipt lost: %v", err)
	}
	if got, err := reopened.ReadEvidence(ctx, winner); err != nil || got.State != DeliveryUnknown {
		t.Fatalf("new unknown lost: %v", err)
	}
	if err := reopened.CheckAttendedIdle(ctx, scope, stack); !errors.Is(err, ErrUnresolvedOperation) {
		t.Fatalf("old success cleared newer unknown: %v", err)
	}
	if _, err := reopened.Commit(ctx, nextAttendedCommit(older, 77, stack)); !errors.Is(err, ErrUnresolvedOperation) {
		t.Fatalf("post-restart duplicate work admitted: %v", err)
	}
}

func TestAttendedGuardPartialReceiptRemainsBlocking(t *testing.T) {
	ctx := context.Background()
	_, s, ref, r := receiptFixture(t, 10)
	r.State, r.Effects, r.OperationID = "failed", "partial", "op_"+strings.Repeat("b", 32)
	if err := s.MarkDeliveryUnknown(ctx, ref, r.ObservedAt); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordTerminalObservation(ctx, ref, r); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckAttendedIdle(ctx, string(ref.Intent.ApprovedTargetScope), ref.Intent.Attended.Stack); !errors.Is(err, ErrUnresolvedOperation) {
		t.Fatalf("partial cleared blocker: %v", err)
	}
}
