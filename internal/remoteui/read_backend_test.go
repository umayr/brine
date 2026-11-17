package remoteui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/targetregistry"
)

type fixtureReadSession struct{}

func (fixtureReadSession) Inventory(context.Context, int) ([]agentclient.InventoryItem, error) {
	return []agentclient.InventoryItem{{ID: testID, Stack: "stack", Service: "svc"}}, nil
}
func (fixtureReadSession) Inspect(_ context.Context, id string) (agentclient.InspectResult, error) {
	var r agentclient.InspectResult
	r.ContainerID = id
	r.Inspect.ID = id
	return r, nil
}
func (fixtureReadSession) Logs(ctx context.Context, id string, _ agentclient.LogOptions, emit func(context.Context, agentclient.LogEvent) error) error {
	if err := emit(ctx, agentclient.LogEvent{Type: "chunk", Bytes: []byte("x"), Stream: "stdout"}); err != nil {
		return err
	}
	return emit(ctx, agentclient.LogEvent{Type: "end", Reason: "complete"})
}

type fixtureLeaseProvider struct {
	active     bool
	closeErr   error
	expired    bool
	acquireErr error
}

func (p *fixtureLeaseProvider) Acquire(ctx context.Context, id string) (context.Context, ReadSession, func() error, error) {
	if p.acquireErr != nil {
		return nil, nil, nil, p.acquireErr
	}
	if id != "host" {
		return nil, nil, nil, errors.New("unknown")
	}
	p.active = true
	if p.expired {
		c, cancel := context.WithCancel(ctx)
		cancel()
		ctx = c
	}
	return ctx, fixtureReadSession{}, func() error { p.active = false; return p.closeErr }, nil
}

type leaseCheckingWriter struct {
	*httptest.ResponseRecorder
	provider *fixtureLeaseProvider
	unleased bool
}

func (w *leaseCheckingWriter) Write(p []byte) (int, error) {
	if !w.provider.active {
		w.unleased = true
	}
	return w.ResponseRecorder.Write(p)
}

func TestAcquireCleanupFailureIsRecordedWithoutDetails(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider *fixtureLeaseProvider
	}{
		{"expired", &fixtureLeaseProvider{expired: true, closeErr: errors.New("secret cleanup")}},
		{"acquire", &fixtureLeaseProvider{acquireErr: errors.Join(errors.New("secret cause"), ErrReadCleanup)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, _, cookie, _ := setup(t)
			r, err := targetregistry.Open(filepath.Join(t.TempDir(), "registry"))
			if err != nil {
				t.Fatal(err)
			}
			b, _ := NewReadBackend(r, tc.provider)
			h, _ := New(b, a)
			var buf bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&buf)
			defer log.SetOutput(previous)
			req := httptest.NewRequest("GET", "/remote/inventory?host=host", nil)
			req.AddCookie(cookie)
			w := httptest.NewRecorder()
			h.Protected().ServeHTTP(w, req)
			if w.Code != 503 || !strings.Contains(buf.String(), "remote read cleanup failed") || strings.Contains(buf.String(), "secret") {
				t.Fatal(w.Code, buf.String())
			}
		})
	}
}

func TestCleanupErrorIsRecordedWithoutDetails(t *testing.T) {
	a, _, _, cookie, _ := setup(t)
	r, err := targetregistry.Open(filepath.Join(t.TempDir(), "registry"))
	if err != nil {
		t.Fatal(err)
	}
	p := &fixtureLeaseProvider{closeErr: errors.New("sensitive detail")}
	b, _ := NewReadBackend(r, p)
	h, _ := New(b, a)
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(previous)
	req := httptest.NewRequest("GET", "/remote/inventory?host=host", nil)
	req.AddCookie(cookie)
	h.Protected().ServeHTTP(httptest.NewRecorder(), req)
	if !strings.Contains(buf.String(), "remote read cleanup failed") || strings.Contains(buf.String(), "sensitive detail") {
		t.Fatal("cleanup log missing or unsafe", buf.String())
	}
}

func TestLeaseHeldThroughHTTPRendering(t *testing.T) {
	a, _, _, cookie, _ := setup(t)
	r, err := targetregistry.Open(filepath.Join(t.TempDir(), "registry"))
	if err != nil {
		t.Fatal(err)
	}
	provider := &fixtureLeaseProvider{}
	b, err := NewReadBackend(r, provider)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(b, a)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/remote/inventory?host=host", nil)
	req.AddCookie(cookie)
	w := &leaseCheckingWriter{ResponseRecorder: httptest.NewRecorder(), provider: provider}
	h.Protected().ServeHTTP(w, req)
	if w.unleased || provider.active || w.Code != 200 {
		t.Fatal("lease not held through response", w.unleased, provider.active, w.Code)
	}
}

func TestLogBackpressureCancellationUnblocksProducer(t *testing.T) {
	r, err := targetregistry.Open(filepath.Join(t.TempDir(), "registry"))
	if err != nil {
		t.Fatal(err)
	}
	p := &fixtureLeaseProvider{}
	b, _ := NewReadBackend(r, p)
	ctx, release, err := b.AcquireRead(context.Background(), "host")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	stream, err := b.Logs(ctx, "host", testID, 100, true)
	if err != nil {
		t.Fatal(err)
	}
	// No consumer reads the unbuffered event channel. Close must interrupt its
	// blocked callback and wait for the producer rather than leaking a goroutine.
	done := make(chan struct{})
	go func() { _ = stream.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("blocked producer was not cancelled")
	}
}

func TestReadBackendRequiresBoundLease(t *testing.T) {
	r, err := targetregistry.Open(filepath.Join(t.TempDir(), "registry"))
	if err != nil {
		t.Fatal(err)
	}
	p := &fixtureLeaseProvider{}
	b, err := NewReadBackend(r, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Inventory(context.Background(), "host"); err == nil {
		t.Fatal("unleased read")
	}
	ctx, release, err := b.AcquireRead(context.Background(), "host")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Inventory(ctx, "other"); err == nil {
		t.Fatal("host substitution")
	}
	items, err := b.Inventory(ctx, "host")
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	stream, err := b.Logs(ctx, "host", testID, 100, false)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := stream.Next(ctx)
	if err != nil || ev.Type != "chunk" || !p.active {
		t.Fatal(ev, err)
	}
	ev, err = stream.Next(ctx)
	if err != nil || ev.Type != "end" || !p.active {
		t.Fatal(ev, err)
	}
	_, err = stream.Next(ctx)
	if err != io.EOF {
		t.Fatal(err)
	}
	if err = stream.Close(); err != nil {
		t.Fatal(err)
	}
	release()
	if p.active {
		t.Fatal("lease not released")
	}
	if _, err = b.Reveal(ctx, "host", testID, Confirmation{}); err == nil {
		t.Fatal("reveal accepted")
	}
}
