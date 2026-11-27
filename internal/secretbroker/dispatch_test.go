package secretbroker

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"
)

func TestDispatchPinsAllBindingWritersAndPolicy(t *testing.T) {
	_, m, _, identity, path := reviewFixture(t)
	peer, err := Open(path)
	wantError(t, err, nil)
	defer peer.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	dispatched := make(chan error, 1)
	go func() {
		dispatched <- m.WithCurrent(context.Background(), identity, func() error { close(entered); <-release; return nil })
	}()
	<-entered
	changed := make(chan error, 1)
	policy := make(chan error, 1)
	go func() { _, err := peer.Save(identity.Scope, identity.BindingRevision, "admin", nil); changed <- err }()
	go func() { policy <- m.ChangePolicy(func() error { return nil }) }()
	select {
	case <-changed:
		t.Fatal("binding edit passed pinned dispatch")
	case <-policy:
		t.Fatal("policy edit passed pinned dispatch")
	case <-time.After(30 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	wantError(t, <-dispatched, nil)
	wantError(t, <-changed, nil)
	wantError(t, <-policy, nil)
	sent := false
	wantError(t, m.WithCurrent(context.Background(), identity, func() error { sent = true; return nil }), ErrConflict)
	if sent {
		t.Fatal("stale binding dispatched")
	}
}
func TestDispatchConsumesOnceAndClearsAmbiguousBytes(t *testing.T) {
	_, m, _, identity, _ := reviewFixture(t)
	p, err := m.Prepare(context.Background(), identity, []string{"TOKEN"})
	wantError(t, err, nil)
	var held []byte
	err = m.Dispatch(context.Background(), p.ID, identity, func(_ Preparation, d *Delivery) error {
		d.Each(func(_ string, b []byte) error { held = b; return nil })
		return ErrUnavailable
	})
	wantError(t, err, ErrUnavailable)
	if len(held) == 0 || bytes.Count(held, []byte{0}) != len(held) {
		t.Fatal("delivery bytes retained after ambiguous send")
	}
	called := false
	err = m.Dispatch(context.Background(), p.ID, identity, func(Preparation, *Delivery) error { called = true; return nil })
	wantError(t, err, ErrUnavailable)
	if called {
		t.Fatal("retried consumed preparation")
	}
}
func TestPolicySwapInvalidatesPreparationAndDispatchAuthorization(t *testing.T) {
	_, m, _, identity, _ := reviewFixture(t)
	allowed := true
	m.authorize = func(context.Context, ReviewIdentity, []Binding) error {
		if !allowed {
			return ErrConflict
		}
		return nil
	}
	p, err := m.Prepare(context.Background(), identity, []string{"TOKEN"})
	wantError(t, err, nil)
	wantError(t, m.ChangePolicy(func() error { allowed = false; return nil }), nil)
	sent := false
	err = m.WithCurrent(context.Background(), identity, func() error { sent = true; return nil })
	wantError(t, err, ErrConflict)
	if sent {
		t.Fatal("revoked policy dispatched")
	}
	if _, err = m.Inspect(context.Background(), p.ID, identity); err == nil {
		t.Fatal("old preparation survived policy swap")
	}
}
