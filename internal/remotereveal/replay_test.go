package remotereveal

import (
	"context"
	"crypto/ed25519"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/targetregistry"
)

func TestFreshGrants(t *testing.T) {
	reg, r, key := fixture(t)
	events := []string{}
	var grants []agentclient.Grant
	e := Executor{Registry: reg, Signer: testSigner{key, &events}, Audit: testAudit{events: &events}, Open: func(context.Context, targetregistry.Record, ed25519.PublicKey) (Session, error) {
		g := new(agentclient.Grant)
		return &capturingSession{testSession: testSession{events: &events, grant: g}, grants: &grants}, nil
	}}
	for i := 0; i < 2; i++ {
		if err := e.Execute(Submission{Context: context.Background(), AdminID: "admin", Release: func() {}}, r, strings.Repeat("a", 64), func(context.Context, agentclient.RevealResult) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if len(grants) != 2 || grants[0].Nonce == grants[1].Nonce || grants[0].SessionID == grants[1].SessionID || grants[0].RequestID == grants[1].RequestID {
		t.Fatal("grant identifiers reused")
	}
}

type capturingSession struct {
	testSession
	grants *[]agentclient.Grant
}

func (s *capturingSession) Reveal(ctx context.Context, epoch, id string, g agentclient.Grant) (agentclient.RevealResult, error) {
	*s.grants = append(*s.grants, g)
	return s.testSession.Reveal(ctx, epoch, id, g)
}
