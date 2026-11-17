//go:build !linux && !darwin

package revealsigner

import (
	"errors"
	"testing"
)

func TestOpenUnsupportedPlatform(t *testing.T) {
	if signer, err := Open("/key"); signer != nil || !errors.Is(err, ErrKey) {
		t.Fatal("unsupported platform did not fail closed")
	}
}
