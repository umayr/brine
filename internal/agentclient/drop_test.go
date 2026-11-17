package agentclient

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestRevealDroppedResponseConsumes(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		conn, _, e := w.(http.Hijacker).Hijack()
		if e == nil {
			conn.Close()
		}
	})
	priv := ed25519.NewKeyFromSeed(make([]byte, 32))
	c.revealKey = priv.Public().(ed25519.PublicKey)
	now := uint64(time.Now().Unix())
	g := Grant{AgentID: shaAgent(c.binding.AgentPublicKey), EngineID: fixtureEngine, Generation: 1, Epoch: testEpoch, ContainerID: fixtureID, SessionID: "AAAAAAAAAAAAAAAAAAAAAA", Nonce: testEpoch, IssuedAt: now, ExpiresAt: now + 30, Scope: "env.values", Interactive: true}
	b, _ := GrantBytes(g)
	g.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, b))
	for i := 0; i < 2; i++ {
		if _, e := c.Reveal(context.Background(), testEpoch, fixtureID, g); e == nil {
			t.Fatal("expected failure")
		}
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
}
