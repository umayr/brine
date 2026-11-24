package snapshothistory

import (
	"context"
	"strings"
)

// AttendedBindingSelector is supplied only by the trusted server from its
// active enrollment and authority, never from locator metadata or a browser.
// It is an exact match, not an alternative to authorizing the actor.
type AttendedBindingSelector struct {
	Locator                                 ObjectID
	Scope, Target, AuthorityRevision, Stack string
}

// AttendedBindingAuthorizer must authorize this exact actor/object/enrollment
// while the caller holds the relevant authority lease. A locator is not a grant.
type AttendedBindingAuthorizer func(context.Context, AttendedBindingSelector) error

// ResolveAttendedBinding returns the original authenticated, in-process open
// binding, without snapshot bytes or provider outcomes. It is deliberately not
// a route. Denial, foreign scope, absence, and corruption return the same error.
// The returned binding is not reusable authorization for a later action.
func (s *Store) ResolveAttendedBinding(ctx context.Context, q AttendedBindingSelector, authorize AttendedBindingAuthorizer) (OpenRequest, error) {
	if s == nil || ctx == nil || ctx.Err() != nil || authorize == nil || q.Locator == (ObjectID{}) ||
		len(q.Scope) == 0 || len(q.Scope) > 512 || len(q.Target) != 64 || len(q.AuthorityRevision) != 64 ||
		len(q.Stack) == 0 || len(q.Stack) > 128 || strings.ContainsAny(q.Scope, "\x00\r\n") {
		return OpenRequest{}, ErrUnavailable
	}
	if authorize(ctx, q) != nil || ctx.Err() != nil {
		return OpenRequest{}, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.impl == nil {
		return OpenRequest{}, ErrUnavailable
	}
	return s.impl.resolveAttendedBinding(ctx, q)
}
