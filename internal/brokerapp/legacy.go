package brokerapp

import "context"

// WithUnprotected serializes ordinary remote admission against permanent broker
// registration, even after provider policy removal. No callbacks may reenter State.
func (s *Service) WithUnprotected(ctx context.Context, stack string, fn func()) error {
	if ctx == nil || ctx.Err() != nil || s == nil || s.State == nil || fn == nil {
		return ErrUnavailable
	}
	started := false
	err := s.State.WithUnprotected(stack, func() {
		if ctx.Err() == nil {
			started = true
			fn()
		}
	})
	if err == nil && !started {
		return ErrUnavailable
	}
	return err
}
