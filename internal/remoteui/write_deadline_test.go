package remoteui

import (
	"errors"
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

type deadlineWriter struct {
	header   http.Header
	started  chan struct{}
	deadline chan struct{}
	once     sync.Once
}

func (w *deadlineWriter) Header() http.Header { return w.header }
func (w *deadlineWriter) WriteHeader(int)     {}
func (w *deadlineWriter) Write([]byte) (int, error) {
	close(w.started)
	<-w.deadline
	return 0, errors.New("deadline")
}
func (w *deadlineWriter) SetWriteDeadline(t time.Time) error {
	if !t.IsZero() {
		w.once.Do(func() { close(w.deadline) })
	}
	return nil
}
func TestLogDeadlineInterruptsBlockedWriteAndReleasesLease(t *testing.T) {
	a, _, _, cookie, csrf := setup(t)
	reg, err := targetregistry.Open(filepath.Join(t.TempDir(), "registry"))
	if err != nil {
		t.Fatal(err)
	}
	provider := &fixtureLeaseProvider{}
	backend, _ := NewReadBackend(reg, provider)
	reads, _ := New(backend, a)
	reads.logDuration = func(bool) time.Duration { return 75 * time.Millisecond }
	enrollment, _ := NewEnrollmentHandler(a, reg, &enrollmentObserverFixture{})
	routes, err := ProtectedRoutes(reads, enrollment)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"_csrf": {csrf}, "host": {"host"}, "container": {testID}, "follow": {"yes"}}
	req := httptest.NewRequest("POST", "/remote/logs", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	w := &deadlineWriter{header: make(http.Header), started: make(chan struct{}), deadline: make(chan struct{})}
	done := make(chan struct{})
	go func() { routes.ServeHTTP(w, req); close(done) }()
	select {
	case <-w.started:
	case <-time.After(3 * time.Second):
		t.Fatal("write did not start")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("operation deadline did not unblock write")
	}
	if provider.active {
		t.Fatal("lease still held")
	}
}

func TestLogoutInterruptsBlockedResponseWriteAndReleasesLease(t *testing.T) {
	a, _, _, cookie, csrf := setup(t)
	reg, err := targetregistry.Open(filepath.Join(t.TempDir(), "registry"))
	if err != nil {
		t.Fatal(err)
	}
	provider := &fixtureLeaseProvider{}
	backend, _ := NewReadBackend(reg, provider)
	reads, _ := New(backend, a)
	enrollment, _ := NewEnrollmentHandler(a, reg, &enrollmentObserverFixture{})
	routes, err := ProtectedRoutes(reads, enrollment)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/remote/inventory?host=host", nil)
	req.AddCookie(cookie)
	w := &deadlineWriter{header: make(http.Header), started: make(chan struct{}), deadline: make(chan struct{})}
	done := make(chan struct{})
	go func() { routes.ServeHTTP(w, req); close(done) }()
	select {
	case <-w.started:
	case <-time.After(3 * time.Second):
		t.Fatal("write did not start")
	}
	logout := httptest.NewRequest("POST", "/logout", strings.NewReader(url.Values{"_csrf": {csrf}}.Encode()))
	logout.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	logout.AddCookie(cookie)
	response := httptest.NewRecorder()
	a.Middleware(http.HandlerFunc(a.HandleLogout)).ServeHTTP(response, logout)
	if response.Code != http.StatusSeeOther {
		t.Fatal(response.Code)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("blocked write/lease not released")
	}
	if provider.active {
		t.Fatal("lease still held")
	}
}
