package remoteui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/targetregistry"
)

type blockingSession struct{ started, stopped chan struct{} }

func (s *blockingSession) Inventory(context.Context, int) ([]agentclient.InventoryItem, error) {
	return nil, nil
}
func (s *blockingSession) Inspect(context.Context, string) (agentclient.InspectResult, error) {
	return agentclient.InspectResult{}, nil
}
func (s *blockingSession) Logs(ctx context.Context, _ string, _ agentclient.LogOptions, _ func(context.Context, agentclient.LogEvent) error) error {
	close(s.started)
	<-ctx.Done()
	close(s.stopped)
	return ctx.Err()
}

type blockingProvider struct {
	session  *blockingSession
	released chan struct{}
}

func (p *blockingProvider) Acquire(ctx context.Context, _ string) (context.Context, ReadSession, func() error, error) {
	return ctx, p.session, func() error { close(p.released); return nil }, nil
}

func TestSessionExpiryCancelsLogAndReleasesLease(t *testing.T) {
	a, err := auth.New(auth.Config{User: "admin", Password: "secret", SessionTTL: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRequest("POST", "/login", strings.NewReader("user=admin&password=secret"))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	logged := httptest.NewRecorder()
	a.HandleLogin(logged, login)
	cookie := logged.Result().Cookies()[0]
	var csrf string
	get := httptest.NewRequest("GET", "/test", nil)
	get.AddCookie(cookie)
	a.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { csrf = a.CSRFFor(r) })).ServeHTTP(httptest.NewRecorder(), get)
	if csrf == "" {
		t.Fatal("csrf unavailable")
	}
	reg, err := targetregistry.Open(filepath.Join(t.TempDir(), "registry"))
	if err != nil {
		t.Fatal(err)
	}
	session := &blockingSession{make(chan struct{}), make(chan struct{})}
	provider := &blockingProvider{session, make(chan struct{})}
	backend, _ := NewReadBackend(reg, provider)
	h, _ := New(backend, a)
	form := url.Values{"_csrf": {csrf}, "host": {"host"}, "container": {testID}, "follow": {"yes"}}
	req := httptest.NewRequest("POST", "/remote/logs", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	done := make(chan struct{})
	go func() { h.Protected().ServeHTTP(httptest.NewRecorder(), req); close(done) }()
	for _, ch := range []<-chan struct{}{session.started, session.stopped, provider.released, done} {
		select {
		case <-ch:
		case <-time.After(3 * time.Second):
			t.Fatal("expiry did not stop stream/lease")
		}
	}
}

func TestLogoutCancelsLogAndReleasesRegistryLease(t *testing.T) {
	a, _, _, cookie, csrf := setup(t)
	reg, err := targetregistry.Open(filepath.Join(t.TempDir(), "registry"))
	if err != nil {
		t.Fatal(err)
	}
	session := &blockingSession{make(chan struct{}), make(chan struct{})}
	provider := &blockingProvider{session, make(chan struct{})}
	backend, err := NewReadBackend(reg, provider)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(backend, a)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"_csrf": {csrf}, "host": {"host"}, "container": {testID}, "follow": {"yes"}}
	req := httptest.NewRequest("POST", "/remote/logs", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	done := make(chan struct{})
	go func() { h.Protected().ServeHTTP(httptest.NewRecorder(), req); close(done) }()
	select {
	case <-session.started:
	case <-time.After(3 * time.Second):
		t.Fatal("log did not start")
	}
	logout := httptest.NewRequest("POST", "/logout", strings.NewReader(url.Values{"_csrf": {csrf}}.Encode()))
	logout.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	logout.AddCookie(cookie)
	response := httptest.NewRecorder()
	a.Middleware(http.HandlerFunc(a.HandleLogout)).ServeHTTP(response, logout)
	if response.Code != http.StatusSeeOther {
		t.Fatal("logout failed", response.Code)
	}
	for _, ch := range []<-chan struct{}{session.stopped, provider.released, done} {
		select {
		case <-ch:
		case <-time.After(3 * time.Second):
			t.Fatal("stream or lease did not stop")
		}
	}
}
