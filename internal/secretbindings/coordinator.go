package secretbindings

import "sync"

// Coordinator serializes Brine-managed operations for a deployment within one
// process. Callers must share the same instance and hold its lock across the
// entire workspace, preview, or lifecycle operation. It is not a host lock.
type Coordinator struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (c *Coordinator) Lock(deployment string) func() {
	c.mu.Lock()
	if c.locks == nil {
		c.locks = make(map[string]*sync.Mutex)
	}
	m := c.locks[deployment]
	if m == nil {
		m = &sync.Mutex{}
		c.locks[deployment] = m
	}
	c.mu.Unlock()
	m.Lock()
	return m.Unlock
}
