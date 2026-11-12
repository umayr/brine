//go:build unix

package secretbindings

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ProofOutput reserves a unique private directory, not the output file: Dill
// requires a fresh, nonexistent output path for its atomic creation.
type ProofOutput struct {
	parent int
	dir    int
	name   string
	path   string
}

func (s *Store) AllocateProofOutput(id string) (*ProofOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	parent, err := s.deployment(id, true)
	if err != nil {
		return nil, err
	}
	name, err := randomName()
	if err != nil {
		unix.Close(parent)
		return nil, err
	}
	name = "proof-" + name
	if unix.Mkdirat(parent, name, 0700) != nil {
		unix.Close(parent)
		return nil, ErrUnsafe
	}
	dir, err := openDirAt(parent, name, true)
	if err != nil {
		unix.Unlinkat(parent, name, unix.AT_REMOVEDIR)
		unix.Close(parent)
		return nil, err
	}
	return &ProofOutput{parent: parent, dir: dir, name: name, path: filepath.Join(s.path, id, name, "proof.json")}, nil
}
func (p *ProofOutput) Path() string { return p.path }

// Read returns only bounded private output; parsing and plan status checks are
// the responsibility of the preview workflow before SavePreview.
func (p *ProofOutput) Read() ([]byte, error) { return readAt(p.dir, "proof.json", 4096) }

// Close removes only this reservation's regular, private, single-link output.
func (p *ProofOutput) Close() error {
	if p.parent < 0 {
		return ErrUnsafe
	}
	err := removeAt(p.dir, "proof.json")
	if err == nil {
		if e := unix.Unlinkat(p.parent, p.name, unix.AT_REMOVEDIR); e != nil && !errors.Is(e, os.ErrNotExist) {
			err = ErrUnsafe
		}
	}
	unix.Close(p.dir)
	unix.Close(p.parent)
	p.parent = -1
	p.dir = -1
	return err
}
