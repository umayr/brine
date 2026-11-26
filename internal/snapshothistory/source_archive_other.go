//go:build !linux

package snapshothistory

import "context"

type sourceArchive struct{}

func initSource(string, string, string) error                   { return ErrUnavailable }
func openSource(string, string, string) (*sourceArchive, error) { return nil, ErrUnavailable }
func (*sourceArchive) close() error                             { return ErrUnavailable }
func (*sourceArchive) putChain(context.Context, string, []string, []SourceRevision) ([]string, error) {
	return nil, ErrUnavailable
}
func (*sourceArchive) get(string, string) (SourceRevision, error) {
	return SourceRevision{}, ErrUnavailable
}
