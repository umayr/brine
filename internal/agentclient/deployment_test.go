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

func TestJournalDeploymentStatusRequiresMatchingEvidence(t *testing.T) {
	id, journal := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for _, tc := range []struct {
		name, responseJournal, responseID, responseStack string
		status                                           int
		duplicate, wantOK                                bool
	}{
		{name: "matching", responseJournal: journal, responseID: id, responseStack: "synthetic", status: http.StatusOK, wantOK: true},
		{name: "unavailable", responseJournal: journal, status: http.StatusServiceUnavailable},
		{name: "missing journal", responseID: id, responseStack: "synthetic", status: http.StatusOK},
		{name: "foreign journal", responseJournal: strings.Repeat("c", 32), responseID: id, responseStack: "synthetic", status: http.StatusOK},
		{name: "duplicate journal", responseJournal: journal, responseID: id, responseStack: "synthetic", status: http.StatusOK, duplicate: true},
		{name: "different request", responseJournal: journal, responseID: strings.Repeat("d", 32), responseStack: "synthetic", status: http.StatusOK},
		{name: "different stack", responseJournal: journal, responseID: id, responseStack: "other", status: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/v1/deploy/operations/"+id || r.Header.Get(journalHeader) != journal {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				if tc.responseJournal != "" {
					w.Header().Add(journalHeader, tc.responseJournal)
				}
				if tc.duplicate {
					w.Header().Add(journalHeader, tc.responseJournal)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				fmt.Fprintf(w, `{"requestId":%q,"stack":%q,"state":"not-admitted","effects":"none"}`, tc.responseID, tc.responseStack)
			})
			c.bearer = "synthetic"
			c.epochCtx = context.Background()
			got, err := c.JournalDeploymentStatus(context.Background(), testEpoch, id, "synthetic", journal)
			if tc.wantOK {
				if err != nil || got.RequestID != id || got.State != "not-admitted" {
					t.Fatalf("matching evidence: status=%+v err=%v", got, err)
				}
			} else if err != ErrProtocol || got != (DeploymentStatus{}) {
				t.Fatalf("unverified evidence accepted: status=%+v err=%v", got, err)
			}
			if calls.Load() != 1 {
				t.Fatalf("got %d requests; want exactly one lookup and no submission", calls.Load())
			}
		})
	}
}

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
