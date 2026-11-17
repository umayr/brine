package targetregistry

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestAcquireReadCancelledWaitLeavesNoLock(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "registry"))
	if err != nil {
		t.Fatal(err)
	}
	old := record()
	if err = r.Add(old, old); err != nil {
		t.Fatal(err)
	}
	f, err := r.lock(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, lease, err := r.AcquireRead(ctx, old.ID)
	if !errors.Is(err, context.DeadlineExceeded) || lease != nil {
		t.Fatalf("waiting reader: %v", err)
	}
	f.Close()
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	next := old
	next.Revision++
	next.Host = "next.example"
	if err := r.UpdateContext(ctx2, old, next, ReviewedUpdate{old, next}); err != nil {
		t.Fatalf("stale lock: %v", err)
	}
}
