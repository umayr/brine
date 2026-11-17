package remoteui

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/umayr/brine/internal/remoteagent"
	"github.com/umayr/brine/internal/targetregistry"
)

type ownedFixture struct {
	fixtureReadSession
	closed chan struct{}
	err    error
}

func (s *ownedFixture) Close() error { close(s.closed); return s.err }
func TestRegistryLeaseProviderBlocksUpdatesUntilSessionClosed(t *testing.T) {
	r, err := targetregistry.Open(filepath.Join(t.TempDir(), "registry"))
	if err != nil {
		t.Fatal(err)
	}
	rec := enrollmentRecord(t)
	if err = r.Add(rec, rec); err != nil {
		t.Fatal(err)
	}
	session := &ownedFixture{closed: make(chan struct{}), err: errors.New("synthetic cleanup")}
	p, err := NewRegistryLeaseProvider(r, remoteagent.Config{LocalDir: "/tmp/private", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	p.open = func(_ context.Context, got targetregistry.Record, _ remoteagent.Config) (ownedReadSession, error) {
		if got != rec {
			t.Error("wrong record")
		}
		return session, nil
	}
	_, _, release, err := p.Acquire(context.Background(), rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	next := rec
	next.Revision++
	next.Host = "next.example"
	done := make(chan error, 1)
	go func() {
		done <- r.UpdateContext(context.Background(), rec, next, targetregistry.ReviewedUpdate{Previous: rec, Next: next})
	}()
	select {
	case err := <-done:
		t.Fatal("writer bypassed lease", err)
	case <-time.After(100 * time.Millisecond):
	}
	if release() == nil || release() == nil {
		t.Fatal("cleanup error not preserved")
	}
	select {
	case <-session.closed:
	default:
		t.Fatal("session not closed")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("writer not released")
	}
}
