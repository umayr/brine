package secretbroker

import (
	"bytes"
	"context"
	"strings"
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
	var delivery *Delivery
	err = m.Dispatch(context.Background(), p.ID, identity, func(_ Preparation, d *Delivery) error {
		delivery = d
		d.Each(func(_ string, b []byte) error { held = b; return nil })
		return ErrUnavailable
	})
	wantError(t, err, ErrUnavailable)
	if len(held) == 0 || bytes.Count(held, []byte{0}) != len(held) || delivery.values != nil {
		t.Fatal("delivery not closed after ambiguous send")
	}
	called := false
	err = m.Dispatch(context.Background(), p.ID, identity, func(Preparation, *Delivery) error { called = true; return nil })
	wantError(t, err, ErrUnavailable)
	if called {
		t.Fatal("retried consumed preparation")
	}
	_, replay, err := m.Take(context.Background(), p.ID, identity)
	defer replay.Close()
	wantError(t, err, ErrUnavailable)
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

func TestPendingIdentityMismatchDoesNotConsume(t *testing.T) {
	_, m, f, identity, _ := reviewFixture(t)
	ctx := context.Background()
	p, err := m.Prepare(ctx, identity, []string{"TOKEN"})
	wantError(t, err, nil)
	wrong := identity
	wrong.SessionDigest = "sha256:" + strings.Repeat("b", 64)
	_, rejected, err := m.Take(ctx, p.ID, wrong)
	defer rejected.Close()
	wantError(t, err, ErrUnavailable)
	called := false
	err = m.Dispatch(ctx, p.ID, wrong, func(Preparation, *Delivery) error { called = true; return nil })
	wantError(t, err, ErrUnavailable)
	if rejected != nil || called || m.bytes != len(f.value) {
		t.Fatal("identity mismatch transferred delivery or changed pending byte accounting")
	}
	_, delivery, err := m.Take(ctx, p.ID, identity)
	defer delivery.Close()
	wantError(t, err, nil)
	seen := false
	wantError(t, delivery.Each(func(name string, b []byte) error {
		seen = name == "TOKEN" && string(b) == f.value
		return nil
	}), nil)
	if !seen {
		t.Fatal("matching identity did not receive the original preparation")
	}
}

func TestConcurrentTakeAndDispatchHaveOneWinner(t *testing.T) {
	_, m, _, identity, _ := reviewFixture(t)
	ctx := context.Background()
	p, err := m.Prepare(ctx, identity, []string{"TOKEN"})
	wantError(t, err, nil)
	start := make(chan struct{})
	taken, dispatched := make(chan error, 1), make(chan error, 1)
	go func() {
		<-start
		_, delivery, err := m.Take(ctx, p.ID, identity)
		delivery.Close()
		taken <- err
	}()
	sends := 0
	go func() {
		<-start
		dispatched <- m.Dispatch(ctx, p.ID, identity, func(Preparation, *Delivery) error {
			sends++
			return nil
		})
	}()
	close(start)
	takeErr, dispatchErr := <-taken, <-dispatched
	switch {
	case takeErr == nil && dispatchErr == ErrUnavailable && sends == 0:
	case takeErr == ErrUnavailable && dispatchErr == nil && sends == 1:
	default:
		t.Fatalf("expected exactly one consumer: Take=%v, Dispatch=%v, sends=%d", takeErr, dispatchErr, sends)
	}
	if m.bytes != 0 || len(m.pending) != 0 {
		t.Fatal("consumed preparation remained pending or accounted for")
	}
}
