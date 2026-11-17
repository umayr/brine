package agentclient

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestSocketReplacementCannotDivert(t *testing.T) {
	dir, e := os.MkdirTemp("/tmp", "brine-own-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s")
	l, e := net.Listen("unix", sock)
	if e != nil {
		t.Fatal(e)
	}
	seed := make([]byte, 32)
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	h := sha256.Sum256(pub)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/session" {
			var q struct {
				Nonce string `json:"nonce"`
			}
			json.NewDecoder(r.Body).Decode(&q)
			p := Proof{AgentID: hex.EncodeToString(h[:]), AgentPublicKey: base64.RawURLEncoding.EncodeToString(pub), EngineID: fixtureEngine, Generation: 1, EngineKind: "docker", EngineSocket: "unix:///run/docker.sock", DaemonID: "daemon", Epoch: testEpoch, Nonce: q.Nonce}
			m, _ := SessionBytes(p)
			p.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, m))
			b, _ := json.Marshal(p)
			reply(w, r, string(b))
			return
		}
		reply(w, r, `{"items":[],"nextCursor":null,"observedAt":"2025-01-01T00:00:00Z"}`)
	})}
	go srv.Serve(l)
	defer srv.Close()
	forward, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, e := New(forward, sock, Binding{pub, fixtureEngine, 1, "docker", "unix:///run/docker.sock", "daemon"})
	if e != nil {
		t.Fatal(e)
	}
	epoch, e := c.Session(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Rename(sock, sock+".old"); e != nil {
		t.Fatal(e)
	}
	attacker, e := net.Listen("unix", sock)
	if e != nil {
		t.Fatal(e)
	}
	var calls atomic.Int32
	bad := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "bad", 500) })}
	go bad.Serve(attacker)
	defer bad.Close()
	if _, e = c.Inventory(context.Background(), epoch, nil, 1); e != nil {
		t.Fatal("existing connection should remain pinned", e)
	}
	srv.Close()
	if _, e = c.Inventory(context.Background(), epoch, nil, 1); e == nil {
		t.Fatal("accepted dead connection")
	}
	if _, e = c.Session(context.Background()); e == nil {
		t.Fatal("reconnected without new client")
	}
	if calls.Load() != 0 {
		t.Fatal("attacker received request")
	}
}
func TestForwardLossCancelsAndJoinsSink(t *testing.T) {
	dir, e := os.MkdirTemp("/tmp", "brine-fw-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s")
	l, e := net.Listen("unix", sock)
	if e != nil {
		t.Fatal(e)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintf(w, "{\"version\":%q,\"requestId\":%q,\"data\":{\"type\":\"heartbeat\",\"sequence\":1}}\n", version, r.Header.Get("X-Request-Id"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})}
	go server.Serve(l)
	defer server.Close()
	f, cancel := context.WithCancel(context.Background())
	c, e := New(f, sock, Binding{make(ed25519.PublicKey, 32), fixtureEngine, 1, "docker", "unix:///run/docker.sock", "daemon"})
	if e != nil {
		t.Fatal(e)
	}
	c.epoch = testEpoch
	c.epochCtx, c.epochCancel = context.WithCancel(context.Background())
	started := make(chan struct{})
	joined := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- c.Logs(context.Background(), testEpoch, fixtureID, LogOptions{Tail: 1, Follow: true}, func(ctx context.Context, _ LogEvent) error {
			close(started)
			<-ctx.Done()
			close(joined)
			return ctx.Err()
		})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("sink not started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("not cancelled")
	}
	select {
	case <-joined:
	default:
		t.Fatal("returned before sink joined")
	}
	if _, e = c.Inventory(context.Background(), testEpoch, nil, 1); e == nil {
		t.Fatal("epoch survived")
	}
}
