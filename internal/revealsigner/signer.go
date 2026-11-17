// Package revealsigner loads an operator-provisioned Ed25519 reveal signing key.
package revealsigner

import (
	"context"
	"crypto/ed25519"
	"errors"
)

// ErrKey denotes an invalid, unsafe, or unavailable signing key. It deliberately
// excludes the path and underlying errors.
var ErrKey = errors.New("reveal signing key invalid or unavailable")

// Signer holds a signing key in memory for the life of the process.
type Signer struct{ private ed25519.PrivateKey }

// PublicKey returns an independent copy of the public key.
func (s *Signer) PublicKey() ed25519.PublicKey {
	return append(ed25519.PublicKey(nil), s.private[ed25519.SeedSize:]...)
}

// Sign signs synchronously and checks cancellation both before and after signing.
func (s *Signer) Sign(ctx context.Context, message []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sig := ed25519.Sign(s.private, message)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return sig, nil
}
