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

func TestPairingStrictAndRedacted(t *testing.T) {
	token := strings.Repeat("A", 43)
	bundle := `{"version":1,"certificateSha256":"` + strings.Repeat("a", 64) + `","token":"` + token + `","agentPublicKey":"` + strings.Repeat("b", 64) + `","engineId":"01234567-89ab-cdef-0123-456789abcdef","generation":1,"engineKind":"docker","engineSocket":"unix:///var/run/docker.sock","daemonId":"daemon"}`
	p, err := ParsePairing([]byte(bundle))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{fmt.Sprint(p), fmt.Sprintf("%#v", p), fmt.Sprintf("%+v", p)} {
		if strings.Contains(s, token) {
			t.Fatal("credential formatted")
		}
	}
	if b, e := json.Marshal(p); e == nil || strings.Contains(string(b), token) {
		t.Fatal("candidate serialized")
	}
	invalid := []string{
		strings.Replace(bundle, `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(bundle, `"version":1`, `"version":1,"other":1`, 1),
		strings.Replace(bundle, `"version":1`, `"version":1.0`, 1),
		strings.Replace(bundle, `"generation":1`, `"generation":0`, 1),
		strings.Replace(bundle, `"engineKind":"docker"`, `"engineKind":"podman"`, 1),
		strings.Replace(bundle, token, token+"=", 1),
		strings.Replace(bundle, strings.Repeat("b", 64), strings.Repeat("B", 64), 1),
		strings.Replace(bundle, `unix:///var/run/docker.sock`, `unix:///var/../docker.sock`, 1),
		strings.Replace(bundle, `,"daemonId":"daemon"`, ``, 1),
		bundle + ` {}`,
	}
	for i, s := range invalid {
		if _, e := ParsePairing([]byte(s)); e != ErrProtocol {
			t.Errorf("case %d: %v", i, e)
		}
	}
	if _, e := p.Open(context.Background(), context.Background(), "8.8.8.8:443"); e != ErrProtocol {
		t.Fatal("public endpoint accepted")
	}
}

func TestPairingOpenRequiresPinAndSignedBinding(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, 32))
	pub := priv.Public().(ed25519.PublicKey)
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	var calls atomic.Int32
	var wrong atomic.Bool
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+token || r.URL.Path != "/v1/session" {
			t.Error("bad request")
		}
		var q struct {
			Nonce string `json:"nonce"`
		}
		if json.NewDecoder(r.Body).Decode(&q) != nil {
			t.Error("bad body")
			return
		}
		h := sha256.Sum256(pub)
		p := Proof{AgentID: hex.EncodeToString(h[:]), AgentPublicKey: base64.RawURLEncoding.EncodeToString(pub), EngineID: fixtureEngine, Generation: 1, EngineKind: "docker", EngineSocket: "unix:///run/docker.sock", DaemonID: "daemon", Epoch: testEpoch, Nonce: q.Nonce}
		if wrong.Load() {
			p.DaemonID = "other"
		}
		msg, _ := SessionBytes(p)
		p.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, msg))
		b, _ := json.Marshal(p)
		reply(w, r, string(b))
	}))
	defer s.Close()
	sum := sha256.Sum256(s.Certificate().Raw)
	bundle := fmt.Sprintf(`{"version":1,"certificateSha256":%q,"token":%q,"agentPublicKey":%q,"engineId":%q,"generation":1,"engineKind":"docker","engineSocket":"unix:///run/docker.sock","daemonId":"daemon"}`, hex.EncodeToString(sum[:]), token, hex.EncodeToString(pub), fixtureEngine)
	p, e := ParsePairing([]byte(bundle))
	if e != nil {
		t.Fatal(e)
	}
	addr := strings.TrimPrefix(s.URL, "https://")
	bad, _ := ParsePairing([]byte(strings.Replace(bundle, hex.EncodeToString(sum[:]), strings.Repeat("0", 64), 1)))
	if _, e = bad.Open(context.Background(), context.Background(), addr); e == nil || calls.Load() != 0 {
		t.Fatal("pin failure sent credential")
	}
	wrong.Store(true)
	if _, e = p.Open(context.Background(), context.Background(), addr); e == nil {
		t.Fatal("accepted wrong binding")
	}
	wrong.Store(false)
	c, e := p.Open(context.Background(), context.Background(), addr)
	if e != nil {
		t.Fatal(e)
	}
	c.Close()
}
