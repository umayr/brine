package snapshothistory

import (
	"context"
	"errors"
)

var ErrUnresolvedOperation = errors.New("unresolved local attended operation for enrollment and stack")

// CheckAttendedIdle checks the full local archive under the same mutex and
// exclusive writer lock as Commit and evidence publication. It is a point-in-
// time guard, not a reservation and not provider evidence. The caller must
// supply its authorized canonical enrollment scope, not request data. A later
// Commit rechecks atomically before publishing a new attended intent.
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
