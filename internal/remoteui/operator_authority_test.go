package remoteui

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/targetregistry"
)

type operatorSourceFixture struct {
	approval operatorGrantApproval
	token    string
	revoked  bool
}

func (s *operatorSourceFixture) LoadCurrent(_ context.Context, _ targetregistry.HistoryIncarnation) (operatorGrantApproval, string, error) {
	if s.revoked {
		return operatorGrantApproval{}, "", errors.New("generated infrastructure denial")
	}
	return s.approval, s.token, nil
}

func TestOperatorDeployAuthoritySeparatesCredentialsAndFailsClosed(t *testing.T) {
	ctx := context.Background()
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{11}, 32))
	pub := priv.Public().(ed25519.PublicKey)
	const engine, socket, daemon = "11111111-1111-4111-8111-111111111111", "unix:///run/docker.sock", "synthetic"
	monitor := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{21}, 32))
	deploy := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{22}, 32))
	var monitorCalls, deployCalls, escapedCalls atomic.Int32
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { escapedCalls.Add(1) }))
	defer redirect.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/session":
			monitorCalls.Add(1)
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+monitor {
				t.Error("monitor received wrong authority")
				w.WriteHeader(401)
				return
			}
			var q struct{ Nonce string }
			if json.NewDecoder(r.Body).Decode(&q) != nil {
				t.Error("invalid challenge")
				return
			}
			p := agentclient.Proof{AgentID: fmt.Sprintf("%x", sha256.Sum256(pub)), AgentPublicKey: base64.RawURLEncoding.EncodeToString(pub), EngineID: engine, Generation: 1, EngineKind: "docker", EngineSocket: socket, DaemonID: daemon, Epoch: strings.Repeat("A", 43), Nonce: q.Nonce}
			msg, err := agentclient.SessionBytes(p)
			if err != nil {
				t.Error(err)
				return
			}
			p.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, msg))
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "brine.agent.read/1", "requestId": r.Header.Get("X-Request-Id"), "data": p})
		case "/v1/deploy/journal-apply":
			deployCalls.Add(1)
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+deploy || r.Header.Get("X-Dill-Journal-Id") != attendedJournal {
				t.Error("deploy received wrong authority or journal")
				w.WriteHeader(401)
				return
			}
			http.Redirect(w, r, redirect.URL, http.StatusTemporaryRedirect)
		default:
			t.Error("unexpected call outside local grant ceiling")
			w.WriteHeader(404)
		}
	}))
	defer origin.Close()
	pin, address := fmt.Sprintf("%x", sha256.Sum256(origin.Certificate().Raw)), strings.TrimPrefix(origin.URL, "https://")
	row := targetregistry.Record{ID: "generated", Revision: 1, HTTPSAddress: address, HTTPSPairing: targetregistry.PairingSecret(fmt.Sprintf(`{"version":1,"certificateSha256":%q,"token":%q,"agentPublicKey":"%x","engineId":%q,"generation":1,"engineKind":"docker","engineSocket":%q,"daemonId":%q}`, pin, monitor, pub, engine, socket, daemon))}
	active := targetregistry.HistoryIncarnation{1}
	base := operatorGrantApproval{Enrollment: active, Target: agentclient.OperatorTarget{Address: address, LeafSHA256: pin, AgentPublicKey: fmt.Sprintf("%x", pub), EngineID: engine, EngineKind: "docker", EngineSocket: socket, DaemonID: daemon, Generation: 1}, LocalGrants: agentclient.LocalNewAttended, Reference: strings.Repeat("1", 64), Revision: strings.Repeat("2", 64), TokenCommitment: sha256.Sum256([]byte(deploy))}
	source := &operatorSourceFixture{approval: base, token: deploy}
	a := &operatorDeployAuthority{source: source}
	for _, tc := range []struct {
		name   string
		change func(*operatorSourceFixture)
	}{
		{"other enrollment", func(s *operatorSourceFixture) { s.approval.Enrollment = targetregistry.HistoryIncarnation{2} }},
		{"other pin", func(s *operatorSourceFixture) { s.approval.Target.LeafSHA256 = strings.Repeat("f", 64) }},
		{"other engine generation", func(s *operatorSourceFixture) { s.approval.Target.Generation++ }},
		{"other origin", func(s *operatorSourceFixture) { s.approval.Target.Address = "127.0.0.1:1" }},
		{"missing grant", func(s *operatorSourceFixture) { s.approval.LocalGrants &^= agentclient.LocalJournalApply }},
		{"token replaced under same revision", func(s *operatorSourceFixture) {
			s.token = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{23}, 32))
		}},
		{"monitor token alias", func(s *operatorSourceFixture) {
			s.token = monitor
			s.approval.TokenCommitment = sha256.Sum256([]byte(monitor))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			*source = operatorSourceFixture{approval: base, token: deploy}
			tc.change(source)
			if c, err := a.connect(ctx, row, active); err != errOperatorAuthority || c.client != nil {
				t.Fatal("mismatch admitted")
			}
			if monitorCalls.Load() != 0 || deployCalls.Load() != 0 {
				t.Fatal("mismatch contacted an origin")
			}
		})
	}
	*source = operatorSourceFixture{approval: base, token: deploy}
	c, err := a.connect(ctx, row, active)
	if err != nil {
		t.Fatal(err)
	}
	defer c.client.Close()
	apply := func() (agentclient.DeploymentStatus, error) {
		return c.client.ApplyJournalDeployment(ctx, c.epoch, attendedStage, strings.Repeat("a", 64), "synthetic", attendedSelection, strings.Repeat("b", 32), "create", attendedJournal)
	}
	// Local approval is not provider authorization. The generated provider's
	// redirect is not followed and does not become outcome evidence.
	if got, err := apply(); got != (agentclient.DeploymentStatus{}) || err == nil || monitorCalls.Load() != 1 || deployCalls.Load() != 1 || escapedCalls.Load() != 0 {
		t.Fatalf("unexpected split or redirect behavior: %v", err)
	}
	source.approval.Revision = strings.Repeat("3", 64)
	if got, err := apply(); got != (agentclient.DeploymentStatus{}) || err == nil || deployCalls.Load() != 1 {
		t.Fatal("stale connected client sent after revision replacement")
	}
	if c.binding.scope != fmt.Sprintf("%x", active) || c.binding.target != targetDigest(row) {
		t.Fatal("connection was not scoped to exact active enrollment")
	}
}
