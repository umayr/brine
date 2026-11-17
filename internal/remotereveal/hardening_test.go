package remotereveal

import (
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/agenttransport"
	"github.com/umayr/brine/internal/remoteagent"
	"github.com/umayr/brine/internal/revealaudit"
	"github.com/umayr/brine/internal/targetregistry"
)

var rawFailure = errors.New("secret raw failure")

type badSigner struct {
	testSigner
	cancel    context.CancelFunc
	malformed bool
}

func (s badSigner) Sign(ctx context.Context, b []byte) ([]byte, error) {
	if s.cancel != nil {
		s.cancel()
	}
	if s.malformed {
		return []byte("bad"), nil
	}
	return s.testSigner.Sign(ctx, b)
}

type faultSession struct {
	testSession
	closeErr  bool
	malformed bool
	revealErr bool
}

func (s *faultSession) Close() error {
	_ = s.testSession.Close()
	if s.closeErr {
		return rawFailure
	}
	return nil
}
func (s *faultSession) Reveal(ctx context.Context, epoch, id string, g agentclient.Grant) (agentclient.RevealResult, error) {
	r, e := s.testSession.Reveal(ctx, epoch, id, g)
	if s.malformed {
		r.ContainerID = "wrong"
	}
	if s.revealErr {
		return r, rawFailure
	}
	return r, e
}

type hookAudit struct {
	testAudit
	afterBegin, afterFinish func()
}

func (a hookAudit) BeginRevealAudit(ctx context.Context, x, y, z string) (revealaudit.Intent, error) {
	i, e := a.testAudit.BeginRevealAudit(ctx, x, y, z)
	if a.afterBegin != nil {
		a.afterBegin()
	}
	return i, e
}
func (a hookAudit) Finish(ctx context.Context, id string, o revealaudit.Outcome) error {
	e := a.testAudit.Finish(ctx, id, o)
	if a.afterFinish != nil {
		a.afterFinish()
	}
	return e
}

func TestFailurePhases(t *testing.T) {
	for _, kind := range []string{"bad signature", "bad response", "agent error", "close error", "begin and close error", "cancel before sign", "cancel signing", "cancel after audit"} {
		t.Run(kind, func(t *testing.T) {
			reg, r, key := fixture(t)
			events := []string{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var grant agentclient.Grant
			a := hookAudit{testAudit: testAudit{events: &events}}
			if kind == "begin and close error" {
				a.beginFail = true
			}
			if kind == "cancel before sign" {
				a.afterBegin = cancel
			}
			if kind == "cancel after audit" {
				a.afterFinish = cancel
			}
			signer := badSigner{testSigner: testSigner{key, &events}, malformed: kind == "bad signature"}
			if kind == "cancel signing" {
				signer.cancel = cancel
			}
			s := &faultSession{testSession: testSession{events: &events, grant: &grant}, closeErr: kind == "close error" || kind == "begin and close error", malformed: kind == "bad response", revealErr: kind == "agent error"}
			e := Executor{Registry: reg, Audit: a, Signer: signer, Open: func(context.Context, targetregistry.Record, ed25519.PublicKey) (Session, error) { return s, nil }}
			delivered := false
			err := e.Execute(Submission{ctx, func() {}, "admin", testRequestID(t)}, r, strings.Repeat("a", 64), func(context.Context, agentclient.RevealResult) error { delivered = true; return nil })
			if err == nil || delivered || strings.Contains(err.Error(), "secret") {
				t.Fatalf("err=%v delivered=%v", err, delivered)
			}
			if errors.Is(err, ErrCleanup) != (s.closeErr) {
				t.Fatalf("cleanup signal: %v", err)
			}
			if (kind == "cancel before sign" || kind == "cancel signing" || kind == "bad signature") && strings.Contains(strings.Join(events, ","), "send") {
				t.Fatal("sent after failure")
			}
		})
	}
}

type fakeForward struct{ closeErr bool }

func (f *fakeForward) Context() context.Context { return context.Background() }
func (f *fakeForward) Socket() string           { return "/fake" }
func (f *fakeForward) Close() error {
	if f.closeErr {
		return rawFailure
	}
	return nil
}

type fakeClient struct{ closeErr, sessionErr bool }

func (c *fakeClient) Session(context.Context) (string, error) {
	if c.sessionErr {
		return "", rawFailure
	}
	return "epoch", nil
}
func (c *fakeClient) Reveal(context.Context, string, string, agentclient.Grant) (agentclient.RevealResult, error) {
	return agentclient.RevealResult{}, nil
}
func (c *fakeClient) Close() error {
	if c.closeErr {
		return rawFailure
	}
	return nil
}
func TestSetupCleanup(t *testing.T) {
	reg, r, key := fixture(t)
	_ = reg
	for _, tc := range []struct {
		name                                                      string
		forwardErr, clientErr, sessionErr, fclose, cclose, marker bool
	}{{"forward cleanup marker", true, false, false, false, false, true}, {"forward error with resource", true, false, false, true, false, true}, {"client setup close", false, true, false, true, false, true}, {"client object with error", false, true, false, false, true, true}, {"handshake close", false, false, true, true, false, true}, {"handshake client close", false, false, true, false, true, true}, {"ordinary failure", false, false, true, false, false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			d := openDeps{forward: func(context.Context, agenttransport.Config) (forwarding, error) {
				f := &fakeForward{tc.fclose}
				if tc.forwardErr {
					if tc.fclose {
						return f, rawFailure
					}
					return nil, agenttransport.ErrCleanup
				}
				return f, nil
			}, client: func(context.Context, string, agentclient.Binding, ed25519.PublicKey) (revealClient, error) {
				c := &fakeClient{tc.cclose, tc.sessionErr}
				if tc.clientErr {
					return c, rawFailure
				}
				return c, nil
			}}
			_, err := pinnedOpener(remoteagent.Config{}, d)(context.Background(), r, key.Public().(ed25519.PublicKey))
			if err == nil || errors.Is(err, ErrCleanup) != tc.marker || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unexpected %v", err)
			}
		})
	}
}

func TestReadLeaseHeldThroughDelivery(t *testing.T) {
	reg, r, key := fixture(t)
	events := []string{}
	var grant agentclient.Grant
	started := make(chan struct{})
	done := make(chan error, 1)
	e := Executor{Registry: reg, Audit: testAudit{events: &events}, Signer: testSigner{key, &events}, Open: func(context.Context, targetregistry.Record, ed25519.PublicKey) (Session, error) {
		return &testSession{events: &events, grant: &grant}, nil
	}}
	err := e.Execute(Submission{context.Background(), func() {}, "admin", testRequestID(t)}, r, strings.Repeat("a", 64), func(context.Context, agentclient.RevealResult) error {
		next := r
		next.Revision++
		next.Generation++
		go func() {
			close(started)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			done <- reg.UpdateContext(ctx, r, next, targetregistry.ReviewedUpdate{Previous: r, Next: next})
		}()
		<-started
		if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("update unexpectedly succeeded: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPersistentAuditNoSecrets(t *testing.T) {
	reg, r, key := fixture(t)
	events := []string{}
	var grant agentclient.Grant
	root := filepath.Join(t.TempDir(), "audit")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	a, err := revealaudit.Initialize(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	e := Executor{Registry: reg, Audit: a, Signer: testSigner{key, &events}, Open: func(context.Context, targetregistry.Record, ed25519.PublicKey) (Session, error) {
		return &testSession{events: &events, grant: &grant}, nil
	}}
	if err = e.Execute(Submission{context.Background(), func() {}, "admin", testRequestID(t)}, r, strings.Repeat("a", 64), func(context.Context, agentclient.RevealResult) error { return nil }); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"supersecret", grant.Signature, grant.Nonce, grant.SessionID, grant.RequestID} {
			if strings.Contains(string(b), secret) {
				t.Fatalf("sensitive content in %s", entry.Name())
			}
		}
	}
}
