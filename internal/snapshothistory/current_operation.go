package snapshothistory

import (
	"context"
	"errors"
)

var ErrUnresolvedOperation = errors.New("unresolved local attended operation for enrollment and stack")

// CheckAttendedIdle reads the authenticated in-memory summary under the same
// mutex as Commit and evidence publication. It performs no archive I/O. This
// point-in-time guard is not a reservation or provider evidence. The caller
// supplies its authorized canonical enrollment scope, not request data. Commit
// still validates the full inventory atomically before publishing an intent.
func (s *Store) CheckAttendedIdle(ctx context.Context, scope, stack string) error {
	if s == nil || ctx == nil || ctx.Err() != nil || scope == "" || len(scope) > 512 || stack == "" || len(stack) > 128 || !s.mu.TryLock() {
		return ErrUnavailable
	}
	defer s.mu.Unlock()
	if s.impl == nil {
		return ErrUnavailable
	}
	return s.impl.checkAttendedIdle(ctx, scope, stack)
}
