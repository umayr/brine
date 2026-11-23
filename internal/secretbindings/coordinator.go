package secretbindings

import (
	"context"
	"sync"
)

// Coordinator serializes Brine-managed operations for a deployment within one
// process. Callers must share the same instance and hold its lock across the
// entire workspace, preview, or lifecycle operation. It is not a host lock.
type Coordinator struct {
	mu    sync.Mutex
	locks map[string]chan struct{}
}

func (c *Coordinator) Lock(deployment string) func() {
	unlock, _ := c.LockContext(context.Background(), deployment)
	return unlock
}

// LockContext waits for the same lock as Lock, but stops waiting on cancellation.
func (c *Coordinator) LockContext(ctx context.Context, deployment string) (func(), error) {
	c.mu.Lock()
	if c.locks == nil {
		c.locks = make(map[string]chan struct{})
	}
	ch := c.locks[deployment]
	if ch == nil {
		ch = make(chan struct{}, 1)
		ch <- struct{}{}
		c.locks[deployment] = ch
	}
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-ch:
		if err := ctx.Err(); err != nil {
			ch <- struct{}{}
			return nil, err
		}
		return func() { ch <- struct{}{} }, nil
	}
}
