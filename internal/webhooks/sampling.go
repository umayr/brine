package webhooks

import (
	"context"
	"time"
)

// ConfigureSampling installs a read-only, context-cooperative observation source
// before Run. Its lifetime is joined by Run/Close, independent of delivery IO.
func (s *Service) ConfigureSampling(sample func(context.Context) error) error {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if s.closed || s.running.Load() {
		return ErrConflict
	}
	s.sample = sample
	return nil
}
func (s *Service) startSampling(ctx context.Context) func() {
	if s.sample == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer s.samplingStatus.Store(3)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			if ctx.Err() != nil {
				return
			}
			s.samplingStatus.Store(1)
			bounded, c := context.WithTimeout(ctx, 10*time.Second)
			e := s.sample(bounded)
			c()
			if e != nil {
				s.samplingStatus.Store(2)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}
