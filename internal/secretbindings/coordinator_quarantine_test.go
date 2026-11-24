package secretbindings

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestCoordinatorChecksQuarantineAfterAdmission(t *testing.T) {
	denied := errors.New("quarantined unknown execution")
	var blocked atomic.Bool
	c := Coordinator{CheckAdmission: func(id string) error {
		if id == "a" && blocked.Load() {
			return denied
		}
		return nil
	}}
	release, err := c.LockContext(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		unlock, err := c.LockContext(ctx, "a")
		if unlock != nil {
			unlock()
		}
		result <- err
	}()
	blocked.Store(true)
	release()
	if err := <-result; !errors.Is(err, denied) {
		t.Fatalf("queued caller escaped quarantine: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if unlock, err := c.LockContext(ctx, "a"); unlock != nil || !errors.Is(err, denied) {
		t.Fatalf("denied admission leaked token: %v", err)
	}
	unlock, err := c.LockContext(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	blocked.Store(false)
	unlock, err = c.LockContext(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}

func TestCoordinatorLegacyLockCannotIgnoreDenial(t *testing.T) {
	denied := errors.New("unreadable quarantine")
	c := Coordinator{CheckAdmission: func(string) error { return denied }}
	defer func() {
		if got := recover(); got != denied {
			t.Fatalf("denial ignored: %v", got)
		}
	}()
	c.Lock("a")
}
