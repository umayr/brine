//go:build !linux

package store

import "context"

func (s *FSStore) ReviewSource(context.Context, string) (SourceView, error) {
	return SourceView{}, ErrSourceUnavailable
}
func (s *FSStore) CommitSource(context.Context, string, string, SourceChange) error {
	return ErrSourceUnavailable
}
