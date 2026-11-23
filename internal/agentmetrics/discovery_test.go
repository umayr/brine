package agentmetrics

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestDiscoveryKAT(t *testing.T) {
	var k struct {
		PublicKeyHex, AgentID, EngineID, Epoch string
		Vectors                                []struct{ BodyHex, Signature, RequestID string }
	}
	b, e := os.ReadFile("testdata/aggregate-signing-candidate-kat.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &k); e != nil {
		t.Fatal(e)
	}
	key, _ := hex.DecodeString(k.PublicKeyHex)
	v := k.Vectors[0]
	body, _ := hex.DecodeString(v.BodyHex)
	c := Context{OptIn: true, CallerAuthorized: true, SessionVerified: true, AttemptActive: true, Method: "POST", Route: DiscoveryRoute, RequestID: v.RequestID, AgentID: k.AgentID, EngineID: k.EngineID, Epoch: k.Epoch, Generation: 1, PublicKey: key}
	a := NewDiscoveryAttempt(c, "target", "session")
	p, e := a.Consume(200, []string{v.Signature}, body)
	if e != nil || !p.Matches(c, "target", "session", true) {
		t.Fatalf("positive %v", e)
	}
	if _, e = a.Consume(200, []string{v.Signature}, body); e == nil {
		t.Fatal("replay")
	}
	if p.Matches(c, "other", "session", true) || p.Matches(c, "target", "session", false) || p.Matches(Context{}, "target", "session", true) {
		t.Fatal("wrong context")
	}
	sample := c
	sample.Route = SampleRoute
	sample.RequestID = "00000000000000000000000000000002"
	active := NewSampleAttempt(sample, p, "target", "session", true)
	if !active.ctx.AttemptActive {
		t.Fatal("matching signed discovery rejected")
	}
	for _, tc := range []struct {
		name   string
		change func(*Context)
	}{
		{"local-opt-out", func(c *Context) { c.OptIn = false }},
		{"ACL-revoked", func(c *Context) { c.CallerAuthorized = false }},
		{"session-unverified", func(c *Context) { c.SessionVerified = false }},
		{"epoch-changed", func(c *Context) { c.Epoch = "changed" }},
		{"binding-generation-changed", func(c *Context) { c.Generation++ }},
		{"key-changed", func(c *Context) { c.PublicKey = make([]byte, 32) }},
		{"engine-changed", func(c *Context) { c.EngineID = "different" }},
		{"agent-changed", func(c *Context) { c.AgentID = "different" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := sample
			tc.change(&changed)
			if NewSampleAttempt(changed, p, "target", "session", true).ctx.AttemptActive {
				t.Fatal("prior discovery allowed a new sample after scope/authority changed")
			}
		})
	}
	if NewSampleAttempt(sample, p, "target", "re-established-session", true).ctx.AttemptActive {
		t.Fatal("prior discovery survived local session replacement")
	}
	active.Invalidate()
	if active.ctx.AttemptActive {
		t.Fatal("existing receipt slot survived caller invalidation")
	}
	for _, q := range []DiscoveryProof{{}, p} {
		if NewSampleAttempt(sample, q, "foreign", "session", true).ctx.AttemptActive {
			t.Fatal("foreign proof accepted")
		}
	}
	if NewSampleAttempt(sample, DiscoveryProof{}, "target", "session", true).ctx.AttemptActive || NewSampleAttempt(sample, p, "target", "session", false).ctx.AttemptActive {
		t.Fatal("missing or stale proof accepted")
	}
	for _, change := range []func(*Context){func(x *Context) { x.Route = SampleRoute }, func(x *Context) { x.Epoch = "wrong" }, func(x *Context) { x.PublicKey = make([]byte, 32) }, func(x *Context) { x.OptIn = false }, func(x *Context) { x.SessionVerified = false }, func(x *Context) { x.AttemptActive = false }} {
		x := c
		change(&x)
		if _, e := NewDiscoveryAttempt(x, "target", "session").Consume(200, []string{v.Signature}, body); e == nil {
			t.Fatal("bad context accepted")
		}
	}
	if _, e := NewDiscoveryAttempt(c, "target", "session").Consume(200, []string{v.Signature, v.Signature}, body); e == nil {
		t.Fatal("duplicate proof")
	}
	a = NewDiscoveryAttempt(c, "target", "session")
	a.Invalidate()
	if _, e = a.Consume(200, []string{v.Signature}, body); e == nil {
		t.Fatal("inactive")
	}
}
func TestSignedUnavailable(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(pub)
	c := Context{OptIn: true, CallerAuthorized: true, SessionVerified: true, AttemptActive: true, Method: "POST", Route: DiscoveryRoute, RequestID: "00000000000000000000000000000001", AgentID: hex.EncodeToString(h[:]), EngineID: "11111111-1111-4111-8111-111111111111", Epoch: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), Generation: 1, PublicKey: pub}
	body := []byte(`{"version":"brine.agent.read/1","requestId":"` + c.RequestID + `","data":{"capabilities":[]}}`)
	pre := []byte("brine-agent-monitor-response/1\x00")
	for _, s := range []string{c.Method, c.Route, c.RequestID, c.AgentID, c.EngineID} {
		pre = appendString(pre, s)
	}
	pre = binary.BigEndian.AppendUint64(pre, c.Generation)
	pre = appendString(pre, c.Epoch)
	digest := sha256.Sum256(body)
	pre = append(pre, digest[:]...)
	sig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, pre))
	proof, e := NewDiscoveryAttempt(c, "target", "session").Consume(200, []string{sig}, body)
	if e != ErrUnavailable || proof.Matches(c, "target", "session", true) {
		t.Fatal("signed absence must be unavailable")
	}
}

func TestDiscoveryStrict(t *testing.T) {
	makeBody := func(c string) []byte {
		return []byte(`{"version":"brine.agent.read/1","requestId":"id","data":{"capabilities":` + c + `}}`)
	}
	good := `["brine.agent.metrics.aggregate/1"]`
	if e := decodeDiscovery(makeBody(good), "id"); e != nil {
		t.Fatal(e)
	}
	for _, s := range []string{`[]`, `["brine.agent.metrics.aggregate/2"]`} {
		if decodeDiscovery(makeBody(s), "id") != ErrUnavailable {
			t.Fatal("expected unavailable")
		}
	}
	for _, s := range []string{`["brine.agent.metrics.aggregate/1","brine.agent.metrics.aggregate/1"]`, `["brine.agent.metrics.aggregate/1","BAD"]`, `null`, `["brine.agent.metrics.aggregate/1",` + strings.Repeat(`"x",`, 7) + `"z"]`} {
		if decodeDiscovery(makeBody(s), "id") == nil {
			t.Fatalf("accepted %s", s)
		}
	}
	if decodeDiscovery([]byte(`{"version":"brine.agent.read/1","version":"brine.agent.read/1","requestId":"id","data":{"capabilities":`+good+`}}`), "id") == nil {
		t.Fatal("duplicate key")
	}
	if decodeDiscovery(append(makeBody(good), []byte(strings.Repeat(" ", 4096))...), "id") == nil {
		t.Fatal("oversize")
	}
}
