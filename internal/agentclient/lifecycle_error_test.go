package agentclient

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestPendingLogErrorHonorsExternalLifecycle(t *testing.T) {
	for _, mode := range []string{"caller", "close", "forward"} {
		t.Run(mode, func(t *testing.T) {
			forward, loseForward := context.WithCancel(context.Background())
			defer loseForward()
			headers := make(chan struct{})
			c := testClientWithForward(t, forward, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(404)
				w.(http.Flusher).Flush()
				close(headers)
				<-r.Context().Done()
			})
			c.epochCtx, c.epochCancel = context.WithCancel(context.Background())
			caller, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- c.Logs(caller, testEpoch, fixtureID, LogOptions{Tail: 1}, func(context.Context, LogEvent) error { return nil })
			}()
			<-headers
			deadline := time.Now().Add(2 * time.Second)
			for {
				c.mu.Lock()
				retired := c.dead
				c.mu.Unlock()
				if retired {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("status not retired")
				}
				time.Sleep(time.Millisecond)
			}
			if _, e := c.Inventory(context.Background(), testEpoch, nil, 1); e == nil {
				t.Fatal("old epoch accepted while error body pending")
			}
			switch mode {
			case "caller":
				cancel()
			case "close":
				c.Close()
			case "forward":
				loseForward()
			}
			select {
			case e := <-done:
				if e == nil {
					t.Fatal("expected failure")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("pending body not cancelled")
			}
		})
	}
}
