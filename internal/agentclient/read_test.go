package agentclient

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testEpoch = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func testClient(t *testing.T, h http.HandlerFunc) *Client {
	return testClientWithForward(t, context.Background(), h)
}
func testClientWithForward(t *testing.T, forward context.Context, h http.HandlerFunc) *Client {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "brine-ac-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	l, e := net.Listen("unix", sock)
	if e != nil {
		t.Fatal(e)
	}
	s := &http.Server{Handler: h}
	go s.Serve(l)
	t.Cleanup(func() { s.Close(); l.Close() })
	c, e := New(forward, sock, Binding{AgentPublicKey: make(ed25519.PublicKey, 32), EngineID: "11111111-1111-4111-8111-111111111111", Generation: 1, EngineKind: "docker", EngineSocket: "unix:///run/docker.sock", DaemonID: "daemon"})
	if e != nil {
		t.Fatal(e)
	}
	c.epoch = testEpoch
	t.Cleanup(func() { c.Close() })
	return c
}
func reply(w http.ResponseWriter, r *http.Request, data string) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"version":"brine.agent.read/1","requestId":%q,"data":%s}`, r.Header.Get("X-Request-Id"), data)
}
func TestInventoryContract(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/inventory" || r.Header.Get("X-Agent-Epoch") != testEpoch || r.Header.Get("X-Brine-Binding") != "1" {
			t.Error("wrong request")
		}
		reply(w, r, `{"items":[{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","name":"n","stack":"s","service":"v","state":"running","image":"i"}],"nextCursor":null,"observedAt":"2025-01-01T00:00:00Z"}`)
	})
	p, e := c.Inventory(context.Background(), testEpoch, nil, 1)
	if e != nil || len(p.Items) != 1 {
		t.Fatal(p, e)
	}
	if _, e = c.Inventory(context.Background(), testEpoch, nil, 0); e == nil {
		t.Fatal("bad limit")
	}
}
func TestErrorsAndLimits(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(404)
		fmt.Fprintf(w, `{"version":"brine.agent.read/1","requestId":%q,"error":{"code":"NOT_FOUND","message":"secret remote text","retryable":false}}`, r.Header.Get("X-Request-Id"))
	})
	_, e := c.Inventory(context.Background(), testEpoch, nil, 1)
	if e == nil || strings.Contains(e.Error(), "secret") {
		t.Fatal(e)
	}
	if _, ok := e.(*RemoteError); !ok {
		t.Fatal(e)
	}
}
func TestLogAndCancellation(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		id := r.Header.Get("X-Request-Id")
		for _, data := range []string{`{"type":"chunk","sequence":1,"stream":"stdout","bytesBase64":"YWJj"}`, `{"type":"end","reason":"complete","bytes":3}`} {
			b, _ := json.Marshal(map[string]any{"version": version, "requestId": id, "data": json.RawMessage(data)})
			fmt.Fprintln(w, string(b))
		}
	})
	var events []LogEvent
	e := c.Logs(context.Background(), testEpoch, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", LogOptions{Tail: 1}, func(_ context.Context, v LogEvent) error { events = append(events, v); return nil })
	if e != nil || len(events) != 2 || string(events[0].Bytes) != "abc" {
		t.Fatal(events, e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e = c.Logs(ctx, testEpoch, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", LogOptions{Tail: 1}, func(context.Context, LogEvent) error { return nil }); e != context.Canceled {
		t.Fatal(e)
	}
}
func TestIncompleteLog(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintf(w, "{\"version\":%q,\"requestId\":%q,\"data\":{\"type\":\"heartbeat\",\"sequence\":1}}\n", version, r.Header.Get("X-Request-Id"))
	})
	if e := c.Logs(context.Background(), testEpoch, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", LogOptions{Tail: 1}, func(context.Context, LogEvent) error { return nil }); e != ErrIncompleteStream {
		t.Fatal(e)
	}
}

var _ = time.Second
