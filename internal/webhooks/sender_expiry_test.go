package webhooks

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestExpiredBacklogReportsProgressBeforeLiveDelivery(t *testing.T) {
	s := testService(t, 10)
	ctx := context.Background()
	testEndpoint(t, s)
	for _, id := range []string{"evt_old_a", "evt_old_b", "evt_old_c"} {
		if e := s.Publish(ctx, testEvent(id)); e != nil {
			t.Fatal(e)
		}
	}
	now := s.now().Add(25 * time.Hour)
	s.now = func() time.Time { return now }
	if e := s.Publish(ctx, testEvent("evt_fresh")); e != nil {
		t.Fatal(e)
	}
	calls := 0
	s.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 204, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	for i := 0; i < 4; i++ {
		progress, e := s.sendOne(ctx)
		if e != nil || !progress {
			t.Fatal("queue unnecessarily waits despite progress", i, progress, e)
		}
		if i < 3 && calls != 0 {
			t.Fatal("expired delivery sent")
		}
	}
	if calls != 1 {
		t.Fatal("live receiver starved", calls)
	}
	if progress, e := s.sendOne(ctx); e != nil || progress {
		t.Fatal("empty queue", progress, e)
	}
}
