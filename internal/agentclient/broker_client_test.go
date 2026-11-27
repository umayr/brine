package agentclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/umayr/brine/internal/brokerwire"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBrokerClientPinnedEpochGrantsAndNoReplay(t *testing.T) {
	for _, profile := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "namespace-pin"}[profile], func(t *testing.T) { testBrokerClientPinnedEpoch(t, profile) })
	}
}
func testBrokerClientPinnedEpoch(t *testing.T, profile bool) {
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{3}, 32))
	pub := key.Public().(ed25519.PublicKey)
	binding := Binding{pub, fixtureEngine, 1, "docker", "unix:///run/docker.sock", "daemon"}
	monitor := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{4}, 32))
	deploy := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{5}, 32))
	digest := brokerwire.Digest([]byte("capture"))
	journal, stage, id := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)
	ref := brokerwire.Reference{Scope: brokerwire.Scope{Registration: strings.Repeat("1", 32), Stack: "demo", Engine: "docker", TargetDigest: brokerwire.Digest([]byte("dill-local-docker-v1\x00daemon")), EndpointDigest: brokerwire.Digest([]byte(binding.EngineSocket)), SourceDigest: digest, ConfigDigest: brokerwire.Digest([]byte("config")), PolicyRevision: brokerwire.Digest([]byte("policy")), Generation: strings.Repeat("2", 32), BindingRevision: 1}, Handle: strings.Repeat("3", 32)}
	if profile {
		ref.Scope.TargetDigest = brokerwire.Digest([]byte("independently-approved-namespace"))
	}
	var calls, applies atomic.Int32
	var revoked atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/session" {
			if r.Header.Get("Authorization") != "Bearer "+monitor {
				t.Error("monitor credential mixed")
				http.Error(w, "denied", 403)
				return
			}
			var q struct{ Nonce string }
			json.NewDecoder(r.Body).Decode(&q)
			p := Proof{AgentID: fmtAgentID(pub), AgentPublicKey: base64.RawURLEncoding.EncodeToString(pub), EngineID: fixtureEngine, Generation: 1, EngineKind: "docker", EngineSocket: binding.EngineSocket, DaemonID: "daemon", Epoch: testEpoch, Nonce: q.Nonce}
			raw, _ := SessionBytes(p)
			p.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, raw))
			body, _ := json.Marshal(p)
			reply(w, r, string(body))
			return
		}
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+deploy || r.Header.Get("X-Dill-Broker-Contract") != brokerwire.Version {
			t.Error("deploy boundary missing")
			http.Error(w, "denied", 403)
			return
		}
		if r.URL.Path != "/v1/broker-secrets/journal" && r.Header.Get(journalHeader) != journal {
			t.Error("journal not pinned")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(journalHeader, journal)
		switch r.URL.Path {
		case "/v1/broker-secrets/journal":
			json.NewEncoder(w).Encode(BrokerJournal{brokerwire.Version, journal, ref.Scope.TargetDigest, ref.Scope.EndpointDigest})
		case "/v1/broker-secrets/requirements/" + stage:
			json.NewEncoder(w).Encode(map[string]any{"contract": brokerwire.Version, "sourceDigest": digest, "targetDigest": ref.Scope.TargetDigest, "endpointDigest": ref.Scope.EndpointDigest, "requirements": brokerwire.Requirements{Contract: brokerwire.Version, Engine: "docker", Stack: "demo", ConfigDigest: ref.Scope.ConfigDigest, Names: []string{"password"}, Grants: []brokerwire.Grant{{Service: "web", Name: "password", Target: "/run/secrets/password", Mode: 0400}}}})
		case "/v1/broker-secrets/prepare/" + stage:
			e, err := brokerwire.Decode(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			defer e.Close()
			if len(e.Items) != 1 || string(e.Items[0].Bytes()) != "synthetic-value" {
				t.Error("wrong frozen bytes")
			}
			json.NewEncoder(w).Encode(map[string]any{"contract": brokerwire.Version, "prepared": brokerwire.Review{Scope: ref.Scope, Handle: ref.Handle, ExpiresAt: time.Now().Add(time.Minute)}})
		case "/v1/broker-secrets/apply/" + stage:
			applies.Add(1)
			http.Error(w, "lost outcome", 503)
		case "/v1/broker-secrets/operations/" + id:
			json.NewEncoder(w).Encode(BrokerStatusResult{brokerwire.Version, DeploymentStatus{RequestID: id, Stack: "demo", State: "unknown", Effects: "unknown"}, &ref})
		default:
			http.Error(w, "unexpected", 404)
		}
	}))
	defer server.Close()
	sum := sha256.Sum256(server.Certificate().Raw)
	pair := &Pairing{pin: hex.EncodeToString(sum[:]), token: monitor, binding: binding}
	target := OperatorTarget{Address: strings.TrimPrefix(server.URL, "https://"), LeafSHA256: pair.pin, AgentPublicKey: hex.EncodeToString(pub), EngineID: fixtureEngine, EngineKind: "docker", EngineSocket: binding.EngineSocket, DaemonID: "daemon", Generation: 1}
	var pins []string
	if profile {
		pins = []string{ref.Scope.TargetDigest}
	}
	client, epoch, err := pair.OpenBrokerDeploy(context.Background(), target, deploy, BrokerStatus|BrokerRequirements|BrokerPrepare|BrokerApply, func(context.Context) bool { return !revoked.Load() }, pins...)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err = client.Journal(context.Background(), epoch); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Requirements(context.Background(), epoch, journal, stage, strings.TrimPrefix(digest, "sha256:"), "demo"); err != nil {
		t.Fatal(err)
	}
	if profile {
		legacy, oldEpoch, e := pair.OpenBrokerDeploy(context.Background(), target, deploy, BrokerStatus, func(context.Context) bool { return true })
		if e != nil {
			t.Fatal(e)
		}
		if _, e = legacy.Journal(context.Background(), oldEpoch); e == nil {
			t.Fatal("broker namespace inferred from monitor or server")
		}
		legacy.Close()
	}
	item, _ := brokerwire.NewItem("password", []byte("synthetic-value"))
	e := brokerwire.Envelope{Scope: ref.Scope, Items: []brokerwire.Item{item}}
	defer e.Close()
	if _, err = client.Prepare(context.Background(), epoch, journal, stage, e); err != nil {
		t.Fatal(err)
	}
	_, err = client.Apply(context.Background(), epoch, journal, stage, id, brokerwire.Digest([]byte("selection")), "create", ref)
	if !errors.Is(err, ErrDeploymentUnknown) || applies.Load() != 1 {
		t.Fatal("ambiguous write retried or succeeded")
	}
	status, err := client.Status(context.Background(), epoch, journal, id, ref)
	if err != nil || status.Status.State != "unknown" {
		t.Fatal("unknown not preserved")
	}
	before := calls.Load()
	if _, err = client.Stage(context.Background(), epoch, []byte("{}")); err == nil || calls.Load() != before {
		t.Fatal("stage grant inherited")
	}
	revoked.Store(true)
	if _, err = client.Journal(context.Background(), epoch); err == nil || calls.Load() != before {
		t.Fatal("revoked policy reached transport")
	}
}
func fmtAgentID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}
func TestBrokerGrantsNeverAuthorizeOrdinaryMutation(t *testing.T) {
	for _, path := range []string{"/v1/deploy/apply", "/v1/deploy/journal-apply", "/v1/deploy/remove", "/v1/deploy/preview", "/v1/broker-secrets/apply/not-an-id"} {
		r := httptest.NewRequest("POST", "https://127.0.0.1"+path, nil)
		if allowedBrokerDeploy(r, brokerAll) {
			t.Fatal("unexpected grant inheritance")
		}
	}
}
