//go:build unix

package secretbindings

import (
	"crypto/rand"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ProofKeyPath ensures a valid key exists and returns its private path for Dill.
// This path is internal control-plane data, never an API response.
func (s *Store) ProofKeyPath(id string) (string, error) {
	if _, err := s.ProofKey(id); err != nil {
		return "", err
	}
	return filepath.Join(s.path, id, "proof-key"), nil
}

// RotateProofKey invalidates the current preview before replacing the key.
// Callers must hold the deployment coordinator for the whole operation.
func (s *Store) RotateProofKey(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fd, err := s.deployment(id, true)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if err = removeAt(fd, "preview.json"); err != nil {
		return err
	}
	var key [32]byte
	if _, err = rand.Read(key[:]); err != nil {
		return ErrUnsafe
	}
	return writeAt(fd, "proof-key", key[:])
}

// MarkSecretEnabled records monotonic routing metadata independently of the
// recipe. It never clears the marker, including after a failed apply.
func (s *Store) MarkSecretEnabled(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fd, err := s.deployment(id, true)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	return writeAt(fd, "secret-enabled", []byte("1\n"))
}
