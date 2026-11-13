//go:build !linux && !darwin

package auth

import "errors"

// File-backed credentials require the hardened primitives qualified on Linux and
// macOS. Explicit credentials remain usable without touching the filesystem.
type credentialState struct{}

func openCredentialState(string) (*credentialState, error) {
	return nil, errors.New("file-backed credentials unsupported on this platform")
}
func (*credentialState) close() {}
func (*credentialState) read() ([]byte, bool, error) {
	return nil, false, errors.New("credential state unavailable")
}
func (*credentialState) write([]byte) error { return errors.New("credential state unavailable") }
