package agenttransport

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

type fixture struct {
	cfg                  Config
	host                 ssh.Signer
	listener             net.Listener
	mu                   sync.Mutex
	conns                []net.Conn
	hostKey              ssh.Signer
	auth, accepts, opens atomic.Int32
	reject, stall        atomic.Bool
	channels             chan ssh.Channel
	release              chan struct{}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir, e := os.MkdirTemp("/tmp", "at-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	host, _ := makeKey(t)
	user, key := makeKey(t)
	p := filepath.Join(dir, "key")
	if e = os.WriteFile(p, key, 0600); e != nil {
		t.Fatal(e)
	}
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	x := &fixture{host: host, hostKey: host, listener: l, channels: make(chan ssh.Channel, 16), release: make(chan struct{})}
	x.cfg = Config{Address: l.Addr().String(), User: "test", KeyPath: p, HostPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(host.PublicKey()))), RemoteSocket: "/private/agent.sock", LocalDir: dir, Timeout: 300 * time.Millisecond}
	go func() {
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			x.accepts.Add(1)
			x.mu.Lock()
			x.conns = append(x.conns, c)
			h := x.hostKey
			x.mu.Unlock()
			go func() {
				sc := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
					x.auth.Add(1)
					if string(k.Marshal()) != string(user.PublicKey().Marshal()) {
						return nil, io.EOF
					}
					return nil, nil
				}}
				sc.AddHostKey(h)
				s, ch, r, e := ssh.NewServerConn(c, sc)
				if e != nil {
					return
				}
				defer s.Close()
				go ssh.DiscardRequests(r)
				for v := range ch {
					if v.ChannelType() != "direct-streamlocal@openssh.com" {
						_ = v.Reject(ssh.Prohibited, "no")
						continue
					}
					x.opens.Add(1)
					if x.reject.Load() {
						_ = v.Reject(ssh.ConnectionFailed, "socket unavailable")
						continue
					}
					if x.stall.Load() {
						<-x.release
						return
					}
					channel, req, e := v.Accept()
					if e == nil {
						go ssh.DiscardRequests(req)
						x.channels <- channel
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { l.Close(); x.disconnect(); close(x.release) })
	return x
}
func (x *fixture) disconnect() {
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, c := range x.conns {
		_ = c.Close()
	}
}
func awaitDead(t *testing.T, f *Forward) {
	t.Helper()
	select {
	case <-f.Context().Done():
	case <-time.After(3 * time.Second):
		t.Fatal("forward remained live")
	}
}
func awaitChannel(t *testing.T, x *fixture) ssh.Channel {
	t.Helper()
	select {
	case c := <-x.channels:
		return c
	case <-time.After(3 * time.Second):
		t.Fatal("channel not opened")
		return nil
	}
}
func connect(t *testing.T, f *Forward) net.Conn {
	t.Helper()
	c, e := net.Dial("unix", f.Socket())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
func TestDisconnectBeforeAccept(t *testing.T) {
	x := newFixture(t)
	f, e := Open(context.Background(), x.cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	x.disconnect()
	awaitDead(t, f)
	if x.opens.Load() != 0 {
		t.Fatal("channel opened before local acceptance")
	}
	if x.accepts.Load() != 1 {
		t.Fatal("retried SSH connection")
	}
}
func TestRemoteCloseAfterAccept(t *testing.T) {
	x := newFixture(t)
	f, e := Open(context.Background(), x.cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	c := connect(t, f)
	ch := awaitChannel(t, x)
	ch.Close()
	awaitDead(t, f)
	c.SetReadDeadline(time.Now().Add(time.Second))
	if _, e = c.Read(make([]byte, 1)); e == nil {
		t.Fatal("local read not interrupted")
	}
	if x.opens.Load() != 1 || x.accepts.Load() != 1 {
		t.Fatal("unexpected retry")
	}
}
func TestRemoteCloseAfterData(t *testing.T) {
	x := newFixture(t)
	f, e := Open(context.Background(), x.cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	c := connect(t, f)
	ch := awaitChannel(t, x)
	if _, e = ch.Write([]byte("data")); e != nil {
		t.Fatal(e)
	}
	ch.Close()
	c.SetReadDeadline(time.Now().Add(time.Second))
	b := make([]byte, 4)
	if _, e = io.ReadFull(c, b); e != nil || string(b) != "data" {
		t.Fatalf("data: %q %v", b, e)
	}
	awaitDead(t, f)
}
func TestHostKeyChange(t *testing.T) {
	x := newFixture(t)
	f, e := Open(context.Background(), x.cfg)
	if e != nil {
		t.Fatal(e)
	}
	f.Close()
	before := x.auth.Load()
	other, _ := makeKey(t)
	x.mu.Lock()
	x.hostKey = other
	x.mu.Unlock()
	if f, e := Open(context.Background(), x.cfg); e == nil {
		f.Close()
		t.Fatal("changed key accepted")
	}
	if x.auth.Load() != before {
		t.Fatal("sent auth to changed host")
	}
	if x.accepts.Load() != 2 {
		t.Fatal("unexpected retry")
	}
}
func TestParallelLocalConnectsOnlyOneChannel(t *testing.T) {
	x := newFixture(t)
	f, e := Open(context.Background(), x.cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var cs []net.Conn
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, e := net.DialTimeout("unix", f.Socket(), time.Second)
			if e == nil {
				mu.Lock()
				cs = append(cs, c)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	defer func() {
		for _, c := range cs {
			c.Close()
		}
	}()
	ch := awaitChannel(t, x)
	defer ch.Close()
	time.Sleep(100 * time.Millisecond)
	if x.opens.Load() != 1 {
		t.Fatalf("opened %d channels", x.opens.Load())
	}
	if x.accepts.Load() != 1 {
		t.Fatal("retried connection")
	}
}
func TestBlockedWriteCloses(t *testing.T) {
	x := newFixture(t)
	f, e := Open(context.Background(), x.cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	c := connect(t, f)
	ch := awaitChannel(t, x)
	defer ch.Close()
	go func() { _, _ = io.Copy(c, zeroReader{}) }()
	awaitDead(t, f)
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

// A remote socket which disappeared before local acceptance is reported by the
// deferred open; the transport does not retry either the channel or SSH.
func TestRemoteUnavailableBeforeAccept(t *testing.T) {
	x := newFixture(t)
	x.reject.Store(true)
	f, e := Open(context.Background(), x.cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if x.opens.Load() != 0 {
		t.Fatal("premature channel")
	}
	c := connect(t, f)
	_ = c
	awaitDead(t, f)
	if x.opens.Load() != 1 || x.accepts.Load() != 1 {
		t.Fatal("retried unavailable remote")
	}
}
func TestChannelOpenTimeout(t *testing.T) {
	x := newFixture(t)
	x.stall.Store(true)
	f, e := Open(context.Background(), x.cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	c := connect(t, f)
	_ = c
	awaitDead(t, f)
	if x.opens.Load() != 1 || x.accepts.Load() != 1 {
		t.Fatal("retried stalled open")
	}
}

func TestDisconnectAfterAccept(t *testing.T) {
	x := newFixture(t)
	f, e := Open(context.Background(), x.cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	c := connect(t, f)
	ch := awaitChannel(t, x)
	defer ch.Close()
	x.disconnect()
	awaitDead(t, f)
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	if _, e = c.Read(make([]byte, 1)); e == nil {
		t.Fatal("disconnect did not interrupt read")
	}
	if x.accepts.Load() != 1 || x.opens.Load() != 1 {
		t.Fatal("retried after disconnect")
	}
}

func TestHandshakeCancellationReturnsContextError(t *testing.T) {
	x := newFixture(t)
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, e := l.Accept()
		if e == nil {
			accepted <- c
		}
	}()
	cfg := x.cfg
	cfg.Address = l.Addr().String()
	cfg.Timeout = 5 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		f, e := Open(ctx, cfg)
		if f != nil {
			f.Close()
		}
		result <- e
	}()
	c := <-accepted
	defer c.Close()
	cancel()
	select {
	case e := <-result:
		if !errors.Is(e, context.Canceled) {
			t.Fatalf("got %v, want context canceled", e)
		}
	case <-time.After(time.Second):
		t.Fatal("handshake not canceled")
	}
}
func TestCloseReturnsCleanupErrorConcurrently(t *testing.T) {
	x := newFixture(t)
	f, e := Open(context.Background(), x.cfg)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(f.dir, "leftover"), []byte("x"), 0600); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- f.Close() }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e == nil || !strings.Contains(e.Error(), "remove ") {
			t.Fatalf("missing cleanup error: %v", e)
		}
	}
}

func TestLocalDisconnectDuringStalledOpen(t *testing.T) {
	x := newFixture(t)
	x.stall.Store(true)
	x.cfg.Timeout = 800 * time.Millisecond
	f, e := Open(context.Background(), x.cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	c := connect(t, f)
	deadline := time.Now().Add(2 * time.Second)
	for x.opens.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("channel open not received")
		}
		time.Sleep(time.Millisecond)
	}
	if e = c.Close(); e != nil {
		t.Fatal(e)
	}
	select {
	case <-f.Context().Done():
		t.Fatal("unexpected cancellation while channel open remains stalled")
	case <-time.After(50 * time.Millisecond):
	}
	awaitDead(t, f)
	if x.opens.Load() != 1 || x.accepts.Load() != 1 {
		t.Fatal("retried stalled open")
	}
}
