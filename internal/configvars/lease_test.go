package configvars

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/umayr/brine/internal/store"
)

func TestLeaseCancellationPreventsLateWrites(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewFSStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	v, err := New(dir, st)
	if err != nil {
		t.Fatal(err)
	}
	release, err := v.ReadLease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- v.SetContext(ctx, "", "NAME", "value", false, nil) }()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("writer stuck")
	}
	readCtx, readCancel := context.WithCancel(context.Background())
	readCancel()
	if _, err = v.ReadLease(readCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("read lease: %v", err)
	}
	release()
	values, err := v.Values("")
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 0 {
		t.Fatalf("late write: %v", values)
	}
}
