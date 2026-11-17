package remotereveal

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/revealaudit"
	"github.com/umayr/brine/internal/targetregistry"
	"golang.org/x/crypto/ssh"
)

func testRequestID(t *testing.T) string {
	t.Helper()
	id, err := agentclient.NewRequestID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type testSigner struct {
	key    ed25519.PrivateKey
	events *[]string
}

func (s testSigner) PublicKey() ed25519.PublicKey { return s.key.Public().(ed25519.PublicKey) }
func (s testSigner) Sign(ctx context.Context, b []byte) ([]byte, error) {
	*s.events = append(*s.events, "sign")
	return ed25519.Sign(s.key, b), ctx.Err()
}

type testAudit struct {
	events                *[]string
	beginFail, finishFail bool
}

func (a testAudit) BeginRevealAudit(ctx context.Context, _, _, _ string) (revealaudit.Intent, error) {
	*a.events = append(*a.events, "intent")
	if a.beginFail {
		return revealaudit.Intent{}, errors.New("secret")
	}
	return revealaudit.Intent{Correlation: "x"}, ctx.Err()
}
func (a testAudit) Finish(ctx context.Context, _ string, _ revealaudit.Outcome) error {
	*a.events = append(*a.events, "finish")
	if a.finishFail {
		return errors.New("secret")
	}
	return ctx.Err()
}

type testSession struct {
	events *[]string
	cancel context.CancelFunc
	grant  *agentclient.Grant
}

func (s *testSession) Epoch() string { return base64.RawURLEncoding.EncodeToString(make([]byte, 32)) }
func (s *testSession) Reveal(ctx context.Context, epoch, id string, g agentclient.Grant) (agentclient.RevealResult, error) {
	*s.events = append(*s.events, "send")
	*s.grant = g
	if s.cancel != nil {
		s.cancel()
	}
	var r agentclient.RevealResult
	r.ContainerID = id
	r.Env = append(r.Env, struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}{"K", "supersecret"})
	return r, nil
}
func (s *testSession) Close() error { *s.events = append(*s.events, "close"); return nil }
func fixture(t *testing.T) (*targetregistry.Registry, targetregistry.Record, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	ap, _, _ := ed25519.GenerateKey(rand.Reader)
	ah := sha256.Sum256(ap)
	rh := sha256.Sum256(pub)
	hp, _, _ := ed25519.GenerateKey(rand.Reader)
	hk, _ := ssh.NewPublicKey(hp)
	r := targetregistry.Record{ID: "test", Revision: 1, Host: "host.example", Port: 22, User: "operator", KeyPath: "/key", HostPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hk))), HostKeySHA256: ssh.FingerprintSHA256(hk), RemoteSocket: "/agent.sock", AgentInstallationID: hex.EncodeToString(ah[:]), AgentPublicKey: base64.RawURLEncoding.EncodeToString(ap), AgentFingerprint: "SHA256:" + base64.RawStdEncoding.EncodeToString(ah[:]), EngineID: "11111111-1111-4111-8111-111111111111", EngineKind: "docker", EngineSocket: "unix:///docker.sock", DaemonID: "daemon", Generation: 1, RevealKeyFingerprint: "SHA256:" + base64.RawStdEncoding.EncodeToString(rh[:])}
	reg, err := targetregistry.Open(filepath.Join(t.TempDir(), "registry"))
	if err != nil {
		t.Fatal(err)
	}
	if err = reg.Add(r, r); err != nil {
		t.Fatal(err)
	}
	return reg, r, priv
}
func TestExecution(t *testing.T) {
	for _, tc := range []struct {
		name                                   string
		begin, finish, cancel, mismatch, wrong bool
		want                                   string
	}{{"success", false, false, false, false, false, "open,intent,sign,send,close,finish,deliver,release"}, {"intent failure", true, false, false, false, false, "open,intent,close,release"}, {"outcome failure", false, true, false, false, false, "open,intent,sign,send,close,finish,release"}, {"cancel", false, false, true, false, false, "open,intent,sign,send,close,release"}, {"target mismatch", false, false, false, true, false, "release"}, {"key mismatch", false, false, false, false, true, "release"}} {
		t.Run(tc.name, func(t *testing.T) {
			reg, r, key := fixture(t)
			events := []string{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var grant agentclient.Grant
			if tc.wrong {
				_, key, _ = ed25519.GenerateKey(rand.Reader)
			}
			e := Executor{Registry: reg, Signer: testSigner{key, &events}, Audit: testAudit{&events, tc.begin, tc.finish}, Open: func(context.Context, targetregistry.Record, ed25519.PublicKey) (Session, error) {
				events = append(events, "open")
				s := &testSession{events: &events, grant: &grant}
				if tc.cancel {
					s.cancel = cancel
				}
				return s, nil
			}}
			if tc.mismatch {
				r.Generation++
			}
			id := strings.Repeat("a", 64)
			err := e.Execute(Submission{Context: ctx, AdminID: "admin", RequestID: testRequestID(t), Release: func() { events = append(events, "release") }}, r, id, func(context.Context, agentclient.RevealResult) error { events = append(events, "deliver"); return nil })
			if (err == nil) != (tc.name == "success") {
				t.Fatalf("unexpected error %v", err)
			}
			if strings.Join(events, ",") != tc.want {
				t.Fatalf("events %v", events)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("raw error leaked")
			}
			if tc.name == "success" {
				if agentclient.VerifyGrant(key.Public().(ed25519.PublicKey), grant, r.AgentInstallationID, r.EngineID, r.Generation, grant.Epoch, id, grant.IssuedAt) != nil {
					t.Fatal("bad grant")
				}
			}
		})
	}
}
