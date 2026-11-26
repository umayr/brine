//go:build !linux

package store

import "context"

func (s *FSStore) ReviewSource(context.Context, string) (SourceView, error) {
	return SourceView{}, ErrSourceUnavailable
}
func (s *FSStore) CommitSource(context.Context, string, string, SourceChange) error {
	return ErrSourceUnavailable
}
func (s *FSStore) CommitAutomaticCheckpoint(context.Context, string, string, SourceChange) error {
	return ErrSourceUnavailable
}
func (s *FSStore) SourceCheckpointDefault() (bool, error) { return false, nil }
func (s *FSStore) SetSourceCheckpointDefault(context.Context, bool) error {
	return ErrSourceUnavailable
}
