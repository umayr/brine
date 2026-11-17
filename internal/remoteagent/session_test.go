package remoteagent

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/agenttransport"
	"github.com/umayr/brine/internal/targetregistry"
	"golang.org/x/crypto/ssh"
)

func record() targetregistry.Record {
	key := ed25519.NewKeyFromSeed(make([]byte, 32)).Public().(ed25519.PublicKey)
	h := sha256.Sum256(key)
	sk, _ := ssh.NewPublicKey(key)
	pin := ssh.FingerprintSHA256(sk)
	return targetregistry.Record{ID: "test", Revision: 1, Host: "host.example", Port: 2222, User: "operator", KeyPath: "/key", HostPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sk))), HostKeySHA256: pin, RemoteSocket: "/run/agent.sock", AgentInstallationID: hex.EncodeToString(h[:]), AgentPublicKey: base64.RawURLEncoding.EncodeToString(key), AgentFingerprint: "SHA256:" + base64.RawStdEncoding.EncodeToString(h[:]), EngineID: "11111111-1111-4111-8111-111111111111", EngineKind: "docker", EngineSocket: "unix:///run/docker.sock", DaemonID: "daemon", Generation: 1, RevealKeyFingerprint: pin}
}

type fakeForward struct {
	ctx      context.Context
	closed   atomic.Int32
	closeErr error
}

func (f *fakeForward) Context() context.Context { return f.ctx }
func (f *fakeForward) Socket() string           { return "/tmp/owned.sock" }
func (f *fakeForward) Close() error             { f.closed.Add(1); return f.closeErr }

type fakeClient struct {
	epoch  string
	err    error
	closed atomic.Int32
	pages  []agentclient.InventoryPage
	calls  int
	logs   func(context.Context) error
}

func (c *fakeClient) Session(context.Context) (string, error) { return c.epoch, c.err }
func (c *fakeClient) Close() error                            { c.closed.Add(1); return nil }
func (c *fakeClient) Inventory(_ context.Context, e string, _ *string, _ int) (agentclient.InventoryPage, error) {
	if e != c.epoch {
		panic("wrong epoch")
	}
	p := c.pages[c.calls]
	c.calls++
	return p, nil
}
func (c *fakeClient) Inspect(context.Context, string, string) (agentclient.InspectResult, error) {
	return agentclient.InspectResult{}, c.err
}
func (c *fakeClient) Logs(ctx context.Context, _ string, _ string, _ agentclient.LogOptions, _ func(context.Context, agentclient.LogEvent) error) error {
	if c.logs != nil {
		return c.logs(ctx)
	}
	return c.err
}
func TestTransportCleanupFailureIsDistinguishable(t *testing.T) {
	d := dependencies{open: func(context.Context, agenttransport.Config) (forward, error) {
		return nil, errors.Join(errors.New("transport failed"), agenttransport.ErrCleanup)
	}}
	_, err := open(context.Background(), record(), Config{}, d)
	if !errors.Is(err, ErrCleanup) {
		t.Fatal("transport cleanup not marked", err)
	}
}

func TestSetupCleanupFailureIsDistinguishable(t *testing.T) {
	f := &fakeForward{ctx: context.Background(), closeErr: errors.New("sensitive cleanup")}
	d := dependencies{open: func(context.Context, agenttransport.Config) (forward, error) { return f, nil }, newClient: func(context.Context, string, agentclient.Binding) (client, error) {
		return nil, errors.New("client failed")
	}}
	_, err := open(context.Background(), record(), Config{}, d)
	if !errors.Is(err, ErrCleanup) {
		t.Fatal("cleanup failure not marked", err)
	}
}

func TestComposition(t *testing.T) {
	r := record()
	f := &fakeForward{ctx: context.Background()}
	c := &fakeClient{epoch: "epoch"}
	var got agenttransport.Config
	d := dependencies{open: func(_ context.Context, c agenttransport.Config) (forward, error) { got = c; return f, nil }, newClient: func(ctx context.Context, s string, b agentclient.Binding) (client, error) {
		if ctx != f.ctx || s != f.Socket() || b.EngineID != r.EngineID || b.Generation != r.Generation {
			t.Fatal("wrong binding or forward")
		}
		return c, nil
	}}
	s, err := open(context.Background(), r, Config{LocalDir: "/local", Timeout: time.Second}, d)
	if err != nil {
		t.Fatal(err)
	}
	if got.Address != "host.example:2222" || got.User != r.User || got.KeyPath != r.KeyPath || got.HostPublicKey != r.HostPublicKey || got.RemoteSocket != r.RemoteSocket || got.LocalDir != "/local" || got.Timeout != time.Second {
		t.Fatalf("config: %+v", got)
	}
	next := "next"
	c.pages = []agentclient.InventoryPage{{Items: []agentclient.InventoryItem{{ID: "a"}}, NextCursor: &next}, {Items: []agentclient.InventoryItem{{ID: "b"}}}}
	items, err := s.Inventory(context.Background(), 2)
	if err != nil || len(items) != 2 {
		t.Fatal(items, err)
	}
	s.Close()
	s.Close()
	if f.closed.Load() != 1 || c.closed.Load() != 1 {
		t.Fatal("not closed once")
	}
}
func TestFailures(t *testing.T) {
	f := &fakeForward{ctx: context.Background()}
	c := &fakeClient{err: errors.New("proof failed")}
	d := dependencies{open: func(context.Context, agenttransport.Config) (forward, error) { return f, nil }, newClient: func(context.Context, string, agentclient.Binding) (client, error) { return c, nil }}
	if _, err := open(context.Background(), record(), Config{}, d); err == nil || f.closed.Load() != 1 || c.closed.Load() != 1 {
		t.Fatal("proof failure did not close")
	}
	f = &fakeForward{ctx: context.Background()}
	c = &fakeClient{epoch: "epoch"}
	s, err := open(context.Background(), record(), Config{}, d)
	if err != nil {
		t.Fatal(err)
	}
	next := "cursor"
	c.pages = []agentclient.InventoryPage{{NextCursor: &next}, {NextCursor: &next}}
	if items, err := s.Inventory(context.Background(), 2); !errors.Is(err, ErrInventoryLimit) || items != nil || f.closed.Load() != 1 {
		t.Fatal("cursor failure", items, err)
	}
}

func TestInventoryOrderingAndLimit(t *testing.T) {
	for _, id := range []string{"a", "0"} {
		t.Run(id, func(t *testing.T) {
			next := "next"
			c := &fakeClient{epoch: "epoch", pages: []agentclient.InventoryPage{{Items: []agentclient.InventoryItem{{ID: "a"}}, NextCursor: &next}, {Items: []agentclient.InventoryItem{{ID: id}}}}}
			f := &fakeForward{ctx: context.Background()}
			s := &Session{client: c, forward: f, epoch: "epoch"}
			items, err := s.Inventory(context.Background(), 2)
			if err == nil || items != nil || f.closed.Load() != 1 {
				t.Fatal("ordering accepted", items, err)
			}
		})
	}
	f := &fakeForward{ctx: context.Background()}
	c := &fakeClient{}
	s := &Session{client: c, forward: f}
	if _, err := s.Inventory(context.Background(), 0); err == nil || f.closed.Load() != 1 {
		t.Fatal("invalid limit did not retire")
	}
}
func TestObserve(t *testing.T) {
	r := record()
	f := &fakeForward{ctx: context.Background()}
	c := &fakeClient{epoch: "epoch"}
	deps := dependencies{open: func(context.Context, agenttransport.Config) (forward, error) { return f, nil }, newClient: func(context.Context, string, agentclient.Binding) (client, error) { return c, nil }}
	opener := func(ctx context.Context, r targetregistry.Record, cfg Config) (*Session, error) {
		return open(ctx, r, cfg, deps)
	}
	got, err := observe(context.Background(), r, Config{}, opener)
	if err != nil || got != r || f.closed.Load() != 1 || c.closed.Load() != 1 {
		t.Fatal("observe", got, err)
	}
	f = &fakeForward{ctx: context.Background()}
	c = &fakeClient{err: errors.New("binding mismatch")}
	got, err = observe(context.Background(), r, Config{}, opener)
	if err == nil || got != (targetregistry.Record{}) || f.closed.Load() != 1 || c.closed.Load() != 1 {
		t.Fatal("mismatch accepted", got, err)
	}
}
func TestLogsCancellation(t *testing.T) {
	f := &fakeForward{ctx: context.Background()}
	started := make(chan struct{})
	c := &fakeClient{logs: func(ctx context.Context) error { close(started); <-ctx.Done(); return ctx.Err() }}
	s := &Session{client: c, forward: f, epoch: "epoch"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Logs(ctx, "id", agentclient.LogOptions{}, nil) }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) || f.closed.Load() != 1 {
		t.Fatal("cancellation", err)
	}
}
