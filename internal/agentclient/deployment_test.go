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
	const stack = "synthetic"
	notAdmitted := DeploymentStatus{RequestID: id, Stack: stack, State: "not-admitted", Effects: "none"}
	unknown := DeploymentStatus{RequestID: id, Stack: stack, State: "unknown", Effects: "unknown"}
	partial := DeploymentStatus{RequestID: id, Stack: stack, State: "failed", Effects: "partial", OperationID: "op_" + strings.Repeat("e", 32)}
	encode := func(status DeploymentStatus) string {
		t.Helper()
		b, err := json.Marshal(status)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	body := encode(notAdmitted)
	for _, tc := range []struct {
		name, responseJournal, body string
		status                      int
		duplicate, unbound          bool
		want                        DeploymentStatus
	}{
		{name: "matching not admitted", responseJournal: journal, body: body, status: http.StatusOK, want: notAdmitted},
		{name: "case state alias", responseJournal: journal, body: strings.Replace(body, `"state"`, `"State"`, 1), status: http.StatusOK},
		{name: "case request alias", responseJournal: journal, body: strings.Replace(body, `"requestId"`, `"RequestID"`, 1), status: http.StatusOK},
		{name: "case effects collision", responseJournal: journal, body: strings.TrimSuffix(body, "}") + `,"Effects":"none"}`, status: http.StatusOK},
		{name: "case operation alias", responseJournal: journal, body: strings.Replace(encode(partial), `"operationId"`, `"OperationID"`, 1), status: http.StatusOK},
		{name: "missing journal", body: body, status: http.StatusOK},
		{name: "foreign journal", responseJournal: strings.Repeat("c", 32), body: body, status: http.StatusOK},
		{name: "duplicate journal", responseJournal: journal, body: body, status: http.StatusOK, duplicate: true},
		{name: "different request", responseJournal: journal, body: strings.Replace(body, id, strings.Repeat("d", 32), 1), status: http.StatusOK},
		{name: "different stack", responseJournal: journal, body: strings.Replace(body, stack, "other", 1), status: http.StatusOK},
		{name: "not found", responseJournal: journal, body: body, status: http.StatusNotFound},
		{name: "unauthenticated", responseJournal: journal, body: body, status: http.StatusUnauthorized},
		{name: "forbidden", responseJournal: journal, body: body, status: http.StatusForbidden},
		{name: "unavailable", responseJournal: journal, body: body, status: http.StatusServiceUnavailable},
		{name: "malformed", responseJournal: journal, body: body[:len(body)-1], status: http.StatusOK},
		{name: "unknown effects", responseJournal: journal, body: encode(unknown), status: http.StatusOK, want: unknown},
		{name: "unknown effects missing journal", body: encode(unknown), status: http.StatusOK},
		{name: "partial effects", responseJournal: journal, body: encode(partial), status: http.StatusOK, want: partial},
		{name: "partial effects foreign journal", responseJournal: strings.Repeat("c", 32), body: encode(partial), status: http.StatusOK},
		{name: "partial effects duplicate journal", responseJournal: journal, body: encode(partial), status: http.StatusOK, duplicate: true},
		// The generic lookup decodes this reply without checking a journal.
		// This does not establish a tombstone on the original journal.
		{name: "generic lookup decodes not admitted without journal evidence", body: body, status: http.StatusOK, unbound: true, want: notAdmitted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				wantJournal := journal
				if tc.unbound {
					wantJournal = ""
				}
				if r.Method != http.MethodGet || r.URL.RequestURI() != "/v1/deploy/operations/"+id || r.Header.Get(journalHeader) != wantJournal || r.Body != http.NoBody {
					t.Errorf("expected a bodyless GET with the exact request and journal: %s %s", r.Method, r.URL.RequestURI())
				}
				if tc.responseJournal != "" {
					w.Header().Add(journalHeader, tc.responseJournal)
				}
				if tc.duplicate {
					w.Header().Add(journalHeader, tc.responseJournal)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			c.bearer = "synthetic"
			c.epochCtx = context.Background()
			var got DeploymentStatus
			var err error
			if tc.unbound {
				got, err = c.DeploymentStatus(context.Background(), testEpoch, id)
			} else {
				got, err = c.JournalDeploymentStatus(context.Background(), testEpoch, id, stack, journal)
			}
			var wantErr error
			if tc.want == (DeploymentStatus{}) {
				wantErr = ErrProtocol
			}
			if got != tc.want || err != wantErr {
				t.Fatalf("lookup: status=%+v err=%v; want status=%+v err=%v", got, err, tc.want, wantErr)
			}
			if calls.Load() != 1 {
				t.Fatalf("got %d requests; want exactly one lookup and no submission", calls.Load())
			}
		})
	}
}

type deploymentLookupTransport func(*http.Request) (*http.Response, error)

func (f deploymentLookupTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestJournalDeploymentStatusTransportTimeout(t *testing.T) {
	id, journal := strings.Repeat("a", 32), strings.Repeat("b", 32)
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("timeout fixture unexpectedly contacted the test server")
	})
	c.bearer = "synthetic"
	c.epochCtx = context.Background()
	calls := 0
	// Inject the transport error directly so the check never depends on timers.
	c.http.Transport = deploymentLookupTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodGet || r.URL.Path != "/v1/deploy/operations/"+id || r.Header.Get(journalHeader) != journal {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		return nil, context.DeadlineExceeded
	})
	got, err := c.JournalDeploymentStatus(context.Background(), testEpoch, id, "synthetic", journal)
	if got != (DeploymentStatus{}) || err != ErrProtocol || calls != 1 {
		t.Fatalf("timeout lookup: status=%+v err=%v calls=%d; want empty status, ErrProtocol and one GET", got, err, calls)
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
