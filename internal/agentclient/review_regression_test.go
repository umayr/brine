package agentclient

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestNon200InvalidatesBeforeContentType(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusConflict, http.StatusServiceUnavailable} {
		for _, stream := range []bool{false, true} {
			t.Run(http.StatusText(status)+map[bool]string{true: "/logs", false: "/inventory"}[stream], func(t *testing.T) {
				var requests atomic.Int32
				c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					w.Header().Set("Content-Type", "text/plain")
					w.WriteHeader(status)
				})
				if stream {
					if e := c.Logs(context.Background(), testEpoch, fixtureID, LogOptions{Tail: 1}, func(context.Context, LogEvent) error { return nil }); e == nil {
						t.Fatal("expected failure")
					}
				} else {
					if _, e := c.Inventory(context.Background(), testEpoch, nil, 1); e == nil {
						t.Fatal("expected failure")
					}
				}
				if _, e := c.Inventory(context.Background(), testEpoch, nil, 1); e == nil {
					t.Fatal("old epoch accepted")
				}
				if requests.Load() != 1 {
					t.Fatal("followup reached agent", requests.Load())
				}
				if _, e := c.Session(context.Background()); e == nil {
					t.Fatal("dead client re-proved")
				}
			})
		}
	}
}

// Simulate a previously queued timer callback after terminal acceptance; it
// must not cancel the independent EOF grace even though Stop cannot retract it.
func TestStreamClockQueuedExpirationAfterEnd(t *testing.T) {
	var cancels atomic.Int32
	c := newStreamClock(time.Hour, true, func() { cancels.Add(1) })
	defer c.stop()
	if !c.acceptEnd() {
		t.Fatal("end not accepted")
	}
	c.check()
	if cancels.Load() != 0 {
		t.Fatal("stale timer canceled terminal grace")
	}
}
func TestStreamClockExpiredBeforeEnd(t *testing.T) {
	var cancels atomic.Int32
	c := newStreamClock(time.Hour, false, func() { cancels.Add(1) })
	defer c.stop()
	c.mu.Lock()
	c.deadline = time.Now().Add(-time.Second)
	c.mu.Unlock()
	if c.acceptEnd() {
		t.Fatal("expired end accepted")
	}
	if cancels.Load() != 1 {
		t.Fatal("deadline did not cancel")
	}
}
