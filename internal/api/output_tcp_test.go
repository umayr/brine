package api

import (
	"bytes"
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/store"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLifecycleNonreadingTCPClient(t *testing.T) {
	st, err := store.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.Create("demo", "old"); err != nil {
		t.Fatal(err)
	}
	runner := dill.New("unused")
	h := stackHandler{store: st, agent: agent.NewLocalAgent(nil, runner), deployTimeout: 5 * time.Second}
	effect := make(chan struct{})
	joined := make(chan struct{})
	router := chi.NewRouter()
	router.Post("/api/stacks/{name}/stop", func(w http.ResponseWriter, r *http.Request) {
		defer close(joined)
		h.lifecycle(w, r, "stop", false, func(ctx context.Context, _ string, out io.Writer) error {
			chunk := bytes.Repeat([]byte("x"), 4096)
			for i := 0; i < 256; i++ {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				out.Write(chunk)
				time.Sleep(time.Millisecond)
			}
			close(effect)
			return nil
		})
	})
	server := httptest.NewUnstartedServer(router)
	server.Config.ConnState = func(c net.Conn, s http.ConnState) {
		if s == http.StateNew {
			if tcp, ok := c.(*net.TCPConn); ok {
				tcp.SetWriteBuffer(1024)
			}
		}
	}
	server.Start()
	defer server.Close()
	c, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if tcp, ok := c.(*net.TCPConn); ok {
		tcp.SetReadBuffer(1024)
	}
	c.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err = io.WriteString(c, "POST /api/stacks/demo/stop HTTP/1.1\r\nHost: synthetic\r\nContent-Length: 0\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	// Deliberately never read response headers or body.
	select {
	case <-effect:
	case <-time.After(2 * time.Second):
		t.Fatal("operation stalled behind TCP observer")
	}
	select {
	case <-joined:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not join bounded output")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err := runner.LockStackContext(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	release()
}
