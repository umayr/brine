package api

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

// An unbuffered connection makes a non-reading HTTP client deterministic:
// the real net/http writer and deadline support run without kernel buffer luck.
type observerListener struct {
	conn     net.Conn
	closed   chan struct{}
	once     sync.Once
	accepted bool
}

func (l *observerListener) Accept() (net.Conn, error) {
	if !l.accepted {
		l.accepted = true
		return l.conn, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}
func (l *observerListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *observerListener) Addr() net.Addr { return l.conn.LocalAddr() }
func TestOutputObserverNonreadingHTTPClient(t *testing.T) {
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	listener := &observerListener{conn: peer, closed: make(chan struct{})}
	effect := make(chan struct{})
	joined := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(joined)
		o, err := newOutputObserver(w)
		if err != nil {
			t.Error(err)
			return
		}
		defer o.Finish()
		o.Write(bytes.Repeat([]byte("progress"), 1024))
		close(effect)
		writeActionStatus(o, "stop", nil)
	})}
	defer server.Close()
	serving := make(chan struct{})
	go func() { defer close(serving); server.Serve(listener) }()
	client.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := io.WriteString(client, "GET / HTTP/1.1\r\nHost: synthetic\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-effect:
	case <-time.After(time.Second):
		t.Fatal("execution blocked on nonreader")
	}
	select {
	case <-joined:
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP delivery exceeded deadline")
	}
	server.Close()
	select {
	case <-serving:
	case <-time.After(time.Second):
		t.Fatal("HTTP server leaked")
	}
}
