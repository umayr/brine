package agentmetrics

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"
)

func TestCanonicalBytesRejectsOversizeWithoutDecoding(t *testing.T) {
	oversized := strings.Repeat("A", 1<<20)
	for _, n := range []int{32, ed25519.SignatureSize} {
		if canonicalBytes(oversized, n) {
			t.Fatal("accepted oversized value")
		}
		if allocations := testing.AllocsPerRun(10, func() { canonicalBytes(oversized, n) }); allocations != 0 {
			t.Fatalf("oversized value allocated: %v allocations", allocations)
		}
	}
}

func TestProof(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := sha256.Sum256(pub)
	c := Context{OptIn: true, CallerAuthorized: true, CapabilityAuthorized: true, SessionVerified: true, AttemptActive: true, DiscoveryVerified: true, Method: "POST", Route: DiscoveryRoute, RequestID: "0123456789abcdef0123456789abcdef", AgentID: hex.EncodeToString(id[:]), EngineID: "11111111-1111-4111-8111-111111111111", Generation: 1<<53 + 1, Epoch: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), PublicKey: pub}
	body := []byte("{\"version\":\"brine.agent.read/1\"}\n")
	var p bytes.Buffer
	p.WriteString("brine-agent-monitor-response/1\x00")
	for _, s := range []string{c.Method, c.Route, c.RequestID, c.AgentID, c.EngineID} {
		_ = binary.Write(&p, binary.BigEndian, uint32(len(s)))
		p.WriteString(s)
	}
	_ = binary.Write(&p, binary.BigEndian, c.Generation)
	_ = binary.Write(&p, binary.BigEndian, uint32(len(c.Epoch)))
	p.WriteString(c.Epoch)
	h := sha256.Sum256(body)
	p.Write(h[:])
	sig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, p.Bytes()))
	got, err := verifyProof(c, []string{sig}, body)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("positive: %v", err)
	}
	got[0] = 'x'
	if body[0] == 'x' {
		t.Fatal("alias")
	}
	cases := map[string]func(*Context){"opt-in": func(x *Context) { x.OptIn = false }, "caller": func(x *Context) { x.CallerAuthorized = false }, "capability": func(x *Context) { x.CapabilityAuthorized = false }, "session": func(x *Context) { x.SessionVerified = false }, "active": func(x *Context) { x.AttemptActive = false }, "method": func(x *Context) { x.Method = "GET" }, "route": func(x *Context) { x.Route = SampleRoute }, "id": func(x *Context) { x.RequestID = "1123456789abcdef0123456789abcdef" }, "generation": func(x *Context) { x.Generation++ }, "epoch": func(x *Context) { x.Epoch = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)) }, "key-id": func(x *Context) { x.AgentID = "x" }, "engine": func(x *Context) { x.EngineID = "21111111-1111-4111-8111-111111111111" }, "key": func(x *Context) { x.PublicKey = make([]byte, 32) }, "zero generation": func(x *Context) { x.Generation = 0 }}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			x := c
			change(&x)
			if b, e := verifyProof(x, []string{sig}, body); e == nil || b != nil {
				t.Fatal("accepted")
			}
		})
	}
	for _, v := range []struct {
		s []string
		b []byte
	}{{nil, body}, {[]string{sig, sig}, body}, {[]string{sig + "="}, body}, {[]string{sig}, append(append([]byte{}, body...), ' ')}, {[]string{sig}, bytes.Repeat([]byte{'a'}, 4097)}, {[]string{sig}, []byte{0xff}}} {
		if b, e := verifyProof(c, v.s, v.b); e == nil || b != nil {
			t.Fatal("accepted bad input")
		}
	}
	if b, e := verifyProof(Context{}, []string{sig}, body); e == nil || b != nil {
		t.Fatal("default accepted")
	}
}
