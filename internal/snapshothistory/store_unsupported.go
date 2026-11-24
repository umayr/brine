//go:build !linux

package snapshothistory

import "context"

// The v1 archive relies on Linux descriptor-relative no-follow traversal, POSIX
// ACL inspection, flock, no-clobber rename publication, and file/directory fsync.
// Other platforms fail closed rather than approximating those guarantees.
type store struct{}

func initialize(context.Context, Options) error          { return ErrUnavailable }
func openStore(context.Context, Options) (*store, error) { return nil, ErrUnavailable }
func (*store) commit(context.Context, CommitRequest) (OpenRequest, error) {
	return OpenRequest{}, ErrUnavailable
}
func (*store) open(context.Context, OpenRequest) (Record, error) { return Record{}, ErrUnavailable }
func (*store) discoverRestartMetadata(context.Context, RestartDiscoveryRequest, int64) (RestartDiscoveryPage, error) {
	return RestartDiscoveryPage{}, ErrUnavailable
}
func (*store) close() error { return nil }
