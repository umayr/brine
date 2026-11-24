package remoteui

import (
	"context"
	"sync"
)

// Stack keys preserve source exclusion even when a different host is selected.
// Pollers queue in arrival order; interactive work cannot bypass queued recovery.
type deploymentAdmission struct {
	mu     sync.Mutex
	active map[string]bool
	queue  []*deploymentWaiter
}
type deploymentWaiter struct {
	stack   string
	ready   chan struct{}
	granted bool
}

func (g *deploymentAdmission) reserve(stack string) func() {
	if g.active == nil {
		g.active = make(map[string]bool)
	}
	g.active[stack] = true
	var once sync.Once
	return func() { once.Do(func() { g.mu.Lock(); defer g.mu.Unlock(); delete(g.active, stack); g.pump() }) }
}
func (g *deploymentAdmission) try(stack string) (func(), bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.queue) > 0 || g.active[stack] || len(g.active) >= 4 {
		return nil, false
	}
	return g.reserve(stack), true
}

// pump grants the oldest runnable waiter; a busy stack does not block others.
func (g *deploymentAdmission) pump() {
	for i := 0; i < len(g.queue) && len(g.active) < 4; {
		w := g.queue[i]
		if g.active[w.stack] {
			i++
			continue
		}
		g.queue = append(g.queue[:i], g.queue[i+1:]...)
		if g.active == nil {
			g.active = make(map[string]bool)
		}
		g.active[w.stack] = true
		w.granted = true
		close(w.ready)
	}
}
func (g *deploymentAdmission) acquire(ctx context.Context, stack string) (func(), bool) {
	g.mu.Lock()
	if ctx.Err() != nil || len(g.queue) >= 32 {
		g.mu.Unlock()
		return nil, false
	}
	w := &deploymentWaiter{stack: stack, ready: make(chan struct{})}
	g.queue = append(g.queue, w)
	g.pump()
	g.mu.Unlock()
	select {
	case <-w.ready:
	case <-ctx.Done():
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if ctx.Err() != nil {
		if w.granted {
			delete(g.active, stack)
		} else {
			for i, v := range g.queue {
				if v == w {
					g.queue = append(g.queue[:i], g.queue[i+1:]...)
					break
				}
			}
		}
		g.pump()
		return nil, false
	}
	return g.reserve(stack), true
}
