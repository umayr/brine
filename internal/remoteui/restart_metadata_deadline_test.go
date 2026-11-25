package remoteui

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/umayr/brine/internal/snapshothistory"
	"github.com/umayr/brine/internal/targetregistry"
)

type restartPipeListener struct {
	conn     net.Conn
	mu       sync.Mutex
	accepted bool
	done     chan struct{}
	once     sync.Once
}

func (l *restartPipeListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if !l.accepted {
		l.accepted = true
		l.mu.Unlock()
		return l.conn, nil
	}
	l.mu.Unlock()
	<-l.done
	return nil, net.ErrClosed
}
func (l *restartPipeListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *restartPipeListener) Addr() net.Addr { return l.conn.LocalAddr() }

// net.Pipe makes backpressure deterministic without claiming TCP buffer or H2
// coverage. The real net/http response writer must honor its write deadline.
func TestRestartMetadataBlockedHTTPWriteDeadline(t *testing.T) {
	h, cookie := restartHTTPFixture(t)
	reader := restartReaderFunc(func(context.Context, snapshothistory.RestartDiscoveryRequest) (snapshothistory.RestartDiscoveryPage, error) {
		return snapshothistory.RestartDiscoveryPage{Next: "must-not-arrive-after-lease"}, nil
	})
	policy := restartMetadataAccess(func(context.Context, archiveReadActor, targetregistry.Record, targetregistry.HistoryIncarnation) error {
		return nil
	})
	handler := h.protectedRestartMetadata(reader, policy)
	finished := make(chan struct{})
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	listener := &restartPipeListener{conn: serverConn, done: make(chan struct{})}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { defer close(finished); handler.ServeHTTP(w, r) })}
	serveDone := make(chan struct{})
	go func() { defer close(serveDone); _ = server.Serve(listener) }()
	defer func() { server.Close(); serverConn.Close(); <-serveDone }()
	if err := clientConn.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(clientConn, "GET /?host=demo HTTP/1.1\r\nHost: fixture\r\nCookie: %s=%s\r\nConnection: close\r\n\r\n", cookie.Name, cookie.Value); err != nil {
		t.Fatal(err)
	}
	// No response read: even a small HTTP flush is blocked on the pipe.
	select {
	case <-finished:
	case <-time.After(4 * time.Second):
		t.Fatal("blocked write retained handler past deadline")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := h.registry.ForgetHTTPS(ctx, "demo"); err != nil {
		t.Fatalf("write failure retained enrollment lease: %v", err)
	}
	_ = clientConn.SetReadDeadline(time.Now().Add(time.Second))
	b := make([]byte, 1024)
	n, err := clientConn.Read(b)
	if n != 0 || err == nil {
		t.Fatal("buffered metadata escaped after lease release")
	}
}
