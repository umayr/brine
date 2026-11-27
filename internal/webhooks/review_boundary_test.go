package webhooks

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestStoppedIncomingWorkerRefusesNewButPreservesDuplicate(t *testing.T) {
	s := testService(t, 10)
	tr, key := testTrigger(t, s)
	d := &testDispatcher{}
	first, err := acceptRequest(s, tr, key, `{"id":"accepted-before-stop"}`, d)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = s.RunTriggers(ctx, d); err != nil {
		t.Fatal(err)
	}
	duplicate, err := acceptRequest(s, tr, key, `{"id":"accepted-before-stop"}`, nil)
	if err != nil || duplicate.ID != first.ID {
		t.Fatal("stopped worker lost duplicate read", err)
	}
	if _, err = acceptRequest(s, tr, key, `{"id":"new-after-stop"}`, d); err != errStorage {
		t.Fatal("stopped worker accepted new work", err)
	}
}
func TestRetryDeadlineAtClaimStillAllowsExplicitRedelivery(t *testing.T) {
	s := testService(t, 10)
	ctx := context.Background()
	ep, _ := testEndpoint(t, s)
	if err := s.Publish(ctx, testEvent("evt_offline_deadline")); err != nil {
		t.Fatal(err)
	}
	now := s.now().Add(25 * time.Hour)
	s.now = func() time.Time { return now }
	calls := 0
	s.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 204, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	if _, err := s.sendOne(ctx); err != nil {
		t.Fatal(err)
	}
	ds, err := s.Deliveries(ctx, ep.ID)
	if err != nil || len(ds) != 1 || ds[0].Status != "exhausted" || ds[0].Attempts != 0 || calls != 0 {
		t.Fatal("late automatic request sent", ds, err, calls)
	}
	if err = s.Redeliver(ctx, ds[0].ID); err != nil {
		t.Fatal(err)
	}
	if sent, err := s.sendOne(ctx); err != nil || !sent || calls != 1 {
		t.Fatal("explicit redelivery refused", sent, err, calls)
	}
}
