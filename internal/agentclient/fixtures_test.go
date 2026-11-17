package agentclient

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

const fixtureEngine = "11111111-1111-4111-8111-111111111111"
const fixtureID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestRevealBytesFixture(t *testing.T) {
	g := Grant{AgentID: "56475aa75463474c0285df5dbf2bcab73da651358839e9b77481b2eab107708c", EngineID: fixtureEngine, Generation: 1, Epoch: testEpoch, ContainerID: fixtureID, SessionID: "AAAAAAAAAAAAAAAAAAAAAA", Nonce: testEpoch, IssuedAt: 1700000000, ExpiresAt: 1700000030, Scope: "env.values", Interactive: true, Signature: "qiKDNJ7emhsRke89Ewjubbj65nNxM_6M8JhH6W1YtsjSPNt9pJHTEHdmXqFHND64XPMsP7IxsouyTdPnxtz2Bw"}
	const expected = "6272696e652d6167656e742d72657665616c2f310000000040353634373561613735343633343734633032383564663564626632626361623733646136353133353838333965396237373438316232656162313037373038630000002431313131313131312d313131312d343131312d383131312d31313131313131313131313100000000000000010000002b41414141414141414141414141414141414141414141414141414141414141414141414141414141414141000000406161616161616161616161616161616161616161616161616161616161616161616161616161616161616161616161616161616161616161616161616161616100000016414141414141414141414141414141414141414141410000002b41414141414141414141414141414141414141414141414141414141414141414141414141414141414141000000006553f100000000006553f11e0000000a656e762e76616c75657301"
	m, e := GrantBytes(g)
	if e != nil || hex.EncodeToString(m) != expected {
		t.Fatal("grant bytes", e)
	}
	key, _ := base64.RawURLEncoding.DecodeString("A6EHv_POEL4dcN0Y50vAmWfk1jCbpQ1fHdyGZBJVMbg")
	if VerifyGrant(key, g, g.AgentID, g.EngineID, 1, g.Epoch, g.ContainerID, 1700000001) != nil {
		t.Fatal("signature")
	}
	g.Interactive = false
	if VerifyGrant(key, g, g.AgentID, g.EngineID, 1, g.Epoch, g.ContainerID, 1700000001) == nil {
		t.Fatal("interactive false")
	}
}
func TestSessionHTTP(t *testing.T) {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Nonce string `json:"nonce"`
		}
		if r.URL.Path != "/v1/session" || json.NewDecoder(r.Body).Decode(&q) != nil {
			t.Error("bad session request")
		}
		h := sha256.Sum256(pub)
		p := Proof{AgentID: hex.EncodeToString(h[:]), AgentPublicKey: base64.RawURLEncoding.EncodeToString(pub), EngineID: fixtureEngine, Generation: 1, EngineKind: "docker", EngineSocket: "unix:///run/docker.sock", DaemonID: "daemon", Epoch: testEpoch, Nonce: q.Nonce}
		m, e := SessionBytes(p)
		if e != nil {
			t.Error(e)
		}
		p.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, m))
		b, _ := json.Marshal(p)
		reply(w, r, string(b))
	})
	c.binding.AgentPublicKey = pub
	epoch, e := c.Session(context.Background())
	if e != nil || epoch != testEpoch {
		t.Fatal(epoch, e)
	}
}
func TestInspectPositive(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		reply(w, r, fmt.Sprintf(`{"containerId":%q,"stack":"s","service":"v","observedAt":"2025-01-01T00:00:00Z","inspect":{"id":%q,"name":"n","image":"i","imageId":"iid","state":"running","createdAt":null,"command":["run"],"user":null,"envKeys":["A","B"],"labels":{"dill.managed":"true","dill.stack":"s","dill.service":"v"},"ports":[{"protocol":"tcp","hostIp":"127.0.0.1","hostPort":80,"containerPort":80}],"mounts":[{"type":"bind","source":"/a","destination":"/b","readOnly":true}],"networks":["n"],"restartPolicy":null,"health":{"status":null,"test":[]},"resources":{"memoryBytes":null,"cpuNano":0,"pidsLimit":null},"security":{"privileged":false,"capAdd":[],"networkMode":null,"pidMode":null,"ipcMode":null}}}`, fixtureID, fixtureID))
	})
	r, e := c.Inspect(context.Background(), testEpoch, fixtureID)
	if e != nil || len(r.Inspect.EnvKeys) != 2 {
		t.Fatal(e)
	}
}
func TestStrictUnicodeAndIdentifiers(t *testing.T) {
	for _, s := range []string{`{"x":"\ud800"}`, `{"x":"\udc00"}`, `{"x":"\ud800\u0041"}`} {
		if checkJSON([]byte(s)) == nil {
			t.Fatal(s)
		}
	}
	if checkJSON([]byte(`{"x":"\ud83d\ude00"}`)) != nil {
		t.Fatal("valid pair")
	}
	for _, s := range []string{"abc", strings.Repeat("A", 64), strings.Repeat("a", 63)} {
		if fullID(s) {
			t.Fatal(s)
		}
	}
	for _, s := range []string{"unix:///a/../b", "unix:///a?x", "http:///a", "unix:///"} {
		if validSocket(s) {
			t.Fatal(s)
		}
	}
	if validUUID("engine") {
		t.Fatal("bad uuid")
	}
}
func TestTrailingStream(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		for _, d := range []string{`{"type":"end","reason":"complete","bytes":0}`, `{"type":"heartbeat","sequence":1}`} {
			fmt.Fprintf(w, "{\"version\":%q,\"requestId\":%q,\"data\":%s}\n", version, r.Header.Get("X-Request-Id"), d)
		}
	})
	if e := c.Logs(context.Background(), testEpoch, fixtureID, LogOptions{Tail: 1}, func(context.Context, LogEvent) error { return nil }); e == nil {
		t.Fatal("trailing frame")
	}
}

var _ = io.EOF
