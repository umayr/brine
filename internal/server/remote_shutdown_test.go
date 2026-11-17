package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/config"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/remoteui"
	"github.com/umayr/brine/internal/targetregistry"
)

type blockingSession struct {
	testSession
	entered, proceed chan struct{}
}

func (s blockingSession) Inventory(ctx context.Context, _ int) ([]agentclient.InventoryItem, error) {
	close(s.entered)
	select {
	case <-s.proceed:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type blockingLease struct {
	reg      *targetregistry.Registry
	session  blockingSession
	released chan struct{}
}

func (p blockingLease) Acquire(ctx context.Context, id string) (context.Context, remoteui.ReadSession, func() error, error) {
	_, l, err := p.reg.AcquireRead(ctx, id)
	if err != nil {
		return nil, nil, nil, err
	}
	return ctx, p.session, func() error { defer close(p.released); return l.Close() }, nil
}
func TestRemoteShutdownCancelsAndDrainsActiveLease(t *testing.T) {
	cfg := config.Default()
	cfg.RemoteEnabled = true
	cfg.RemoteRegistryDir = privateTestDir(t)
	cfg.RemoteForwardDir = privateTestDir(t)
	cfg.RemoteTimeout = time.Second
	if err := targetregistry.Init(cfg.RemoteRegistryDir); err != nil {
		t.Fatal(err)
	}
	reg, err := targetregistry.OpenExisting(cfg.RemoteRegistryDir)
	if err != nil {
		t.Fatal(err)
	}
	record := syntheticRecord()
	if err := reg.Add(record, record); err != nil {
		t.Fatal(err)
	}
	a, _ := auth.New(auth.Config{User: "admin", Password: "secret"})
	s := New(cfg, agent.NewLocalAgent(nil, dill.New("dill")), nil, nil, nil, a)
	session := blockingSession{entered: make(chan struct{}), proceed: make(chan struct{})}
	released := make(chan struct{})
	if err := s.prepareRemote(blockingLease{reg, session, released}, testObserver{}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.router)
	defer ts.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Post(ts.URL+"/login", "application/x-www-form-urlencoded", strings.NewReader("user=admin&password=secret"))
	if err != nil {
		t.Fatal(err)
	}
	cookies := response.Cookies()
	response.Body.Close()
	if len(cookies) == 0 {
		t.Fatal("missing cookie")
	}
	requestDone := make(chan error, 1)
	go func() {
		req, _ := http.NewRequest("GET", ts.URL+"/remote/inventory?host=example", nil)
		req.AddCookie(cookies[0])
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		requestDone <- err
	}()
	select {
	case <-session.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not enter")
	}
	shutdownDone := make(chan error, 1)
	s.remoteCancel()
	go func() { shutdownDone <- shutdownHTTP(ts.Config, time.Second) }()
	select {
	case <-requestDone:
		// Shutdown cancellation may abort the response and the client may retry
		// against the closed listener; either outcome is permitted.
	case <-time.After(2 * time.Second):
		t.Fatal("request did not finish")
	}
	select {
	case <-released:
	case <-time.After(2 * time.Second):
		t.Fatal("lease not released")
	}
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not finish")
	}
}
