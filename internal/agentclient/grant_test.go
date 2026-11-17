package agentclient

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRevealOneShot(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
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
	g := Grant{AgentID: hex.EncodeToString(h[:]), EngineID: "11111111-1111-4111-8111-111111111111", Generation: 1, Epoch: testEpoch, ContainerID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SessionID: base64.RawURLEncoding.EncodeToString(make([]byte, 16)), Nonce: testEpoch, IssuedAt: now, ExpiresAt: now + 30, Scope: "env.values", Interactive: true}
	m, e := GrantBytes(g)
	if e != nil {
		t.Fatal(e)
	}
	g.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, m))
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
	g.ContainerID = "other"
	if VerifyGrant(c.revealKey, g, g.AgentID, g.EngineID, 1, testEpoch, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", now) == nil {
		t.Fatal("wrong target accepted")
	}
}
