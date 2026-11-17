package targetregistry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestExpiryWhileWaitingToCommit(t *testing.T) {
	r := registry(t)
	rec := record()
	lock, err := r.lock(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err = r.AddContext(ctx, rec, rec)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("lock wait did not expire", err)
	}
	if err = lock.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Get(rec.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired commit published", err)
	}
}

func TestCancelImmediatelyBeforePublication(t *testing.T) {
	r := registry(t)
	rec := record()
	ctx, cancel := context.WithCancel(context.Background())
	r.beforePublish = cancel
	if err := r.AddContext(ctx, rec, rec); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation not returned", err)
	}
	if _, err := r.Get(rec.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("record published", err)
	}
}
