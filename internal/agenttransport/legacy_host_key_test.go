package agenttransport

import (
	"crypto/dsa"
	"math/big"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestPinnedRejectsLegacyDSAAlgorithm(t *testing.T) {
	// Encoding-only fixture, not cryptographic parameters used for signing.
	public, err := ssh.NewPublicKey(&dsa.PublicKey{
		Parameters: dsa.Parameters{
			P: new(big.Int).Add(new(big.Int).Lsh(big.NewInt(1), 1023), big.NewInt(643)),
			Q: new(big.Int).Add(new(big.Int).Lsh(big.NewInt(1), 159), big.NewInt(7)),
			G: big.NewInt(2),
		},
		Y: big.NewInt(4),
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(public)))
	if _, err := ssh.ParsePublicKey(public.Marshal()); err != nil {
		t.Fatal("fixture must be parseable:", err)
	}
	if _, err := pinned(encoded); err == nil {
		t.Fatal("explicit pin negotiation must not enable legacy DSA")
	}
}
