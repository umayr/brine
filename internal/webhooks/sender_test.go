package webhooks

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestSignedDeliveryAndAttemptHistory(t *testing.T) {
	ctx := context.Background()
	s := testService(t, 10)
	ep, key := testEndpoint(t, s)
	s.Publish(ctx, testEvent("evt_signed"))
	called := false
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		body, _ := io.ReadAll(r.Body)
		if r.URL.String() != ep.URL || r.Header.Get("Brine-Event-ID") != "evt_signed" || r.Header.Get("Brine-Delivery-ID") == "" {
			t.Error("delivery identity missing")
		}
		if e := Verify([]byte(key), r.Header.Get("Brine-Timestamp"), r.Header.Get("Brine-Signature"), body, s.now()); e != nil {
			t.Error(e)
		}
		return &http.Response{StatusCode: 204, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("sensitive response not retained"))}, nil
	})}
	if sent, e := s.sendOne(ctx); !sent || e != nil {
		t.Fatalf("send %v %v", sent, e)
	}
	if !called {
		t.Fatal("not delivered")
	}
	ds, _ := s.Deliveries(ctx, ep.ID)
	if ds[0].Status != "delivered" || ds[0].Event.Outcome != "unknown" {
		t.Fatal("delivery altered operation outcome")
	}
	as, e := s.Attempts(ctx, ds[0].ID)
	if e != nil || len(as) != 1 || as[0].HTTPStatus != 204 {
		t.Fatal("attempt not recorded")
	}
	if e = s.Redeliver(ctx, ds[0].ID); e != nil {
		t.Fatal(e)
	}
	s.sendOne(ctx)
	ds, _ = s.Deliveries(ctx, ep.ID)
	if ds[0].Attempts != 2 {
		t.Fatal("redelivery history lost")
	}
}
func TestRetryPolicyAndPause(t *testing.T) {
	for _, status := range []int{200, 302, 400, 401, 408, 429, 500, 503} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			ctx := context.Background()
			s := testService(t, 10)
			ep, _ := testEndpoint(t, s)
			s.Publish(ctx, testEvent("evt_retry"))
			s.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": []string{"3600"}}, Body: io.NopCloser(strings.NewReader("private"))}, nil
			})}
			s.sendOne(ctx)
			ds, _ := s.Deliveries(ctx, ep.ID)
			want := "exhausted"
			if status == 200 {
				want = "delivered"
			} else if status == 408 || status == 429 || status >= 500 {
				want = "pending"
			}
			if ds[0].Status != want {
				t.Fatalf("%d: %s", status, ds[0].Status)
			}
			if want == "pending" && ds[0].NextAttemptAt.Sub(ds[0].UpdatedAt) < 59*time.Minute {
				t.Fatal("Retry-After ignored")
			}
		})
	}
	ctx := context.Background()
	s := testService(t, 10)
	ep, _ := testEndpoint(t, s)
	s.Publish(ctx, testEvent("evt_paused"))
	ep.Enabled = false
	s.SaveEndpoint(ctx, ep)
	if sent, e := s.sendOne(ctx); sent || e != nil {
		t.Fatal("paused endpoint sent")
	}
}
func TestNetworkErrorsAreSanitizedAndBounded(t *testing.T) {
	ctx := context.Background()
	s := testService(t, 10)
	ep, _ := testEndpoint(t, s)
	s.Publish(ctx, testEvent("evt_network"))
	s.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("credential=private") })}
	for attempt := 1; attempt <= 8; attempt++ {
		s.db.Exec("UPDATE deliveries SET next_at=0")
		sent, e := s.sendOne(ctx)
		if !sent || e != nil {
			t.Fatal(e)
		}
	}
	ds, _ := s.Deliveries(ctx, ep.ID)
	if ds[0].Status != "exhausted" || ds[0].Attempts != 8 || ds[0].ErrorCategory != "network_unavailable" {
		t.Fatalf("unbounded or raw error: %+v", ds[0])
	}
}
func TestRunCloseDrain(t *testing.T) {
	s := testService(t, 10)
	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background()) }()
	deadline := time.Now().Add(time.Second)
	for !s.running.Load() {
		if time.Now().After(deadline) {
			t.Fatal("worker not running")
		}
		time.Sleep(time.Millisecond)
	}
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not drain")
	}
	if e := s.Run(context.Background()); !errors.Is(e, ErrConflict) {
		t.Fatal("closed service restarted")
	}
}
func TestRetryAfterBounds(t *testing.T) {
	now := time.Now()
	for _, raw := range []string{"-1", "invalid", "9223372036854775808"} {
		if parseRetryAfter(raw, now) != 0 {
			t.Errorf("accepted %s", raw)
		}
	}
	if parseRetryAfter("999999999", now) != time.Hour {
		t.Fatal("unbounded retry-after")
	}
}
