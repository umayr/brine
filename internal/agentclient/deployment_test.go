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
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/umayr/brine/internal/dill"
)

func TestHTTPSPreviewVerifiesReceiptTarget(t *testing.T) {
	golden, err := os.ReadFile("../dill/testdata/preflight-v1/preview-up-create.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct{ Stdout json.RawMessage }
	if err := json.Unmarshal(golden, &fixture); err != nil {
		t.Fatal(err)
	}
	result, err := dill.DecodePreflightResult(fixture.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	const socket = "unix:///run/example.sock"
	wantDigest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(socket)))
	good := bytes.ReplaceAll(fixture.Stdout, []byte(result.Target.EndpointDigest), []byte(wantDigest))
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, 32))
	pub := priv.Public().(ed25519.PublicKey)
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	var mismatch atomic.Bool
	var calls atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("missing credential")
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/v1/session":
			var q struct{ Nonce string }
			if json.NewDecoder(r.Body).Decode(&q) != nil {
				t.Error("missing nonce")
				return
			}
			p := Proof{AgentID: fmt.Sprintf("%x", sha256.Sum256(pub)), AgentPublicKey: base64.RawURLEncoding.EncodeToString(pub), EngineID: fixtureEngine, Generation: 1, EngineKind: "docker", EngineSocket: socket, DaemonID: "daemon", Epoch: testEpoch, Nonce: q.Nonce}
			msg, _ := SessionBytes(p)
			p.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, msg))
			wire, _ := json.Marshal(p)
			reply(w, r, string(wire))
		case "/v1/deploy/preview":
			calls.Add(1)
			var fields map[string]string
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&fields) != nil || len(fields) != 3 || fields["stageId"] != strings.Repeat("1", 32) || fields["manifestSha256"] != strings.Repeat("a", 64) || fields["stack"] != result.Stack {
				t.Error("preview did not carry exact reviewed reference")
			}
			w.Header().Set("Content-Type", "application/json")
			if mismatch.Load() {
				_, _ = w.Write(fixture.Stdout)
				return
			}
			_, _ = w.Write(good)
		default:
			t.Error("unexpected route")
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	pin := sha256.Sum256(s.Certificate().Raw)
	ctx := context.Background()
	client, err := NewHTTPS(ctx, strings.TrimPrefix(s.URL, "https://"), hex.EncodeToString(pin[:]), token, Binding{pub, fixtureEngine, 1, "docker", socket, "daemon"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	epoch, err := client.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stage, digest := strings.Repeat("1", 32), strings.Repeat("a", 64)
	if _, err := client.PreviewDeployment(ctx, "wrong", stage, digest, result.Stack); err != ErrProtocol || calls.Load() != 0 {
		t.Fatal("preview submitted without a valid session")
	}
	if _, err := client.PreviewDeployment(ctx, epoch, stage, digest, result.Stack); err != nil {
		t.Fatal(err)
	}
	mismatch.Store(true)
	if _, err := client.PreviewDeployment(ctx, epoch, stage, digest, result.Stack); err != ErrProtocol || calls.Load() != 2 {
		t.Fatal("preview accepted a different engine or replayed")
	}
}
