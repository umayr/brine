package agentclient

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

func TestSessionFixture(t *testing.T) {
	const nonce = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	p := Proof{Nonce: nonce, Epoch: nonce, AgentID: "56475aa75463474c0285df5dbf2bcab73da651358839e9b77481b2eab107708c", AgentPublicKey: "A6EHv_POEL4dcN0Y50vAmWfk1jCbpQ1fHdyGZBJVMbg", EngineID: "11111111-1111-4111-8111-111111111111", Generation: 1, EngineKind: "docker", EngineSocket: "unix:///run/docker.sock", DaemonID: "daemon", Signature: "TfGj3-8u5lRhmfenSvBJSfb55qc-lMICj_elS5dhX5pGPiwNovh5VZlQock1c5x1v4_Nm8sYUmi8h4PIkUayAw"}
	const want = "6272696e652d6167656e742d73657373696f6e2f31000000002b4141414141414141414141414141414141414141414141414141414141414141414141414141414141414100000040353634373561613735343633343734633032383564663564626632626361623733646136353133353838333965396237373438316232656162313037373038630000002b41364548765f504f454c3464634e3059353076416d57666b316a436270513166486479475a424a564d62670000002431313131313131312d313131312d343131312d383131312d313131313131313131313131000000000000000100000006646f636b657200000017756e69783a2f2f2f72756e2f646f636b65722e736f636b000000066461656d6f6e0000002b41414141414141414141414141414141414141414141414141414141414141414141414141414141414141"
	msg, e := SessionBytes(p)
	if e != nil || hex.EncodeToString(msg) != want {
		t.Fatal("canonical fixture mismatch", e)
	}
	key, _ := base64.RawURLEncoding.DecodeString(p.AgentPublicKey)
	b := Binding{ed25519.PublicKey(key), "11111111-1111-4111-8111-111111111111", 1, "docker", "unix:///run/docker.sock", "daemon"}
	if VerifyProof(b, p, nonce) != nil {
		t.Fatal("fixture signature rejected")
	}
	p.DaemonID = "other"
	if VerifyProof(b, p, nonce) == nil {
		t.Fatal("changed binding accepted")
	}
	p.DaemonID = "daemon"
	p.Signature = "A" + p.Signature[1:]
	if VerifyProof(b, p, nonce) == nil {
		t.Fatal("changed signature accepted")
	}
}
func TestStrictJSON(t *testing.T) {
	for _, s := range []string{`{"a":1,"a":2}`, `{"a":1,"\u0061":2}`, `{} {}`, `{"a":[{"x":1,"x":2}]}`} {
		if checkJSON([]byte(s)) == nil {
			t.Fatalf("accepted %s", s)
		}
	}
	if checkJSON([]byte(`{"a":[1,2]}`)) != nil {
		t.Fatal("rejected valid json")
	}
}
