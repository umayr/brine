package remotereveal

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/agenttransport"
	"github.com/umayr/brine/internal/remoteagent"
	"github.com/umayr/brine/internal/targetregistry"
)

func TestOpenFailureClosesReturnedSession(t *testing.T) {
	reg, r, key := fixture(t)
	events := []string{}
	var grant agentclient.Grant
	e := Executor{Registry: reg, Audit: testAudit{events: &events}, Signer: testSigner{key, &events}, Open: func(context.Context, targetregistry.Record, ed25519.PublicKey) (Session, error) {
		return &faultSession{testSession: testSession{events: &events, grant: &grant}, closeErr: true}, rawFailure
	}}
	err := e.Execute(Submission{context.Background(), func() {}, "admin", testRequestID(t)}, r, strings.Repeat("a", 64), func(context.Context, agentclient.RevealResult) error { t.Fatal("delivery"); return nil })
	if !errors.Is(err, ErrCleanup) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("%v", err)
	}
	if strings.Join(events, ",") != "close" {
		t.Fatal(events)
	}
}
func TestTransportCleanupCauseSanitizedAtExecutor(t *testing.T) {
	reg, r, key := fixture(t)
	events := []string{}
	d := openDeps{forward: func(context.Context, agenttransport.Config) (forwarding, error) {
		return nil, errors.Join(agenttransport.ErrCleanup, rawFailure)
	}}
	e := Executor{Registry: reg, Audit: testAudit{events: &events}, Signer: testSigner{key, &events}, Open: pinnedOpener(remoteagent.Config{}, d)}
	err := e.Execute(Submission{context.Background(), func() {}, "admin", testRequestID(t)}, r, strings.Repeat("a", 64), func(context.Context, agentclient.RevealResult) error { t.Fatal("delivery"); return nil })
	if !errors.Is(err, ErrCleanup) || errors.Is(err, rawFailure) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAlreadyCanceled(t *testing.T) {
	reg, r, key := fixture(t)
	events := []string{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	released := false
	e := Executor{Registry: reg, Audit: testAudit{events: &events}, Signer: testSigner{key, &events}, Open: func(context.Context, targetregistry.Record, ed25519.PublicKey) (Session, error) {
		t.Fatal("open")
		return nil, nil
	}}
	err := e.Execute(Submission{ctx, func() { released = true }, "admin", testRequestID(t)}, r, strings.Repeat("a", 64), func(context.Context, agentclient.RevealResult) error { t.Fatal("delivery"); return nil })
	if err == nil || !released {
		t.Fatal("did not deny and release")
	}
}
