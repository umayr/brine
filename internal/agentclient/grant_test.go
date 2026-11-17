package agentclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func bytesOf(b byte, n int) []byte { return []byte(strings.Repeat(string([]byte{b}), n)) }

func TestRevealResponseIDMismatchConsumes(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"brine.agent.read/1","requestId":"ffffffffffffffffffffffffffffffff","data":{"containerId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","env":[]}}`))
	})
	priv := ed25519.NewKeyFromSeed(make([]byte, 32))
	c.revealKey = priv.Public().(ed25519.PublicKey)
	now := uint64(time.Now().Unix())
	g := Grant{AgentID: shaAgent(c.binding.AgentPublicKey), EngineID: fixtureEngine, Generation: 1, Epoch: testEpoch, ContainerID: fixtureID, SessionID: base64.RawURLEncoding.EncodeToString(make([]byte, 16)), Nonce: testEpoch, RequestID: "0123456789abcdef0123456789abcdef", IssuedAt: now, ExpiresAt: now + 30, Scope: "env.values", Interactive: true}
	m, err := GrantBytes(g)
	if err != nil {
		t.Fatal(err)
	}
	g.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, m))
	for i := 0; i < 2; i++ {
		if _, err := c.Reveal(context.Background(), testEpoch, fixtureID, g); err == nil {
			t.Fatal("accepted mismatched response or replay")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestRevealOneShot(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("X-Request-Id") != "0123456789abcdef0123456789abcdef" {
			t.Error("reveal header not bound to grant")
		}
		reply(w, r, `{"containerId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","env":[{"key":"K","value":"secret"}]}`)
	})
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	c.revealKey = priv.Public().(ed25519.PublicKey)
	h := sha256.Sum256(c.binding.AgentPublicKey)
	now := uint64(time.Now().Unix())
	g := Grant{AgentID: hex.EncodeToString(h[:]), EngineID: "11111111-1111-4111-8111-111111111111", Generation: 1, Epoch: testEpoch, ContainerID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SessionID: base64.RawURLEncoding.EncodeToString(make([]byte, 16)), Nonce: testEpoch, RequestID: "0123456789abcdef0123456789abcdef", IssuedAt: now, ExpiresAt: now + 30, Scope: "env.values", Interactive: true}
	m, e := GrantBytes(g)
	if e != nil {
		t.Fatal(e)
	}
	g.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, m))
	for _, id := range []string{"", "0123456789ABCDEF0123456789ABCDEF", "0123456789abcdef0123456789abcde", strings.Repeat("g", 32), "fedcba9876543210fedcba9876543210"} {
		bad := g
		bad.RequestID = id
		if _, err := c.Reveal(context.Background(), testEpoch, fixtureID, bad); err == nil {
			t.Errorf("accepted request ID %q", id)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid grant reached network")
	}
	// Reconstruct the actual v1 layout: no LP(requestId), not merely a v1 domain.
	lpID := append([]byte{0, 0, 0, 32}, []byte(g.RequestID)...)
	if bytes.Count(m, lpID) != 1 {
		t.Fatal("request ID fixture is not unique")
	}
	old := bytes.Replace(m, lpID, nil, 1)
	old[len("brine-agent-reveal/")] = '1'
	legacy := g
	legacy.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, old))
	if _, err := c.Reveal(context.Background(), testEpoch, fixtureID, legacy); err == nil {
		t.Fatal("v1 layout accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("v1 grant reached network")
	}
	var wg sync.WaitGroup
	var success atomic.Int32
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := c.Reveal(context.Background(), testEpoch, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", g); e == nil {
				success.Add(1)
			}
		}()
	}
	wg.Wait()
	if success.Load() != 1 || calls.Load() != 1 {
		t.Fatal(success.Load(), calls.Load())
	}
	// A separately signed grant cannot reuse the same request ID with a new nonce.
	g.Nonce = base64.RawURLEncoding.EncodeToString(bytesOf(1, 32))
	m, _ = GrantBytes(g)
	g.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, m))
	if _, err := c.Reveal(context.Background(), testEpoch, fixtureID, g); err == nil || calls.Load() != 1 {
		t.Fatal("request ID reused")
	}
	g.ContainerID = "other"
	if VerifyGrant(c.revealKey, g, g.AgentID, g.EngineID, 1, testEpoch, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", now) == nil {
		t.Fatal("wrong target accepted")
	}
}
