package agentclient

import (
	"sync"
	"time"
)

// streamClock serializes expiration and end acceptance. A timer callback already
// queued when Stop is called cannot cancel a successfully accepted terminal frame.
type streamClock struct {
	mu             sync.Mutex
	timer          *time.Timer
	end, expired   bool
	deadline, idle time.Time
	follow         bool
	cancel         func()
}

func newStreamClock(duration time.Duration, follow bool, cancel func()) *streamClock {
	now := time.Now()
	c := &streamClock{deadline: now.Add(duration), follow: follow, cancel: cancel}
	if follow {
		c.idle = now.Add(30 * time.Second)
	}
	c.timer = time.AfterFunc(duration, c.check)
	c.mu.Lock()
	c.armLocked(now)
	c.mu.Unlock()
	return c
}
func (c *streamClock) next() time.Time {
	if c.follow && c.idle.Before(c.deadline) {
		return c.idle
	}
	return c.deadline
}
func (c *streamClock) armLocked(now time.Time) {
	delay := c.next().Sub(now)
	if delay < 0 {
		delay = 0
	}
	c.timer.Reset(delay)
}
func (c *streamClock) check() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.end || c.expired {
		return
	}
	now := time.Now()
	if !now.Before(c.next()) {
		c.expired = true
		c.cancel()
		return
	}
	c.armLocked(now)
}
func (c *streamClock) activity() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.end || c.expired {
		return false
	}
	now := time.Now()
	if !now.Before(c.next()) {
		c.expired = true
		c.cancel()
		return false
	}
	if c.follow {
		c.idle = now.Add(30 * time.Second)
		c.armLocked(now)
	}
	return true
}
func (c *streamClock) acceptEnd() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.end || c.expired {
		return false
	}
	if !time.Now().Before(c.next()) {
		c.expired = true
		c.cancel()
		return false
	}
	c.end = true
	c.timer.Stop()
	return true
}
func (c *streamClock) stop() { c.mu.Lock(); c.end = true; c.timer.Stop(); c.mu.Unlock() }
