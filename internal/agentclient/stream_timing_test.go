package agentclient

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestCallbackBackpressureBound(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintf(w, "{\"version\":%q,\"requestId\":%q,\"data\":{\"type\":\"heartbeat\",\"sequence\":1}}\n", version, r.Header.Get("X-Request-Id"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	start := time.Now()
	e := c.Logs(context.Background(), testEpoch, fixtureID, LogOptions{Tail: 1, Follow: true}, func(ctx context.Context, _ LogEvent) error { <-ctx.Done(); return ctx.Err() })
	if e == nil || time.Since(start) < 5*time.Second || time.Since(start) > 8*time.Second {
		t.Fatal(e, time.Since(start))
	}
}
func TestFollowHeartbeatDeadline(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	start := time.Now()
	e := c.Logs(context.Background(), testEpoch, fixtureID, LogOptions{Tail: 1, Follow: true}, func(context.Context, LogEvent) error { return nil })
	if e == nil || time.Since(start) < 30*time.Second || time.Since(start) > 34*time.Second {
		t.Fatal(e, time.Since(start))
	}
}
