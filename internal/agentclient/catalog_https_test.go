package agentclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHTTPSCatalogVerifiedSession(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, 32))
	pub := priv.Public().(ed25519.PublicKey)
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	const socket = "unix:///run/example.sock"
	id := strings.Repeat("1", 32)
	raw := `{"schema":"dill.snapshot.manifest.v1","entrypoint":"main.pkl","files":[{"path":"main.pkl","length":3,"sha256":"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"}]}`
	digest := sha256.Sum256([]byte(raw))
	var calls atomic.Int32
	var bad atomic.Bool
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("missing bearer")
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/session" {
			var q struct{ Nonce string }
			_ = json.NewDecoder(r.Body).Decode(&q)
			p := Proof{AgentID: fmt.Sprintf("%x", sha256.Sum256(pub)), AgentPublicKey: base64.RawURLEncoding.EncodeToString(pub), EngineID: fixtureEngine, Generation: 1, EngineKind: "docker", EngineSocket: socket, DaemonID: "daemon", Epoch: testEpoch, Nonce: q.Nonce}
			msg, _ := SessionBytes(p)
			p.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, msg))
			wire, _ := json.Marshal(p)
			reply(w, r, string(wire))
			return
		}
		if r.URL.Path == "/v1/capabilities" {
			if r.Method != http.MethodGet {
				t.Error("capabilities used mutation method")
			}
			fmt.Fprint(w, `{"schema":"dill.agent.capabilities.v1","monitor":["session","capabilities","inventory"],"deployment_grants":[]}`)
			return
		}
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/v1/snapshots/"+id {
			t.Error("wrong request")
		}
		hash := fmt.Sprintf("%x", digest)
		if bad.Load() {
			hash = strings.Repeat("0", 64)
		}
		fmt.Fprintf(w, `{"stage_id":"%s","manifest":%s,"manifest_sha256":"%s"}`, id, raw, hash)
	}))
	defer s.Close()
	pin := sha256.Sum256(s.Certificate().Raw)
	ctx := context.Background()
	c, err := NewHTTPS(ctx, strings.TrimPrefix(s.URL, "https://"), hex.EncodeToString(pin[:]), token, Binding{pub, fixtureEngine, 1, "docker", socket, "daemon"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	epoch, err := c.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, e := c.Capabilities(ctx, "wrong-epoch"); e == nil {
		t.Fatal("capabilities accepted stale proof")
	}
	caps, e := c.Capabilities(ctx, epoch)
	if e != nil || len(caps) != 3 {
		t.Fatal("verified capability report unavailable", caps, e)
	}
	if _, err = c.Snapshot(ctx, "bad", id); err != ErrProtocol || calls.Load() != 0 {
		t.Fatal("accepted invalid session")
	}
	m, err := c.Snapshot(ctx, epoch, id)
	if err != nil || m.Digest() != digest || m.Entrypoint() != "main.pkl" {
		t.Fatalf("catalog: %v", err)
	}
	bad.Store(true)
	if _, err = c.Snapshot(ctx, epoch, id); err != ErrProtocol || calls.Load() != 2 {
		t.Fatal("accepted bad digest or retried")
	}
}
