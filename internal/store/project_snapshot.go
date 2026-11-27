//go:build linux || darwin

package store

import (
	"github.com/umayr/brine/internal/projectsnapshot"
	"os"
)

// ProjectSnapshot pins validated metadata and opens every workspace component
// relative to trusted store descriptors. No raw Git subfolder path is reopened.
func (s *FSStore) ProjectSnapshot(name string, limits projectsnapshot.Limits) (*Stack, projectsnapshot.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	stack, err := s.readMeta(name)
	if err != nil {
		return nil, projectsnapshot.Snapshot{}, err
	}
	if stack.protectsRemoteGit() {
		return nil, projectsnapshot.Snapshot{}, ErrRemoteGit
	}
	fd, err := s.workspaceFD(name, stack.Git)
	if err != nil {
		return nil, projectsnapshot.Snapshot{}, ErrSourceUnavailable
	}
	dir := os.NewFile(uintptr(fd), "stack workspace")
	defer dir.Close()
	snapshot, err := projectsnapshot.BuildDirectory(dir, entrypointOrDefault(stack.Entrypoint), limits)
	if err != nil {
		return nil, projectsnapshot.Snapshot{}, ErrSourceUnavailable
	}
	return stack, snapshot, nil
}
