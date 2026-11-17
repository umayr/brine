package agentclient

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestEndEOFGraceBeyondTailDeadline(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		time.Sleep(14500 * time.Millisecond)
		fmt.Fprintf(w, "{\"version\":%q,\"requestId\":%q,\"data\":{\"type\":\"end\",\"reason\":\"duration\",\"bytes\":0}}\n", version, r.Header.Get("X-Request-Id"))
		w.(http.Flusher).Flush()
		time.Sleep(time.Second)
	})
	if e := c.Logs(context.Background(), testEpoch, fixtureID, LogOptions{Tail: 1}, func(context.Context, LogEvent) error { return nil }); e != nil {
		t.Fatal(e)
	}
}
func TestEndEOFMissingFailsWithinGrace(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintf(w, "{\"version\":%q,\"requestId\":%q,\"data\":{\"type\":\"end\",\"reason\":\"complete\",\"bytes\":0}}\n", version, r.Header.Get("X-Request-Id"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	start := time.Now()
	if e := c.Logs(context.Background(), testEpoch, fixtureID, LogOptions{Tail: 1}, func(context.Context, LogEvent) error { return nil }); e == nil || time.Since(start) < 5*time.Second || time.Since(start) > 8*time.Second {
		t.Fatal(e, time.Since(start))
	}
}
