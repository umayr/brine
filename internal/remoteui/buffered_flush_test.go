package remoteui

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/umayr/brine/internal/targetregistry"
)

type bufferedFlushWriter struct {
	header             http.Header
	mu                 sync.Mutex
	data               []byte
	flushing           chan struct{}
	deadline           chan struct{}
	once               sync.Once
	expired            bool
	clearedAfterExpiry bool
	immediate          bool
}

func (w *bufferedFlushWriter) Header() http.Header { return w.header }
func (*bufferedFlushWriter) WriteHeader(int)       {}
func (w *bufferedFlushWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.data = append(w.data, p...)
	return len(p), nil
}
func (w *bufferedFlushWriter) SetWriteDeadline(t time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if t.IsZero() {
		if w.expired {
			w.clearedAfterExpiry = true
		}
	} else {
		w.expired = true
		w.once.Do(func() { close(w.deadline) })
	}
	return nil
}
func (w *bufferedFlushWriter) FlushError() error {
	close(w.flushing)
	if !w.immediate {
		<-w.deadline
	}
	return errors.New("private flush diagnostic")
}

type failingLogsBackend struct{ *ReadBackend }

func (b failingLogsBackend) Logs(context.Context, string, string, int, bool) (LogStream, error) {
	return nil, errors.New("synthetic backend failure")
}

type successfulStreamFinalFlushWriter struct {
	*bufferedFlushWriter
	flushCalls int
}

func (w *successfulStreamFinalFlushWriter) FlushError() error {
	w.flushCalls++
	// fixtureReadSession emits a chunk and an authentic end event. Their
	// streaming flushes succeed; only the final handler flush blocks.
	if w.flushCalls <= 2 {
		return nil
	}
	close(w.flushing)
	<-w.deadline
	return errors.New("final flush interrupted")
}

func TestSuccessfulLogStreamFinalFlushRetainsLeaseUntilDeadline(t *testing.T) {
	a, _, _, cookie, csrf := setup(t)
	reg, err := targetregistry.Open(filepath.Join(t.TempDir(), "registry"))
	if err != nil {
		t.Fatal(err)
	}
	rec := enrollmentRecord(t)
	if err := reg.Add(rec, rec); err != nil {
		t.Fatal(err)
	}
	backend, _ := NewReadBackend(reg, registryDeadlineProvider{reg})
	reads, _ := New(backend, a)
	reads.logDuration = func(bool) time.Duration { return 250 * time.Millisecond }
	enrollment, _ := NewEnrollmentHandler(a, reg, &enrollmentObserverFixture{})
	routes, err := ProtectedRoutes(reads, enrollment)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"_csrf": {csrf}, "host": {"server"}, "container": {testID}}
	req := httptest.NewRequest("POST", "/remote/logs", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	w := &successfulStreamFinalFlushWriter{bufferedFlushWriter: &bufferedFlushWriter{header: make(http.Header), flushing: make(chan struct{}), deadline: make(chan struct{})}}
	done := make(chan struct{})
	go func() { routes.ServeHTTP(w, req); close(done) }()
	select {
	case <-w.flushing:
	case <-time.After(3 * time.Second):
		t.Fatal("final flush did not start")
	}
	w.mu.Lock()
	body := string(w.data)
	w.mu.Unlock()
	if !strings.Contains(body, `"type":"end"`) {
		t.Fatalf("stream did not end before final flush: %q", body)
	}
	next := rec
	next.ID = "second"
	writer := make(chan error, 1)
	go func() { writer <- reg.Add(next, next) }()
	select {
	case <-writer:
		t.Fatal("writer bypassed active final flush lease")
	case <-time.After(30 * time.Millisecond):
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("operation deadline failed to interrupt final flush")
	}
	select {
	case err := <-writer:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("writer remained blocked")
	}
	w.mu.Lock()
	cleared := w.clearedAfterExpiry
	w.mu.Unlock()
	if cleared {
		t.Fatal("expired deadline was cleared")
	}
}

func TestBufferedLogsBackendErrorFlushUsesOperationDeadline(t *testing.T) {
	a, _, _, cookie, csrf := setup(t)
	reg, err := targetregistry.Open(filepath.Join(t.TempDir(), "registry"))
	if err != nil {
		t.Fatal(err)
	}
	rec := enrollmentRecord(t)
	if err := reg.Add(rec, rec); err != nil {
		t.Fatal(err)
	}
	backend, _ := NewReadBackend(reg, registryDeadlineProvider{reg})
	reads, _ := New(failingLogsBackend{backend}, a)
	reads.logDuration = func(bool) time.Duration { return 250 * time.Millisecond }
	enrollment, _ := NewEnrollmentHandler(a, reg, &enrollmentObserverFixture{})
	routes, err := ProtectedRoutes(reads, enrollment)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"_csrf": {csrf}, "host": {"server"}, "container": {testID}}
	req := httptest.NewRequest("POST", "/remote/logs", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	w := &bufferedFlushWriter{header: make(http.Header), flushing: make(chan struct{}), deadline: make(chan struct{})}
	done := make(chan struct{})
	go func() { routes.ServeHTTP(w, req); close(done) }()
	select {
	case <-w.flushing:
	case <-time.After(3 * time.Second):
		t.Fatal("error response did not enter flush")
	}
	w.mu.Lock()
	body := string(w.data)
	w.mu.Unlock()
	if !strings.Contains(body, "Logs unavailable") {
		t.Fatalf("unexpected buffered body: %q", body)
	}
	next := rec
	next.ID = "second"
	writer := make(chan error, 1)
	go func() { writer <- reg.Add(next, next) }()
	select {
	case <-writer:
		t.Fatal("writer bypassed lease before flush")
	case <-time.After(30 * time.Millisecond):
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("logs operation deadline did not interrupt flush")
	}
	select {
	case err := <-writer:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("writer remained blocked")
	}
	w.mu.Lock()
	cleared := w.clearedAfterExpiry
	w.mu.Unlock()
	if cleared {
		t.Fatal("expired deadline was cleared")
	}
}

func TestBufferedResponseFlushRetainsLeaseUntilDeadline(t *testing.T) {
	for _, path := range []string{"/remote/inventory?host=server", "/remote/inspect?host=server&container=" + testID} {
		t.Run(path, func(t *testing.T) {
			a, _, _, cookie, _ := setup(t)
			reg, err := targetregistry.Open(filepath.Join(t.TempDir(), "registry"))
			if err != nil {
				t.Fatal(err)
			}
			rec := enrollmentRecord(t)
			if err := reg.Add(rec, rec); err != nil {
				t.Fatal(err)
			}
			backend, _ := NewReadBackend(reg, registryDeadlineProvider{reg})
			reads, _ := New(backend, a)
			reads.readDuration = func() time.Duration { return 250 * time.Millisecond }
			enrollment, _ := NewEnrollmentHandler(a, reg, &enrollmentObserverFixture{})
			routes, err := ProtectedRoutes(reads, enrollment)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("GET", path, nil)
			req.AddCookie(cookie)
			w := &bufferedFlushWriter{header: make(http.Header), flushing: make(chan struct{}), deadline: make(chan struct{})}
			done := make(chan struct{})
			go func() { routes.ServeHTTP(w, req); close(done) }()
			select {
			case <-w.flushing:
			case <-time.After(3 * time.Second):
				t.Fatal("buffered response was not explicitly flushed")
			}
			w.mu.Lock()
			size := len(w.data)
			w.mu.Unlock()
			if size == 0 {
				t.Fatal("flush did not follow buffered writes")
			}
			next := rec
			next.ID = "second"
			writer := make(chan error, 1)
			go func() { writer <- reg.Add(next, next) }()
			select {
			case <-writer:
				t.Fatal("lease released before flush completed")
			case <-time.After(30 * time.Millisecond):
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("flush did not respect operation deadline")
			}
			select {
			case err := <-writer:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("writer remained blocked")
			}
			w.mu.Lock()
			cleared := w.clearedAfterExpiry
			w.mu.Unlock()
			if cleared {
				t.Fatal("deadline cleared after failed flush")
			}
		})
	}
}

func TestBufferedFlushFailureSanitizedAndConnectionDeadlineRetained(t *testing.T) {
	a, _, _, cookie, _ := setup(t)
	reg, err := targetregistry.Open(filepath.Join(t.TempDir(), "registry"))
	if err != nil {
		t.Fatal(err)
	}
	rec := enrollmentRecord(t)
	if err := reg.Add(rec, rec); err != nil {
		t.Fatal(err)
	}
	backend, _ := NewReadBackend(reg, registryDeadlineProvider{reg})
	reads, _ := New(backend, a)
	enrollment, _ := NewEnrollmentHandler(a, reg, &enrollmentObserverFixture{})
	routes, _ := ProtectedRoutes(reads, enrollment)
	req := httptest.NewRequest("GET", "/remote/inventory?host=server", nil)
	req.AddCookie(cookie)
	w := &bufferedFlushWriter{header: make(http.Header), flushing: make(chan struct{}), deadline: make(chan struct{}), immediate: true}
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	routes.ServeHTTP(w, req)
	if !strings.Contains(logs.String(), "remote response flush failed") || strings.Contains(logs.String(), "private flush diagnostic") {
		t.Fatalf("unsafe or absent error report: %s", logs.String())
	}
	w.mu.Lock()
	expired, cleared := w.expired, w.clearedAfterExpiry
	w.mu.Unlock()
	if !expired || cleared {
		t.Fatal("failed flush did not retain expired deadline")
	}
	next := rec
	next.ID = "second"
	if err := reg.Add(next, next); err != nil {
		t.Fatal(err)
	}
}
