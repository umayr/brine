package agentclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestLogDelayedErrorBodySurvivesRetirement(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
	}{{404, "NOT_FOUND"}, {409, "BINDING_CHANGED"}} {
		t.Run(tc.code, func(t *testing.T) {
			var calls atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				w.(http.Flusher).Flush()
				time.Sleep(50 * time.Millisecond)
				fmt.Fprintf(w, `{"version":%q,"requestId":%q,"error":{"code":%q,"message":"private remote details","retryable":false}}`, version, r.Header.Get("X-Request-Id"), tc.code)
			})
			c.epochCtx, c.epochCancel = context.WithCancel(context.Background())
			e := c.Logs(context.Background(), testEpoch, fixtureID, LogOptions{Tail: 1}, func(context.Context, LogEvent) error { return nil })
			var remote *RemoteError
			if !errors.As(e, &remote) || remote.Code != tc.code {
				t.Fatalf("got %v", e)
			}
			if e.Error() == "private remote details" {
				t.Fatal("message exposed")
			}
			if _, e = c.Inventory(context.Background(), testEpoch, nil, 1); e == nil {
				t.Fatal("old epoch accepted")
			}
			if calls.Load() != 1 {
				t.Fatal("follow-up sent")
			}
		})
	}
}
func TestLogDelayedErrorBodyCallerCancellation(t *testing.T) {
	started := make(chan struct{})
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(404)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- c.Logs(ctx, testEpoch, fixtureID, LogOptions{Tail: 1}, func(context.Context, LogEvent) error { return nil })
	}()
	<-started
	cancel()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("expected error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("body read did not cancel")
	}
}
