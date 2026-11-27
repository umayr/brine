package webhooks

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testService(t *testing.T, max int) *Service {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	s, e := Open(Config{Directory: dir, EncryptionKey: bytes.Repeat([]byte{1}, 32), MaxPending: max})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func testEndpoint(t *testing.T, s *Service) (Endpoint, string) {
	t.Helper()
	ep, secret, e := s.SaveEndpoint(context.Background(), Endpoint{Name: "Example", URL: "https://hooks.example.com/events", Events: []string{"operation.completed"}, Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	return ep, secret
}
func testEvent(id string) Event {
	return Event{ID: id, SchemaVersion: 1, Type: "operation.completed", OccurredAt: time.Date(2026, 11, 26, 16, 0, 0, 0, time.UTC), Stack: Resource{ID: "example", Name: "example", Kind: "stack"}, Source: Resource{Kind: "git"}, Target: Resource{ID: "local", Kind: "local"}, Action: "deploy", Outcome: "unknown"}
}
func TestAtomicPublishAndDedupe(t *testing.T) {
	ctx := context.Background()
	s := testService(t, 1)
	ep, _ := testEndpoint(t, s)
	event := testEvent("evt_one")
	if e := s.Publish(ctx, event); e != nil {
		t.Fatal(e)
	}
	if e := s.Publish(ctx, event); e != nil {
		t.Fatal(e)
	}
	changed := event
	changed.Outcome = "success"
	if e := s.Publish(ctx, changed); !errors.Is(e, ErrConflict) {
		t.Fatalf("collision: %v", e)
	}
	if e := s.Publish(ctx, testEvent("evt_two")); !errors.Is(e, ErrCapacity) {
		t.Fatalf("capacity: %v", e)
	}
	var n int
	s.db.QueryRow("SELECT COUNT(*) FROM events WHERE id='evt_two'").Scan(&n)
	if n != 0 {
		t.Fatal("event persisted without fanout")
	}
	ds, e := s.Deliveries(ctx, ep.ID)
	if e != nil || len(ds) != 1 || ds[0].Event.Outcome != "unknown" {
		t.Fatalf("deliveries=%+v %v", ds, e)
	}
}
func TestEndpointScopeAndVersions(t *testing.T) {
	ctx := context.Background()
	s := testService(t, 10)
	ep, _ := testEndpoint(t, s)
	ep.Stacks = []string{"other"}
	updated, secret, e := s.SaveEndpoint(ctx, ep)
	if e != nil || secret != "" || updated.Version != 2 {
		t.Fatalf("update %v %s", e, secret)
	}
	if _, _, e = s.SaveEndpoint(ctx, ep); !errors.Is(e, ErrConflict) {
		t.Fatalf("stale update %v", e)
	}
	if e = s.Publish(ctx, testEvent("evt_scope")); e != nil {
		t.Fatal(e)
	}
	ds, _ := s.Deliveries(ctx, ep.ID)
	if len(ds) != 0 {
		t.Fatal("scope ignored")
	}
	if e = s.DeleteEndpoint(ctx, ep.ID, ep.Version); !errors.Is(e, ErrConflict) {
		t.Fatal("stale deletion accepted")
	}
	if e = s.DeleteEndpoint(ctx, ep.ID, updated.Version); e != nil {
		t.Fatal(e)
	}
}
func TestURLChangeAndRotationRetireQueuedPayloads(t *testing.T) {
	ctx := context.Background()
	s := testService(t, 10)
	ep, key := testEndpoint(t, s)
	if len(key) != 64 {
		t.Fatal("weak signing secret")
	}
	s.Publish(ctx, testEvent("evt_old"))
	ep.URL = "https://replacement.example.com/events"
	ep, _, e := s.SaveEndpoint(ctx, ep)
	if e != nil {
		t.Fatal(e)
	}
	ds, _ := s.Deliveries(ctx, ep.ID)
	if ds[0].Status != "cancelled" {
		t.Fatal("old queue redirected")
	}
	if e = s.Redeliver(ctx, ds[0].ID); !errors.Is(e, ErrConflict) {
		t.Fatal("retired URL redelivered")
	}
	s.Publish(ctx, testEvent("evt_key"))
	rotated, e := s.RotateSecret(ctx, ep.ID, ep.Version)
	if e != nil || rotated == key {
		t.Fatal("key not rotated")
	}
	ds, _ = s.Deliveries(ctx, ep.ID)
	for _, d := range ds {
		if d.Status != "cancelled" {
			t.Fatal("retired credential pending")
		}
	}
}
func TestEncryptedStorageAndRestartRecovery(t *testing.T) {
	ctx := context.Background()
	s := testService(t, 10)
	ep, key := testEndpoint(t, s)
	s.Publish(ctx, testEvent("evt_recover"))
	var encrypted []byte
	s.db.QueryRow("SELECT secret FROM endpoints WHERE id=?", ep.ID).Scan(&encrypted)
	if bytes.Contains(encrypted, []byte(key)) {
		t.Fatal("secret in plaintext")
	}
	s.db.Exec("UPDATE deliveries SET status='sending'")
	cfg := s.cfg
	cfg.EncryptionKey = bytes.Repeat([]byte{1}, 32)
	if _, e := Open(cfg); e == nil {
		t.Fatal("concurrent process accepted")
	}
	s.Close()
	wrong := cfg
	wrong.EncryptionKey = bytes.Repeat([]byte{2}, 32)
	if _, e := Open(wrong); e == nil {
		t.Fatal("wrong encryption key accepted")
	}
	reopened, e := Open(cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	ds, e := reopened.Deliveries(ctx, ep.ID)
	if e != nil || len(ds) != 1 || ds[0].Status != "pending" {
		t.Fatalf("recovery %+v %v", ds, e)
	}
}
func TestStorageRejectsSymlinksAndPermissions(t *testing.T) {
	for _, kind := range []string{"directory-symlink", "db-symlink", "public-dir", "public-db"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "private")
			os.Mkdir(dir, 0700)
			switch kind {
			case "directory-symlink":
				os.Remove(dir)
				os.Symlink(root, dir)
			case "db-symlink":
				os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(dir, "webhooks.db"))
			case "public-dir":
				os.Chmod(dir, 0755)
			case "public-db":
				os.WriteFile(filepath.Join(dir, "webhooks.db"), nil, 0644)
			}
			if s, e := Open(Config{Directory: dir, EncryptionKey: bytes.Repeat([]byte{1}, 32)}); e == nil {
				s.Close()
				t.Fatal("unsafe path accepted")
			}
		})
	}
}
